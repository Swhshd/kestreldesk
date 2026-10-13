package services

// This file has been modified for the KestrelDesk project.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/Swhshd/kestreldesk/internal/pkg/httpx/params"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

var ChannelMessageOutboxService = newChannelMessageOutboxService()

func newChannelMessageOutboxService() *channelMessageOutboxService {
	return &channelMessageOutboxService{}
}

type channelMessageOutboxService struct {
}

const (
	channelOutboxPreflightMaxRetries = 5
	channelOutboxPreflightRetryBase  = 20 * time.Second
	channelOutboxDispatchClaimLease  = 15 * time.Minute
	staleOutboxClaimManualReviewText = "delivery outcome unknown after stale sending claim; check provider status before manual retry"
)

func (s *channelMessageOutboxService) Get(id int64) *models.ChannelMessageOutbox {
	return repositories.ChannelMessageOutboxRepository.Get(sqls.DB(), id)
}

func (s *channelMessageOutboxService) Take(where ...interface{}) *models.ChannelMessageOutbox {
	return repositories.ChannelMessageOutboxRepository.Take(sqls.DB(), where...)
}

func (s *channelMessageOutboxService) Find(cnd *sqls.Cnd) []models.ChannelMessageOutbox {
	return repositories.ChannelMessageOutboxRepository.Find(sqls.DB(), cnd)
}

func (s *channelMessageOutboxService) FindOne(cnd *sqls.Cnd) *models.ChannelMessageOutbox {
	return repositories.ChannelMessageOutboxRepository.FindOne(sqls.DB(), cnd)
}

func (s *channelMessageOutboxService) FindPageByParams(params *params.QueryParams) (list []models.ChannelMessageOutbox, paging *sqls.Paging) {
	return repositories.ChannelMessageOutboxRepository.FindPageByParams(sqls.DB(), params)
}

func (s *channelMessageOutboxService) FindPageByCnd(cnd *sqls.Cnd) (list []models.ChannelMessageOutbox, paging *sqls.Paging) {
	return repositories.ChannelMessageOutboxRepository.FindPageByCnd(sqls.DB(), cnd)
}

func (s *channelMessageOutboxService) Count(cnd *sqls.Cnd) int64 {
	return repositories.ChannelMessageOutboxRepository.Count(sqls.DB(), cnd)
}

func (s *channelMessageOutboxService) Create(t *models.ChannelMessageOutbox) error {
	return repositories.ChannelMessageOutboxRepository.Create(sqls.DB(), t)
}

func (s *channelMessageOutboxService) Update(t *models.ChannelMessageOutbox) error {
	return repositories.ChannelMessageOutboxRepository.Update(sqls.DB(), t)
}

func (s *channelMessageOutboxService) Updates(id int64, columns map[string]interface{}) error {
	return repositories.ChannelMessageOutboxRepository.Updates(sqls.DB(), id, columns)
}

func (s *channelMessageOutboxService) UpdateClaimed(outbox *models.ChannelMessageOutbox, columns map[string]any) error {
	return s.updateClaimed(sqls.DB(), outbox, columns)
}

