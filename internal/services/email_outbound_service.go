package services

// This file has been modified for the KestrelDesk project.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/Swhshd/kestreldesk/internal/email"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/repositories"

	"github.com/mlogclub/simple/sqls"
)

const (
	emailOutboxBatchSize = 20
	emailOutboxMaxRetry  = 5
)

var EmailOutboundService = newEmailOutboundService()

func newEmailOutboundService() *emailOutboundService {
	return &emailOutboundService{}
}

type emailOutboundService struct{}

func (s *emailOutboundService) DispatchPendingOutbox() int {
	return s.doDispatchPendingOutbox(emailOutboxBatchSize)
}

func (s *emailOutboundService) doDispatchPendingOutbox(limit int) int {
	if limit <= 0 {
		limit = emailOutboxBatchSize
	}
	items := ChannelMessageOutboxService.ListPending(enums.ChannelTypeEmail, limit)
	if len(items) == 0 {
		return 0
	}

	successCount := 0
	for i := range items {
		if err := s.processOutbox(items[i].ID); err != nil {
			slog.Warn("process email outbox failed",
				"outbox_id", items[i].ID,
				"error", err,
			)
			continue
		}
		successCount++
	}
	return successCount
}

func (s *emailOutboundService) processOutbox(outboxID int64) error {
	outbox := ChannelMessageOutboxService.Get(outboxID)
	if outbox == nil {
		return nil
	}
	if outbox.ChannelType != enums.ChannelTypeEmail {
		return nil
	}
	if outbox.SendStatus == string(enums.ChannelMessageOutboxStatusSent) {
		return nil
	}
	if outbox.NextRetryAt != nil && outbox.NextRetryAt.After(time.Now()) {
		return nil
	}

	claimed, err := ChannelMessageOutboxService.ClaimForSending(outbox, time.Now())
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}

	message, lookupErr := repositories.MessageRepository.GetForOutboundDispatch(sqls.DB(), outbox.MessageID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "email message", "message not found", lookupErr, s.markOutboxFailed)
	}
	if message == nil {
		return s.markOutboxFailed(outbox, "message not found")
	}
	conversation, lookupErr := repositories.ConversationRepository.GetForOutboundDispatch(sqls.DB(), outbox.ConversationID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "email conversation", "conversation not found", lookupErr, s.markOutboxFailed)
	}
	if conversation == nil {
		return s.markOutboxFailed(outbox, "conversation not found")
	}

	channel, lookupErr := repositories.ChannelRepository.GetForOutboundDispatch(sqls.DB(), conversation.ChannelID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "email channel", "email channel not found or disabled", lookupErr, s.markOutboxFailed)
	}
	if channel == nil || channel.Status != enums.StatusOk {
		return s.markOutboxFailed(outbox, "email channel not found or disabled")
	}
	cfg, err := ChannelService.ParseEmailChannelConfig(channel.ConfigJSON)
	if err != nil || cfg == nil {
		return s.markOutboxFailed(outbox, "email channel config invalid")
	}

	// 1. Resolve recipient email address
	targetEmail := ""
	targetName := ""
	customer, lookupErr := repositories.CustomerRepository.GetForOutboundDispatch(sqls.DB(), conversation.CustomerID)
	if handled, err := handleOptionalOutboundLookupFailure(outbox, "email customer", lookupErr, s.markOutboxFailed); handled || err != nil {
		return err
	}
	if customer != nil {
		targetEmail = strings.TrimSpace(customer.PrimaryEmail)
		targetName = strings.TrimSpace(customer.Name)
	}
	if targetEmail == "" {
		customerIdentity, lookupErr := repositories.CustomerIdentityRepository.FindOneForOutboundDispatch(sqls.DB(), sqls.NewCnd().
			Eq("customer_id", conversation.CustomerID).
			Eq("external_source", enums.ExternalSourceEmail))
		if handled, err := handleOptionalOutboundLookupFailure(outbox, "email customer identity", lookupErr, s.markOutboxFailed); handled || err != nil {
			return err
		}
		if customerIdentity != nil {
			targetEmail = strings.TrimSpace(customerIdentity.ExternalID)
		}
	}
	if targetEmail == "" {
		return s.markOutboxFailed(outbox, "unable to resolve customer email address")
	}

	// 2. Resolve sender config & system fallbacks
	var sysEmail config.EmailConfig
	if c := config.GetCurrent(); c != nil {
		sysEmail = c.Email
	}
	fromEmail := strings.TrimSpace(cfg.EmailAddress)
	if fromEmail == "" {
		fromEmail = strings.TrimSpace(sysEmail.FromAddress)
	}
	if fromEmail == "" {
		fromEmail = "support@example.com"
	}

	fromName := strings.TrimSpace(cfg.SenderName)
	if fromName == "" {
		fromName = strings.TrimSpace(sysEmail.FromName)
	}
	if fromName == "" {
		fromName = "Customer Support"
	}

	provider := email.DeliveryProvider(strings.ToLower(strings.TrimSpace(cfg.Provider)))
	if provider == "" || provider == "default" {
		provider = email.DeliveryProvider(strings.ToLower(strings.TrimSpace(sysEmail.Provider)))
	}
	if provider == "" {
		provider = email.ProviderSMTP
	}

	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = sysEmail.APIKey
	}

	smtpHost := cfg.SMTPHost
	if smtpHost == "" {
		smtpHost = sysEmail.SMTPHost
	}
	smtpPort := cfg.SMTPPort
	if smtpPort <= 0 {
		smtpPort = sysEmail.SMTPPort
	}
	if smtpPort <= 0 {
		smtpPort = 587
	}
	smtpUser := cfg.SMTPUser
	if smtpUser == "" {
		smtpUser = sysEmail.SMTPUser
	}
	smtpPassword := cfg.SMTPPassword
	if smtpPassword == "" {
		smtpPassword = sysEmail.SMTPPassword
	}
	smtpUseTLS := sysEmail.SMTPUseTLS

	client := email.NewClient(email.ClientConfig{
		Provider:     provider,
		APIKey:       apiKey,
		SMTPHost:     smtpHost,
		SMTPPort:     smtpPort,
		SMTPUser:     smtpUser,
		SMTPPassword: smtpPassword,
		SMTPUseTLS:   smtpUseTLS,
	})

	// 3. Resolve threading headers and subject
	lastInboundMessageID := ""
	lastInboundSubject := ""
	recentMessages, lookupErr := repositories.MessageRepository.FindForOutboundDispatch(sqls.DB(), sqls.NewCnd().
		Eq("conversation_id", conversation.ID).
		Eq("sender_type", enums.IMSenderTypeCustomer).
		Desc("id").
		Limit(5))
	if handled, err := handleOptionalOutboundLookupFailure(outbox, "email customer message thread", lookupErr, s.markOutboxFailed); handled || err != nil {
		return err
	}

	for _, rm := range recentMessages {
		if rm.Payload != "" {
			var pMap map[string]any
			if json.Unmarshal([]byte(rm.Payload), &pMap) == nil {
				if msgID, ok := pMap["email_message_id"].(string); ok && msgID != "" && lastInboundMessageID == "" {
					lastInboundMessageID = msgID
				}
				if subj, ok := pMap["email_subject"].(string); ok && subj != "" && lastInboundSubject == "" {
					lastInboundSubject = subj
				}
			}
		}
	}

	subject := fmt.Sprintf("Re: [#%d] Support Inquiry", conversation.ID)
	if lastInboundSubject != "" {
		cleanSubj := strings.TrimPrefix(lastInboundSubject, "Re: ")
		cleanSubj = strings.TrimPrefix(cleanSubj, "re: ")
		subject = fmt.Sprintf("Re: [#%d] %s", conversation.ID, cleanSubj)
	}

	fromDomain := "localhost"
	parts := strings.Split(fromEmail, "@")
	if len(parts) == 2 {
		fromDomain = parts[1]
	}
	outboundMsgID := fmt.Sprintf("<kestreldesk-msg-%d-%d@%s>", conversation.ID, message.ID, fromDomain)

	customHeaders := map[string]string{
		"X-KestrelDesk-Conversation-ID": fmt.Sprintf("%d", conversation.ID),
		"X-KestrelDesk-Message-ID":      fmt.Sprintf("%d", message.ID),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	sendErr := client.SendEmail(ctx, email.SendEmailParams{
		FromEmail:  fromEmail,
		FromName:   fromName,
		ToEmail:    targetEmail,
		ToName:     targetName,
		Subject:    subject,
		BodyText:   message.Content,
		MessageID:  outboundMsgID,
		InReplyTo:  lastInboundMessageID,
		References: lastInboundMessageID,
		Headers:    customHeaders,
	})

	if sendErr != nil {
		return s.handleOutboxSendError(outbox, sendErr)
	}

	return s.markOutboxSent(outbox)
}

