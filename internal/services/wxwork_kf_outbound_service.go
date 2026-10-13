package services

// This file has been modified for the KestrelDesk project.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/i18nx"
	"github.com/Swhshd/kestreldesk/internal/pkg/utils"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/Swhshd/kestreldesk/internal/wxwork"

	"github.com/mlogclub/simple/common/strs"
	"github.com/mlogclub/simple/sqls"
	"github.com/silenceper/wechat/v2/work/kf/sendmsg"
	"gorm.io/gorm"
)

const (
	wxWorkKFOutboxBatchSize = 20
)

var WxWorkKFOutboundService = newWxWorkKFOutboundService()

func newWxWorkKFOutboundService() *wxWorkKFOutboundService {
	return &wxWorkKFOutboundService{}
}

type wxWorkKFOutboundService struct {
}

type wxWorkKFOutboundChunk struct {
	MessageType enums.IMMessageType
	Content     string
	AssetID     string
	Asset       *models.Asset
}

func (s *wxWorkKFOutboundService) DispatchPendingOutbox() int {
	if !wxwork.Enabled() {
		return 0
	}

	var totalCount int = 0
	for {
		count := s.doDispatchPendingOutbox(wxWorkKFOutboxBatchSize)

		totalCount += count
		if count > 0 {
			slog.Info("wxwork kf outbound dispatch loop",
				"batch_count", count,
				"total_count", totalCount,
			)
		}

		if count == 0 {
			break
		}
	}
	return totalCount
}

func (s *wxWorkKFOutboundService) doDispatchPendingOutbox(limit int) int {
	if !wxwork.Enabled() {
		return 0
	}
	if limit <= 0 {
		limit = wxWorkKFOutboxBatchSize
	}

	items := ChannelMessageOutboxService.ListPending(enums.ChannelTypeWxWorkKF, limit)
	if len(items) == 0 {
		return 0
	}

	successCount := 0
	for i := range items {
		if err := s.processOutbox(items[i].ID); err != nil {
			slog.Warn("process wxwork kf outbox failed",
				"outbox_id", items[i].ID,
				"conversation_id", items[i].ConversationID,
				"message_id", items[i].MessageID,
				"error", err,
			)
			continue
		}
		successCount++
	}
	return successCount
}