func (s *channelMessageOutboxService) TouchDispatchClaim(outbox *models.ChannelMessageOutbox, now time.Time) (bool, error) {
	if outbox == nil || outbox.ID <= 0 || strings.TrimSpace(outbox.DispatchClaimToken) == "" {
		return false, nil
	}
	// Keep the lease timestamp monotonic at millisecond precision so a fast
	// sequence of provider calls still produces a changed database value.
	now = now.Truncate(time.Millisecond)
	lastUpdatedAt := outbox.UpdatedAt.Truncate(time.Millisecond)
	if !now.After(lastUpdatedAt) {
		now = lastUpdatedAt.Add(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	updated, err := repositories.ChannelMessageOutboxRepository.TouchDispatchClaim(sqls.DB().WithContext(ctx), outbox, now)
	if err == nil && updated {
		outbox.UpdatedAt = now
	}
	return updated, err
}

func (s *channelMessageOutboxService) updateClaimed(db *gorm.DB, outbox *models.ChannelMessageOutbox, columns map[string]any) error {
	if outbox == nil {
		return nil
	}
	if columns == nil {
		columns = make(map[string]any)
	}
	preserveWxWorkSendClaim := outbox.ChannelType == string(enums.ChannelTypeWxWorkKF) &&
		columns["send_status"] == string(enums.ChannelMessageOutboxStatusSent)
	if preserveWxWorkSendClaim {
		delete(columns, "dispatch_claim_token")
	} else {
		columns["dispatch_claim_token"] = ""
	}
	updated, err := repositories.ChannelMessageOutboxRepository.UpdatesIfClaim(db, outbox, columns)
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("outbox record %d dispatch claim is no longer current", outbox.ID)
	}
	return nil
}

func (s *channelMessageOutboxService) ClaimForSending(outbox *models.ChannelMessageOutbox, now time.Time) (bool, error) {
	if outbox == nil {
		return false, nil
	}
	claimToken := uuid.NewString()
	claimed, claimErr := repositories.ChannelMessageOutboxRepository.ClaimForSending(sqls.DB(), outbox, now, claimToken)
	if claimErr == nil {
		if claimed {
			outbox.DispatchClaimToken = claimToken
			outbox.UpdatedAt = now
		}
		return claimed, nil
	}

	// A lost database response can follow a committed claim. The unique token
	// proves this caller owns that pre-provider claim before it is returned to
	// the retry queue; another dispatcher’s sending row is left untouched.
	claimedRow, lookupErr := repositories.ChannelMessageOutboxRepository.GetForOutboundDispatch(sqls.DB(), outbox.ID)
	if lookupErr == nil && claimedRow != nil &&
		claimedRow.SendStatus == string(enums.ChannelMessageOutboxStatusSending) && claimedRow.DispatchClaimToken == claimToken {
		outbox.DispatchClaimToken = claimToken
		if retryErr := s.MarkClaimResponseLossForRetry(outbox, claimToken); retryErr != nil {
			return false, errors.Join(claimErr, retryErr)
		}
	}
	return false, claimErr
}

func (s *channelMessageOutboxService) MarkAmbiguousDeliveryFailure(outbox *models.ChannelMessageOutbox, reason string) error {
	if outbox == nil {
		return nil
	}
	return s.UpdateClaimed(outbox, map[string]interface{}{
		"send_status":          string(enums.ChannelMessageOutboxStatusFailed),
		"dispatch_claim_token": "",
		"retry_count":          outbox.RetryCount + 1,
		"next_retry_at":        nil,
		"last_error":           reason,
		"updated_at":           time.Now(),
	})
}

// MarkPreflightLookupFailure retries database errors that happen before any
// provider request, where replaying the outbox item cannot duplicate a send.
func (s *channelMessageOutboxService) MarkPreflightLookupFailure(outbox *models.ChannelMessageOutbox, lookup string) error {
	if outbox == nil {
		return nil
	}
	retryCount := outbox.RetryCount + 1
	now := time.Now()
	columns := map[string]interface{}{
		"retry_count":          retryCount,
		"last_error":           "temporary database lookup failure: " + lookup,
		"updated_at":           now,
		"dispatch_claim_token": "",
	}
	if retryCount >= channelOutboxPreflightMaxRetries {
		columns["send_status"] = string(enums.ChannelMessageOutboxStatusFailed)
		columns["next_retry_at"] = nil
	} else {
		delay := channelOutboxPreflightRetryBase
		for attempt := 1; attempt < retryCount; attempt++ {
			delay *= 2
		}
		nextRetry := now.Add(delay)
		columns["send_status"] = string(enums.ChannelMessageOutboxStatusPending)
		columns["next_retry_at"] = &nextRetry
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.updateClaimed(sqls.DB().WithContext(ctx), outbox, columns)
}

func (s *channelMessageOutboxService) MarkClaimResponseLossForRetry(outbox *models.ChannelMessageOutbox, claimToken string) error {
	if outbox == nil || claimToken == "" {
		return nil
	}
	retryCount := outbox.RetryCount + 1
	now := time.Now()
	columns := map[string]interface{}{
		"retry_count":          retryCount,
		"last_error":           "temporary database claim response failure",
		"updated_at":           now,
		"dispatch_claim_token": "",
	}
	if retryCount >= channelOutboxPreflightMaxRetries {
		columns["send_status"] = string(enums.ChannelMessageOutboxStatusFailed)
		columns["next_retry_at"] = nil
	} else {
		delay := channelOutboxPreflightRetryBase
		for attempt := 1; attempt < retryCount; attempt++ {
			delay *= 2
		}
		nextRetry := now.Add(delay)
		columns["send_status"] = string(enums.ChannelMessageOutboxStatusPending)
		columns["next_retry_at"] = &nextRetry
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	updated, err := repositories.ChannelMessageOutboxRepository.RetryUnsentClaimAfterResponseLoss(
		sqls.DB().WithContext(ctx), outbox.ID, outbox.ChannelType, claimToken, columns)
	if err != nil {
		return err
	}
	if !updated {
		return fmt.Errorf("outbox record %d claim ownership changed before retry", outbox.ID)
	}
	return nil
}

func isOutboundLookupNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func isRetryableOutboundDatabaseError(err error) bool {
	if err == nil || isOutboundLookupNotFound(err) || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, sql.ErrConnDone) || errors.Is(err, driver.ErrBadConn) {
		return true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return true
	}
	var mysqlErr *drivermysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	switch mysqlErr.Number {
	case 1040, // too many connections
		1205, // lock wait timeout
		1213, // deadlock
		2002, // connection refused / cannot connect
		2006, // server gone away
		2013, // lost connection during query
		2055: // lost connection with extended error details
		return true
	default:
		return false
	}
}

func isRetryableOutboundStorageError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return networkErr.Timeout() || networkErr.Temporary()
	}
	var ossErr *oss.ServiceError
	if errors.As(err, &ossErr) && ossErr != nil {
		return ossErr.StatusCode == 429 || ossErr.StatusCode >= 500
	}
	return false
}