func (s *emailOutboundService) handleOutboxSendError(outbox *models.ChannelMessageOutbox, sendErr error) error {
	var smtpErr *textproto.Error
	if errors.As(sendErr, &smtpErr) && smtpErr.Code >= 400 && smtpErr.Code < 500 {
		return s.handleOutboxError(outbox, sendErr.Error())
	}
	var apiErr *email.HTTPStatusError
	if errors.As(sendErr, &apiErr) && apiErr.HTTPStatusCode() == http.StatusTooManyRequests {
		return s.handleOutboxError(outbox, sendErr.Error())
	}
	return s.markOutboxFailed(outbox, sendErr.Error())
}

func (s *emailOutboundService) markOutboxSent(outbox *models.ChannelMessageOutbox) error {
	now := time.Now()
	return ChannelMessageOutboxService.UpdateClaimed(outbox, map[string]any{
		"send_status":   string(enums.ChannelMessageOutboxStatusSent),
		"last_error":    "",
		"sent_at":       &now,
		"updated_at":    now,
		"next_retry_at": nil,
	})
}

func (s *emailOutboundService) markOutboxFailed(outbox *models.ChannelMessageOutbox, reason string) error {
	now := time.Now()
	return ChannelMessageOutboxService.UpdateClaimed(outbox, map[string]any{
		"send_status":   string(enums.ChannelMessageOutboxStatusFailed),
		"last_error":    reason,
		"updated_at":    now,
		"next_retry_at": nil,
	})
}

func (s *emailOutboundService) handleOutboxError(outbox *models.ChannelMessageOutbox, errMsg string) error {
	retryCount := outbox.RetryCount + 1
	now := time.Now()

	if retryCount >= emailOutboxMaxRetry {
		return ChannelMessageOutboxService.UpdateClaimed(outbox, map[string]any{
			"send_status":   string(enums.ChannelMessageOutboxStatusFailed),
			"last_error":    fmt.Sprintf("max retries exceeded: %s", errMsg),
			"retry_count":   retryCount,
			"updated_at":    now,
			"next_retry_at": nil,
		})
	}

	// Exponential backoff
	backoff := time.Duration(1<<retryCount) * 10 * time.Second
	nextRetry := now.Add(backoff)

	return ChannelMessageOutboxService.UpdateClaimed(outbox, map[string]any{
		"send_status":   string(enums.ChannelMessageOutboxStatusPending),
		"last_error":    fmt.Sprintf("retry #%d error: %s", retryCount, errMsg),
		"retry_count":   retryCount,
		"updated_at":    now,
		"next_retry_at": &nextRetry,
	})
}