func (s *wxWorkKFOutboundService) processOutbox(outboxID int64) error {
	outbox := ChannelMessageOutboxService.Get(outboxID)
	if outbox == nil {
		return nil
	}
	if outbox.ChannelType != enums.ChannelTypeWxWorkKF {
		return nil
	}
	if outbox.SendStatus == string(enums.ChannelMessageOutboxStatusSent) {
		return nil
	}
	if outbox.NextRetryAt != nil && outbox.NextRetryAt.After(time.Now()) {
		return nil
	}

	slog.Info("processing wxwork kf outbox",
		"outbox_id", outbox.ID,
		"conversation_id", outbox.ConversationID,
		"message_id", outbox.MessageID,
		"send_status", outbox.SendStatus,
		"retry_count", outbox.RetryCount,
	)

	now := time.Now()
	claimed, err := ChannelMessageOutboxService.ClaimForSending(outbox, now)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}

	message, lookupErr := repositories.MessageRepository.GetForOutboundDispatch(sqls.DB(), outbox.MessageID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "WxWork message", "平台消息不存在", lookupErr, s.markOutboxFailed)
	}
	if message == nil {
		return s.markOutboxFailed(outbox, "平台消息不存在")
	}
	conversation, lookupErr := repositories.ConversationRepository.GetForOutboundDispatch(sqls.DB(), outbox.ConversationID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "WxWork conversation", "平台会话不存在", lookupErr, s.markOutboxFailed)
	}
	if conversation == nil {
		return s.markOutboxFailed(outbox, "平台会话不存在")
	}
	mapping, lookupErr := repositories.WxWorkKFConversationRepository.TakeForOutboundDispatch(sqls.DB(), "conversation_id = ?", conversation.ID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "WxWork conversation mapping", "企业微信会话映射不存在", lookupErr, s.markOutboxFailed)
	}
	if mapping == nil {
		return s.markOutboxFailed(outbox, "企业微信会话映射不存在")
	}
	if mapping.ChannelID <= 0 {
		return s.markOutboxFailed(outbox, "企业微信会话映射缺少渠道ID")
	}
	channel, lookupErr := repositories.ChannelRepository.GetForOutboundDispatch(sqls.DB(), mapping.ChannelID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "WxWork channel", "企业微信接入渠道不存在、未启用或类型不匹配", lookupErr, s.markOutboxFailed)
	}
	if channel == nil || channel.Status != enums.StatusOk || channel.ChannelType != enums.ChannelTypeWxWorkKF {
		return s.markOutboxFailed(outbox, "企业微信接入渠道不存在、未启用或类型不匹配")
	}
	if strings.TrimSpace(mapping.OpenKfID) == "" || strings.TrimSpace(mapping.ExternalUserID) == "" {
		return s.markOutboxFailed(outbox, "企业微信会话映射缺少发送必要参数")
	}
	chunks, buildErr := s.buildOutboundChunks(message)
	if buildErr != nil {
		return s.markOutboxFailed(outbox, buildErr.Error())
	}
	if len(chunks) == 0 {
		return s.markOutboxFailed(outbox, "当前消息无法转换为企业微信下行消息")
	}
	if handled, err := s.prepareOutboundChunkAssets(outbox, chunks); handled || err != nil {
		return err
	}

	slog.Info("built wxwork outbound chunks",
		"outbox_id", outbox.ID,
		"conversation_id", conversation.ID,
		"message_id", message.ID,
		"sender_type", message.SenderType,
		"message_type", message.MessageType,
		"chunk_count", len(chunks),
		"open_kfid", mapping.OpenKfID,
		"external_userid", mapping.ExternalUserID,
	)

	wxMsgIDs := make([]string, 0, len(chunks))
	for i := range chunks {
		wxMsgID, sendErr := s.sendOutboundChunk(outbox, mapping, message, chunks[i], i)
		if sendErr != nil {
			if handled, failureErr := s.handleDispatchClaimRenewalFailure(outbox, sendErr, len(wxMsgIDs)); handled {
				return failureErr
			}
			var attachmentReadErr *wxWorkAttachmentReadError
			if errors.As(sendErr, &attachmentReadErr) {
				return s.handleAttachmentReadFailure(outbox, attachmentReadErr.err)
			}
			return ChannelMessageOutboxService.MarkAmbiguousDeliveryFailure(outbox, sendErr.Error())
		}
		wxMsgIDs = append(wxMsgIDs, wxMsgID)
	}

	return sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		now = time.Now()
		if err := ChannelMessageOutboxService.updateClaimed(ctx.Tx, outbox, map[string]any{
			"send_status":      string(enums.ChannelMessageOutboxStatusSent),
			"sent_at":          now,
			"last_error":       "",
			"updated_at":       now,
			"update_user_id":   outbox.UpdateUserID,
			"update_user_name": outbox.UpdateUserName,
		}); err != nil {
			return err
		}
		if err := s.createOutboundAttemptRefs(ctx.Tx, outbox, conversation, message, mapping, chunks, wxMsgIDs, now); err != nil {
			return err
		}
		return ConversationEventLogService.CreateEvent(ctx, conversation.ID, enums.IMEventTypeWxWorkKFOutbound, message.SenderType, message.SenderID, fmt.Sprintf("企业微信消息发送成功，共%d条", len(wxMsgIDs)), "")
	})
}