func handleRequiredOutboundLookupFailure(
	outbox *models.ChannelMessageOutbox,
	lookup string,
	missingReason string,
	lookupErr error,
	markPermanent func(*models.ChannelMessageOutbox, string) error,
) error {
	if isOutboundLookupNotFound(lookupErr) {
		return markPermanent(outbox, missingReason)
	}
	if isRetryableOutboundDatabaseError(lookupErr) {
		return ChannelMessageOutboxService.MarkPreflightLookupFailure(outbox, lookup)
	}
	return markPermanent(outbox, "database lookup failed: "+lookup)
}

func handleOptionalOutboundLookupFailure(
	outbox *models.ChannelMessageOutbox,
	lookup string,
	lookupErr error,
	markPermanent func(*models.ChannelMessageOutbox, string) error,
) (bool, error) {
	if lookupErr == nil || isOutboundLookupNotFound(lookupErr) {
		return false, nil
	}
	if isRetryableOutboundDatabaseError(lookupErr) {
		return true, ChannelMessageOutboxService.MarkPreflightLookupFailure(outbox, lookup)
	}
	return true, markPermanent(outbox, "database lookup failed: "+lookup)
}

func (s *channelMessageOutboxService) UpdateColumn(id int64, name string, value interface{}) error {
	return repositories.ChannelMessageOutboxRepository.UpdateColumn(sqls.DB(), id, name, value)
}

func (s *channelMessageOutboxService) Delete(id int64) {
	repositories.ChannelMessageOutboxRepository.Delete(sqls.DB(), id)
}

// GetByMessageID retrieves the outbox entry by message ID and channel type.
func (s *channelMessageOutboxService) GetByMessageID(channelType string, messageID int64) *models.ChannelMessageOutbox {
	return repositories.ChannelMessageOutboxRepository.Take(sqls.DB(), "channel_type = ? AND message_id = ?", channelType, messageID)
}

func (s *channelMessageOutboxService) EnqueueWxWorkKFMessage(conversation *models.Conversation, message *models.Message) error {
	if conversation == nil || message == nil {
		return nil
	}
	channel := ChannelService.Get(conversation.ChannelID)
	if channel == nil || channel.ChannelType != enums.ChannelTypeWxWorkKF {
		return nil
	}
	if message.SenderType != enums.IMSenderTypeAgent && message.SenderType != enums.IMSenderTypeAI {
		return nil
	}
	if message.MessageType != enums.IMMessageTypeText && message.MessageType != enums.IMMessageTypeHTML {
		return nil
	}
	if existing := s.GetByMessageID(enums.ChannelTypeWxWorkKF, message.ID); existing != nil {
		return nil
	}

	payload, err := json.Marshal(map[string]any{
		"conversationId": conversation.ID,
		"messageId":      message.ID,
		"messageType":    message.MessageType,
		"content":        strings.TrimSpace(message.Content),
		"payload":        strings.TrimSpace(message.Payload),
		"senderId":       message.SenderID,
	})
	if err != nil {
		return err
	}

	now := time.Now()
	return s.Create(&models.ChannelMessageOutbox{
		ChannelType:    enums.ChannelTypeWxWorkKF,
		ConversationID: conversation.ID,
		MessageID:      message.ID,
		Payload:        string(payload),
		SendStatus:     string(enums.ChannelMessageOutboxStatusPending),
		AuditFields: models.AuditFields{
			CreatedAt:      now,
			CreateUserID:   message.UpdateUserID,
			CreateUserName: message.UpdateUserName,
			UpdatedAt:      now,
			UpdateUserID:   message.UpdateUserID,
			UpdateUserName: message.UpdateUserName,
		},
	})
}

func (s *channelMessageOutboxService) EnqueueTelegramMessage(conversation *models.Conversation, message *models.Message) error {
	if conversation == nil || message == nil {
		return nil
	}
	channel := ChannelService.Get(conversation.ChannelID)
	if channel == nil || channel.ChannelType != enums.ChannelTypeTelegram {
		return nil
	}
	if message.SenderType != enums.IMSenderTypeAgent && message.SenderType != enums.IMSenderTypeAI {
		return nil
	}
	if message.MessageType != enums.IMMessageTypeText && message.MessageType != enums.IMMessageTypeHTML {
		return nil
	}
	if existing := s.GetByMessageID(enums.ChannelTypeTelegram, message.ID); existing != nil {
		return nil
	}

	payload, err := json.Marshal(map[string]any{
		"conversationId": conversation.ID,
		"messageId":      message.ID,
		"messageType":    message.MessageType,
		"content":        strings.TrimSpace(message.Content),
		"payload":        strings.TrimSpace(message.Payload),
		"senderId":       message.SenderID,
	})
	if err != nil {
		return err
	}

	now := time.Now()
	err = s.Create(&models.ChannelMessageOutbox{
		ChannelType:    enums.ChannelTypeTelegram,
		ConversationID: conversation.ID,
		MessageID:      message.ID,
		Payload:        string(payload),
		SendStatus:     string(enums.ChannelMessageOutboxStatusPending),
		AuditFields: models.AuditFields{
			CreatedAt:      now,
			CreateUserID:   message.UpdateUserID,
			CreateUserName: message.UpdateUserName,
			UpdatedAt:      now,
			UpdateUserID:   message.UpdateUserID,
			UpdateUserName: message.UpdateUserName,
		},
	})
	if err != nil {
		return err
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered from panic in telegram outbound dispatch", "error", r)
			}
		}()
		TelegramOutboundService.DispatchPendingOutbox()
	}()

	return nil
}