func (s *wxWorkKFOutboundService) createOutboundAttemptRefs(db *gorm.DB, outbox *models.ChannelMessageOutbox, conversation *models.Conversation, message *models.Message, mapping *models.WxWorkKFConversation, chunks []wxWorkKFOutboundChunk, wxMsgIDs []string, createdAt time.Time) error {
	if db == nil || outbox == nil || conversation == nil || message == nil || mapping == nil {
		return fmt.Errorf("cannot persist WxWork outbound references without dispatch context")
	}
	if len(wxMsgIDs) == 0 {
		return fmt.Errorf("cannot persist WxWork outbound references without provider message IDs")
	}
	for i, wxMsgID := range wxMsgIDs {
		rawPayload := strings.TrimSpace(outbox.Payload)
		if len(chunks) > i {
			if payload, err := json.Marshal(map[string]any{
				"messageId":    message.ID,
				"chunkIndex":   i,
				"chunkType":    chunks[i].MessageType,
				"chunkText":    strings.TrimSpace(chunks[i].Content),
				"chunkAssetId": strings.TrimSpace(chunks[i].AssetID),
			}); err == nil {
				rawPayload = string(payload)
			}
		}
		if err := repositories.WxWorkKFMessageRefRepository.Create(db, &models.WxWorkKFMessageRef{
			ConversationID:     conversation.ID,
			MessageID:          message.ID,
			WxMsgID:            strings.TrimSpace(wxMsgID),
			Direction:          string(enums.WxWorkKFMessageDirectionOut),
			DispatchClaimToken: outbox.DispatchClaimToken,
			Origin:             0,
			OpenKfID:           mapping.OpenKfID,
			ExternalUserID:     mapping.ExternalUserID,
			SendStatus:         string(enums.WxWorkKFMessageSendStatusSent),
			RawPayload:         rawPayload,
			Status:             enums.StatusOk,
			AuditFields: models.AuditFields{
				CreatedAt:      createdAt,
				CreateUserID:   outbox.UpdateUserID,
				CreateUserName: outbox.UpdateUserName,
				UpdatedAt:      createdAt,
				UpdateUserID:   outbox.UpdateUserID,
				UpdateUserName: outbox.UpdateUserName,
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *wxWorkKFOutboundService) sendOutboundChunk(outbox *models.ChannelMessageOutbox, mapping *models.WxWorkKFConversation, message *models.Message, chunk wxWorkKFOutboundChunk, chunkIndex int) (string, error) {
	switch chunk.MessageType {
	case enums.IMMessageTypeText:
		return s.sendTextMessage(outbox, mapping, message, chunk.Content, chunkIndex)
	case enums.IMMessageTypeImage:
		return s.sendImageMessage(outbox, mapping, message, chunk, chunkIndex)
	default:
		return "", i18nx.Errorf("error.wxwork.unsupportedOutboundMessageType", chunk.MessageType)
	}
}

func (s *wxWorkKFOutboundService) renewDispatchClaim(outbox *models.ChannelMessageOutbox, providerRequestAlreadyStarted bool) error {
	// Refresh immediately before every provider request so a multi-chunk send
	// cannot outlive the stale-claim lease while still issuing side effects.
	if outbox == nil {
		return &wxWorkDispatchClaimRenewalError{err: fmt.Errorf("cannot renew an empty WxWork dispatch claim"), providerRequestAlreadyStarted: providerRequestAlreadyStarted}
	}
	updated, err := ChannelMessageOutboxService.TouchDispatchClaim(outbox, time.Now())
	if err != nil {
		return &wxWorkDispatchClaimRenewalError{err: err, providerRequestAlreadyStarted: providerRequestAlreadyStarted}
	}
	if !updated {
		return &wxWorkDispatchClaimRenewalError{
			err:                           fmt.Errorf("WxWork dispatch claim for outbox %d is no longer current", outbox.ID),
			claimLost:                     true,
			providerRequestAlreadyStarted: providerRequestAlreadyStarted,
		}
	}
	return nil
}

func (s *wxWorkKFOutboundService) handleDispatchClaimRenewalFailure(outbox *models.ChannelMessageOutbox, sendErr error, completedChunks int) (bool, error) {
	var renewalErr *wxWorkDispatchClaimRenewalError
	if !errors.As(sendErr, &renewalErr) {
		return false, nil
	}
	if renewalErr.claimLost {
		return true, renewalErr
	}
	if completedChunks == 0 && !renewalErr.providerRequestAlreadyStarted {
		return true, handleRequiredOutboundLookupFailure(
			outbox,
			"WxWork dispatch claim renewal",
			"",
			renewalErr.err,
			s.markOutboxFailed,
		)
	}
	return false, nil
}

func (s *wxWorkKFOutboundService) prepareOutboundChunkAssets(outbox *models.ChannelMessageOutbox, chunks []wxWorkKFOutboundChunk) (bool, error) {
	for i := range chunks {
		if chunks[i].MessageType != enums.IMMessageTypeImage {
			continue
		}
		asset, lookupErr := repositories.AssetRepository.GetByAssetIDForOutboundDispatch(sqls.DB(), strings.TrimSpace(chunks[i].AssetID))
		if lookupErr != nil {
			return true, handleRequiredOutboundLookupFailure(outbox, "WxWork attachment asset", "企业微信消息附件不存在", lookupErr, s.markOutboxFailed)
		}
		chunks[i].Asset = asset
	}
	return false, nil
}

func (s *wxWorkKFOutboundService) sendTextMessage(outbox *models.ChannelMessageOutbox, mapping *models.WxWorkKFConversation, message *models.Message, content string, chunkIndex int) (string, error) {
	cli, err := wxwork.GetWorkCli().GetKF()
	if err != nil {
		return "", err
	}

	req := sendmsg.Text{}
	req.Message.ToUser = strings.TrimSpace(mapping.ExternalUserID)
	req.Message.OpenKFID = strings.TrimSpace(mapping.OpenKfID)
	req.Message.MsgID = s.buildOutboundClientMsgID(outbox.DispatchClaimToken, message.ID, chunkIndex)
	req.MsgType = "text"
	req.Text.Content = strings.TrimSpace(content)

	slog.Info("sending wxwork text message",
		"conversation_id", message.ConversationID,
		"message_id", message.ID,
		"chunk_index", chunkIndex,
		"client_msg_id", req.Message.MsgID,
		"open_kfid", req.Message.OpenKFID,
		"external_userid", req.Message.ToUser,
		"content_length", len([]rune(req.Text.Content)),
	)

	if err := s.renewDispatchClaim(outbox, false); err != nil {
		return "", err
	}
	resp, err := cli.SendMsg(req)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.MsgID) == "" {
		return "", i18nx.Errorf("error.e0114")
	}
	slog.Info("wxwork text message accepted",
		"conversation_id", message.ConversationID,
		"message_id", message.ID,
		"chunk_index", chunkIndex,
		"client_msg_id", req.Message.MsgID,
		"wx_msg_id", strings.TrimSpace(resp.MsgID),
		"open_kfid", req.Message.OpenKFID,
		"external_userid", req.Message.ToUser,
	)
	return strings.TrimSpace(resp.MsgID), nil
}

func (s *wxWorkKFOutboundService) sendImageMessage(outbox *models.ChannelMessageOutbox, mapping *models.WxWorkKFConversation, message *models.Message, chunk wxWorkKFOutboundChunk, chunkIndex int) (string, error) {
	if strings.TrimSpace(chunk.AssetID) == "" {
		return "", i18nx.Errorf("error.e0145")
	}

	asset := chunk.Asset
	if asset == nil {
		return "", i18nx.Errorf("error.e0146")
	}
	fileReader, err := AssetService.OpenReader(asset)
	if err != nil {
		return "", &wxWorkAttachmentReadError{err: err}
	}
	defer func() {
		if fileReader != nil {
			_ = fileReader.Close()
		}
	}()

	slog.Info("sending wxwork image message",
		"conversation_id", message.ConversationID,
		"message_id", message.ID,
		"chunk_index", chunkIndex,
		"asset_id", asset.AssetID,
		"filename", asset.Filename,
		"storage_key", asset.StorageKey,
		"open_kfid", mapping.OpenKfID,
		"external_userid", mapping.ExternalUserID,
	)

	materialCli := wxwork.GetWorkCli().GetMaterial()
	if err := s.renewDispatchClaim(outbox, false); err != nil {
		return "", err
	}
	uploadResp, err := materialCli.UploadTempFileFromReader(asset.Filename, "image", fileReader)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(uploadResp.MediaID) == "" {
		return "", i18nx.Errorf("error.e0113")
	}

	kfCli, err := wxwork.GetWorkCli().GetKF()
	if err != nil {
		return "", err
	}
	req := sendmsg.Image{
		Message: sendmsg.Message{
			ToUser:   strings.TrimSpace(mapping.ExternalUserID),
			OpenKFID: strings.TrimSpace(mapping.OpenKfID),
			MsgID:    s.buildOutboundClientMsgID(outbox.DispatchClaimToken, message.ID, chunkIndex),
		},
		MsgType: "image",
	}
	req.Image.MediaID = strings.TrimSpace(uploadResp.MediaID)

	if err := s.renewDispatchClaim(outbox, true); err != nil {
		return "", err
	}
	resp, err := kfCli.SendMsg(req)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.MsgID) == "" {
		return "", i18nx.Errorf("error.e0114")
	}
	slog.Info("wxwork image message accepted",
		"conversation_id", message.ConversationID,
		"message_id", message.ID,
		"chunk_index", chunkIndex,
		"client_msg_id", req.Message.MsgID,
		"wx_msg_id", strings.TrimSpace(resp.MsgID),
		"media_id", req.Image.MediaID,
		"asset_id", asset.AssetID,
		"open_kfid", req.Message.OpenKFID,
		"external_userid", req.Message.ToUser,
	)
	return strings.TrimSpace(resp.MsgID), nil
}

type wxWorkAttachmentReadError struct {
	err error
}

type wxWorkDispatchClaimRenewalError struct {
	err                           error
	claimLost                     bool
	providerRequestAlreadyStarted bool
}

func (e *wxWorkDispatchClaimRenewalError) Error() string {
	if e == nil || e.err == nil {
		return "WxWork dispatch claim renewal failed"
	}
	return "WxWork dispatch claim renewal failed: " + e.err.Error()
}

func (e *wxWorkDispatchClaimRenewalError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *wxWorkAttachmentReadError) Error() string {
	if e == nil || e.err == nil {
		return "wxwork attachment storage read failed"
	}
	return "wxwork attachment storage read failed: " + e.err.Error()
}

func (e *wxWorkAttachmentReadError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (s *wxWorkKFOutboundService) handleAttachmentReadFailure(outbox *models.ChannelMessageOutbox, err error) error {
	if isRetryableOutboundStorageError(err) {
		return ChannelMessageOutboxService.MarkPreflightLookupFailure(outbox, "WxWork attachment storage")
	}
	return s.markOutboxFailed(outbox, "企业微信消息附件无法从存储读取")
}

func (s *wxWorkKFOutboundService) markOutboxFailed(outbox *models.ChannelMessageOutbox, errMsg string) error {
	if outbox == nil {
		return nil
	}
	slog.Warn("mark wxwork kf outbox failed",
		"outbox_id", outbox.ID,
		"conversation_id", outbox.ConversationID,
		"message_id", outbox.MessageID,
		"retry_count", outbox.RetryCount,
		"error", strings.TrimSpace(errMsg),
	)
	now := time.Now()
	return ChannelMessageOutboxService.UpdateClaimed(outbox, map[string]any{
		"send_status":      string(enums.ChannelMessageOutboxStatusFailed),
		"next_retry_at":    nil,
		"last_error":       strings.TrimSpace(errMsg),
		"updated_at":       now,
		"update_user_id":   outbox.UpdateUserID,
		"update_user_name": outbox.UpdateUserName,
	})
}

func (s *wxWorkKFOutboundService) markOutboxFailedFromCallback(outbox *models.ChannelMessageOutbox, ref *models.WxWorkKFMessageRef, errMsg string) (bool, error) {
	if outbox == nil || ref == nil || ref.MessageID != outbox.MessageID {
		return false, nil
	}
	return repositories.ChannelMessageOutboxRepository.UpdatesIfStatusAndClaim(
		sqls.DB(), outbox.ID, outbox.ChannelType,
		[]string{string(enums.ChannelMessageOutboxStatusSent)}, ref.DispatchClaimToken,
		map[string]any{
			"send_status":          string(enums.ChannelMessageOutboxStatusFailed),
			"dispatch_claim_token": "",
			"next_retry_at":        nil,
			"last_error":           strings.TrimSpace(errMsg),
			"updated_at":           time.Now(),
			"update_user_id":       outbox.UpdateUserID,
			"update_user_name":     outbox.UpdateUserName,
		},
	)
}

func (s *wxWorkKFOutboundService) buildOutboundClientMsgID(claimToken string, messageID int64, chunkIndex int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", strings.TrimSpace(claimToken), messageID, chunkIndex)))
	return hex.EncodeToString(digest[:16])
}

type wxWorkKFOutboundPayload struct {
	ConversationID int64               `json:"conversationId"`
	MessageID      int64               `json:"messageId"`
	MessageType    enums.IMMessageType `json:"messageType"`
	Content        string              `json:"content"`
	Payload        string              `json:"payload"`
	SenderID       int64               `json:"senderId"`
}

func (s *wxWorkKFOutboundService) parseOutboxPayload(raw string) (*wxWorkKFOutboundPayload, error) {
	payload := &wxWorkKFOutboundPayload{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (s *wxWorkKFOutboundService) buildOutboundChunks(message *models.Message) ([]wxWorkKFOutboundChunk, error) {
	if message == nil {
		return nil, i18nx.Errorf("error.e0186")
	}
	switch message.MessageType {
	case enums.IMMessageTypeText:
		content := strings.TrimSpace(message.Content)
		if content == "" {
			return nil, i18nx.Errorf("error.e0217")
		}
		return []wxWorkKFOutboundChunk{{MessageType: enums.IMMessageTypeText, Content: content}}, nil
	case enums.IMMessageTypeHTML:
		return s.buildHTMLChunks(message.Content)
	default:
		return nil, i18nx.Errorf("error.wxwork.currentUnsupportedOutboundMessageType", message.MessageType)
	}
}

func (s *wxWorkKFOutboundService) buildHTMLChunks(content string) ([]wxWorkKFOutboundChunk, error) {
	contentChunks, err := utils.SplitHTMLContentChunks(content)
	if err != nil {
		return nil, err
	}
	chunks := make([]wxWorkKFOutboundChunk, 0, len(contentChunks))
	for _, chunk := range contentChunks {
		switch chunk.Type {
		case utils.ContentChunkTypeText:
			if strs.IsNotBlank(chunk.Content) {
				chunks = append(chunks, wxWorkKFOutboundChunk{
					MessageType: enums.IMMessageTypeText,
					Content:     chunk.Content,
				})
			}
		case utils.ContentChunkTypeImage:
			if strs.IsBlank(chunk.AssetID) {
				chunks = append(chunks, wxWorkKFOutboundChunk{
					MessageType: enums.IMMessageTypeText,
					Content:     "[图片]",
				})
			} else {
				chunks = append(chunks, wxWorkKFOutboundChunk{
					MessageType: enums.IMMessageTypeImage,
					AssetID:     chunk.AssetID,
				})
			}
		}
	}
	if len(chunks) == 0 {
		return nil, i18nx.Errorf("error.e0029")
	}
	return chunks, nil
}