func (s *channelMessageOutboxService) EnqueueZaloOAMessage(conversation *models.Conversation, message *models.Message) error {
	if conversation == nil || message == nil {
		return nil
	}
	channel := ChannelService.Get(conversation.ChannelID)
	if channel == nil || channel.ChannelType != enums.ChannelTypeZaloOA {
		return nil
	}
	if message.SenderType != enums.IMSenderTypeAgent && message.SenderType != enums.IMSenderTypeAI {
		return nil
	}
	if message.MessageType != enums.IMMessageTypeText && message.MessageType != enums.IMMessageTypeHTML {
		return nil
	}
	if existing := s.GetByMessageID(enums.ChannelTypeZaloOA, message.ID); existing != nil {
		return nil
	}

	payload, err := json.Marshal(map[string]any{
		"conversationId": conversation.ID,
		"messageId":      message.ID,
		"messageType":    message.MessageType,
		"content":        strings.TrimSpace(message.Content),
		"payload":        strings.TrimSpace(message.Payload),
		"senderId":       message.SenderID,
	})
	if err != nil {
		return err
	}

	now := time.Now()
	err = s.Create(&models.ChannelMessageOutbox{
		ChannelType:    enums.ChannelTypeZaloOA,
		ConversationID: conversation.ID,
		MessageID:      message.ID,
		Payload:        string(payload),
		SendStatus:     string(enums.ChannelMessageOutboxStatusPending),
		AuditFields: models.AuditFields{
			CreatedAt:      now,
			CreateUserID:   message.UpdateUserID,
			CreateUserName: message.UpdateUserName,
			UpdatedAt:      now,
			UpdateUserID:   message.UpdateUserID,
			UpdateUserName: message.UpdateUserName,
		},
	})
	if err != nil {
		return err
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered from panic in zalo oa outbound dispatch", "error", r)
			}
		}()
		ZaloOAOutboundService.DispatchPendingOutbox()
	}()

	return nil
}

func (s *channelMessageOutboxService) EnqueueSlackMessage(conversation *models.Conversation, message *models.Message) error {
	if conversation == nil || message == nil {
		return nil
	}
	channel := ChannelService.Get(conversation.ChannelID)
	if channel == nil || channel.ChannelType != enums.ChannelTypeSlack {
		return nil
	}
	if message.SenderType != enums.IMSenderTypeAgent && message.SenderType != enums.IMSenderTypeAI {
		return nil
	}
	if message.MessageType != enums.IMMessageTypeText && message.MessageType != enums.IMMessageTypeHTML && message.MessageType != enums.IMMessageTypeImage && message.MessageType != enums.IMMessageTypeAttachment {
		return nil
	}
	if existing := s.GetByMessageID(enums.ChannelTypeSlack, message.ID); existing != nil {
		return nil
	}

	payload, err := json.Marshal(map[string]any{
		"conversationId": conversation.ID,
		"messageId":      message.ID,
		"messageType":    message.MessageType,
		"content":        strings.TrimSpace(message.Content),
		"payload":        strings.TrimSpace(message.Payload),
		"senderId":       message.SenderID,
	})
	if err != nil {
		return err
	}

	now := time.Now()
	err = s.Create(&models.ChannelMessageOutbox{
		ChannelType:    enums.ChannelTypeSlack,
		ConversationID: conversation.ID,
		MessageID:      message.ID,
		Payload:        string(payload),
		SendStatus:     string(enums.ChannelMessageOutboxStatusPending),
		AuditFields: models.AuditFields{
			CreatedAt:      now,
			CreateUserID:   message.UpdateUserID,
			CreateUserName: message.UpdateUserName,
			UpdatedAt:      now,
			UpdateUserID:   message.UpdateUserID,
			UpdateUserName: message.UpdateUserName,
		},
	})
	if err != nil {
		return err
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered from panic in slack outbound dispatch", "error", r)
			}
		}()
		SlackOutboundService.DispatchPendingOutbox()
	}()

	return nil
}

func (s *channelMessageOutboxService) EnqueueEmailMessage(conversation *models.Conversation, message *models.Message) error {
	if conversation == nil || message == nil {
		return nil
	}
	channel := ChannelService.Get(conversation.ChannelID)
	if channel == nil || channel.ChannelType != enums.ChannelTypeEmail {
		return nil
	}
	if message.SenderType != enums.IMSenderTypeAgent && message.SenderType != enums.IMSenderTypeAI {
		return nil
	}
	if message.MessageType != enums.IMMessageTypeText && message.MessageType != enums.IMMessageTypeHTML {
		return nil
	}
	if existing := s.GetByMessageID(enums.ChannelTypeEmail, message.ID); existing != nil {
		return nil
	}

	payload, err := json.Marshal(map[string]any{
		"conversationId": conversation.ID,
		"messageId":      message.ID,
		"messageType":    message.MessageType,
		"content":        strings.TrimSpace(message.Content),
		"payload":        strings.TrimSpace(message.Payload),
		"senderId":       message.SenderID,
	})
	if err != nil {
		return err
	}

	now := time.Now()
	err = s.Create(&models.ChannelMessageOutbox{
		ChannelType:    enums.ChannelTypeEmail,
		ConversationID: conversation.ID,
		MessageID:      message.ID,
		Payload:        string(payload),
		SendStatus:     string(enums.ChannelMessageOutboxStatusPending),
		AuditFields: models.AuditFields{
			CreatedAt:      now,
			CreateUserID:   message.UpdateUserID,
			CreateUserName: message.UpdateUserName,
			UpdatedAt:      now,
			UpdateUserID:   message.UpdateUserID,
			UpdateUserName: message.UpdateUserName,
		},
	})
	if err != nil {
		return err
	}

	// Trigger async dispatch immediately
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered from panic in email outbound dispatch", "error", r)
			}
		}()
		EmailOutboundService.DispatchPendingOutbox()
	}()

	return nil
}

func (s *channelMessageOutboxService) EnqueueDiscordMessage(conversation *models.Conversation, message *models.Message) error {
	if conversation == nil || message == nil {
		return nil
	}
	channel := ChannelService.Get(conversation.ChannelID)
	if channel == nil || channel.ChannelType != enums.ChannelTypeDiscord {
		return nil
	}
	if message.SenderType != enums.IMSenderTypeAgent && message.SenderType != enums.IMSenderTypeAI {
		return nil
	}
	if message.MessageType != enums.IMMessageTypeText && message.MessageType != enums.IMMessageTypeHTML && message.MessageType != enums.IMMessageTypeImage && message.MessageType != enums.IMMessageTypeAttachment {
		return nil
	}
	if existing := s.GetByMessageID(enums.ChannelTypeDiscord, message.ID); existing != nil {
		return nil
	}

	payload, err := json.Marshal(map[string]any{
		"conversationId": conversation.ID,
		"messageId":      message.ID,
		"messageType":    message.MessageType,
		"content":        strings.TrimSpace(message.Content),
		"payload":        strings.TrimSpace(message.Payload),
		"senderId":       message.SenderID,
	})
	if err != nil {
		return err
	}

	now := time.Now()
	err = s.Create(&models.ChannelMessageOutbox{
		ChannelType:    enums.ChannelTypeDiscord,
		ConversationID: conversation.ID,
		MessageID:      message.ID,
		Payload:        string(payload),
		SendStatus:     string(enums.ChannelMessageOutboxStatusPending),
		AuditFields: models.AuditFields{
			CreatedAt:      now,
			CreateUserID:   message.UpdateUserID,
			CreateUserName: message.UpdateUserName,
			UpdatedAt:      now,
			UpdateUserID:   message.UpdateUserID,
			UpdateUserName: message.UpdateUserName,
		},
	})
	if err != nil {
		return err
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered from panic in discord outbound dispatch", "error", r)
			}
		}()
		DiscordOutboundService.DispatchPendingOutbox()
	}()

	return nil
}

func (s *channelMessageOutboxService) EnqueueLarkMessage(conversation *models.Conversation, message *models.Message) error {
	if conversation == nil || message == nil {
		return nil
	}
	channel := ChannelService.Get(conversation.ChannelID)
	if channel == nil || channel.ChannelType != enums.ChannelTypeLark {
		return nil
	}
	if message.SenderType != enums.IMSenderTypeAgent && message.SenderType != enums.IMSenderTypeAI {
		return nil
	}
	if message.MessageType != enums.IMMessageTypeText && message.MessageType != enums.IMMessageTypeHTML && message.MessageType != enums.IMMessageTypeImage && message.MessageType != enums.IMMessageTypeAttachment {
		return nil
	}
	if existing := s.GetByMessageID(enums.ChannelTypeLark, message.ID); existing != nil {
		return nil
	}

	payload, err := json.Marshal(map[string]any{
		"conversationId": conversation.ID,
		"messageId":      message.ID,
		"messageType":    message.MessageType,
		"content":        strings.TrimSpace(message.Content),
		"payload":        strings.TrimSpace(message.Payload),
		"senderId":       message.SenderID,
	})
	if err != nil {
		return err
	}

	now := time.Now()
	err = s.Create(&models.ChannelMessageOutbox{
		ChannelType:    enums.ChannelTypeLark,
		ConversationID: conversation.ID,
		MessageID:      message.ID,
		Payload:        string(payload),
		SendStatus:     string(enums.ChannelMessageOutboxStatusPending),
		AuditFields: models.AuditFields{
			CreatedAt:      now,
			CreateUserID:   message.UpdateUserID,
			CreateUserName: message.UpdateUserName,
			UpdatedAt:      now,
			UpdateUserID:   message.UpdateUserID,
			UpdateUserName: message.UpdateUserName,
		},
	})
	if err != nil {
		return err
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered from panic in lark outbound dispatch", "error", r)
			}
		}()
		LarkOutboundService.DispatchPendingOutbox()
	}()

	return nil
}

func (s *channelMessageOutboxService) ListPending(channelType string, limit int) []models.ChannelMessageOutbox {
	channelType = strings.TrimSpace(channelType)
	now := time.Now()
	recovered, err := repositories.ChannelMessageOutboxRepository.ResolveStaleSendingClaims(
		sqls.DB(), channelType, now.Add(-channelOutboxDispatchClaimLease), now, staleOutboxClaimManualReviewText)
	if err != nil {
		slog.Warn("failed to resolve stale channel outbox claims", "channel_type", channelType, "error", err)
	} else if recovered > 0 {
		slog.Warn("resolved stale channel outbox claims for manual inspection",
			"channel_type", channelType,
			"count", recovered,
			"claim_lease", channelOutboxDispatchClaimLease,
		)
	}
	return repositories.ChannelMessageOutboxRepository.ListPending(sqls.DB(), channelType, limit, now)
}

func (s *channelMessageOutboxService) RetryWxWorkFailure(id int64, operator *dto.AuthPrincipal) error {
	item := s.Get(id)
	if item == nil || item.ChannelType != enums.ChannelTypeWxWorkKF {
		return errorsx.InvalidParam("outbox record does not exist")
	}
	if item.SendStatus != string(enums.ChannelMessageOutboxStatusFailed) &&
		item.SendStatus != string(enums.ChannelMessageOutboxStatusIgnored) {
		return errorsx.InvalidParam("only failed or ignored outbox records can be retried")
	}
	now := time.Now()
	columns := map[string]interface{}{
		"send_status":   string(enums.ChannelMessageOutboxStatusPending),
		"next_retry_at": nil,
		"updated_at":    now,
	}
	if operator != nil {
		columns["update_user_id"] = operator.UserID
		columns["update_user_name"] = operator.Username
	}
	updated, err := repositories.ChannelMessageOutboxRepository.UpdatesIfStatus(
		sqls.DB(), id, string(enums.ChannelTypeWxWorkKF),
		[]string{string(enums.ChannelMessageOutboxStatusFailed), string(enums.ChannelMessageOutboxStatusIgnored)}, columns)
	if err != nil {
		return err
	}
	if !updated {
		return errorsx.InvalidParam("only failed or ignored outbox records can be retried")
	}
	return nil
}

func (s *channelMessageOutboxService) IgnoreWxWorkFailure(id int64, operator *dto.AuthPrincipal) error {
	item := s.Get(id)
	if item == nil || item.ChannelType != enums.ChannelTypeWxWorkKF {
		return errorsx.InvalidParam("outbox record does not exist")
	}
	if item.SendStatus != string(enums.ChannelMessageOutboxStatusFailed) {
		return errorsx.InvalidParam("only failed outbox records can be ignored")
	}
	now := time.Now()
	columns := map[string]interface{}{
		"send_status":   string(enums.ChannelMessageOutboxStatusIgnored),
		"next_retry_at": nil,
		"updated_at":    now,
	}
	if operator != nil {
		columns["update_user_id"] = operator.UserID
		columns["update_user_name"] = operator.Username
	}
	updated, err := repositories.ChannelMessageOutboxRepository.UpdatesIfStatus(
		sqls.DB(), id, string(enums.ChannelTypeWxWorkKF),
		[]string{string(enums.ChannelMessageOutboxStatusFailed)}, columns)
	if err != nil {
		return err
	}
	if !updated {
		return errorsx.InvalidParam("only failed outbox records can be ignored")
	}
	return nil
}
