package services

// This file has been modified for the KestrelDesk project.

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/Swhshd/kestreldesk/internal/lark"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/Swhshd/kestreldesk/internal/services/storage"

	"github.com/mlogclub/simple/sqls"
)

const (
	larkOutboxBatchSize = 20
)

var LarkOutboundService = newLarkOutboundService()

func newLarkOutboundService() *larkOutboundService {
	return &larkOutboundService{}
}

type larkOutboundService struct{}

func (s *larkOutboundService) DispatchPendingOutbox() int {
	return s.doDispatchPendingOutbox(larkOutboxBatchSize)
}

func (s *larkOutboundService) doDispatchPendingOutbox(limit int) int {
	if limit <= 0 {
		limit = larkOutboxBatchSize
	}
	items := ChannelMessageOutboxService.ListPending(enums.ChannelTypeLark, limit)
	if len(items) == 0 {
		return 0
	}

	successCount := 0
	for i := range items {
		if err := s.processOutbox(items[i].ID); err != nil {
			slog.Warn("process lark outbox failed",
				"outbox_id", items[i].ID,
				"error", err,
			)
			continue
		}
		successCount++
	}
	return successCount
}

func (s *larkOutboundService) processOutbox(outboxID int64) error {
	outbox := ChannelMessageOutboxService.Get(outboxID)
	if outbox == nil {
		return nil
	}
	if outbox.ChannelType != enums.ChannelTypeLark {
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
		return handleRequiredOutboundLookupFailure(outbox, "Lark message", "message not found", lookupErr, s.markOutboxFailed)
	}
	if message == nil {
		return s.markOutboxFailed(outbox, "message not found")
	}
	conversation, lookupErr := repositories.ConversationRepository.GetForOutboundDispatch(sqls.DB(), outbox.ConversationID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "Lark conversation", "conversation not found", lookupErr, s.markOutboxFailed)
	}
	if conversation == nil {
		return s.markOutboxFailed(outbox, "conversation not found")
	}

	channel, lookupErr := repositories.ChannelRepository.GetForOutboundDispatch(sqls.DB(), conversation.ChannelID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "Lark channel", "lark channel not found or disabled", lookupErr, s.markOutboxFailed)
	}
	if channel == nil || channel.Status != enums.StatusOk {
		return s.markOutboxFailed(outbox, "lark channel not found or disabled")
	}
	cfg, err := ChannelService.ParseLarkChannelConfig(channel.ConfigJSON)
	if err != nil || cfg == nil {
		return s.markOutboxFailed(outbox, "invalid lark channel config")
	}
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return s.markOutboxFailed(outbox, "lark app credentials not configured")
	}

	// Replies go back into the chat the customer wrote from; the chat id is
	// captured on the customer's own message payload at inbound time.
	targetChatID := ""
	lastCustomerMsg, lookupErr := repositories.MessageRepository.FindOneForOutboundDispatch(sqls.DB(), sqls.NewCnd().
		Eq("conversation_id", conversation.ID).
		Eq("sender_type", enums.IMSenderTypeCustomer).
		Desc("id"))
	if handled, err := handleOptionalOutboundLookupFailure(outbox, "Lark customer message", lookupErr, s.markOutboxFailed); handled || err != nil {
		return err
	}
	if lastCustomerMsg != nil && lastCustomerMsg.Payload != "" {
		var payloadMap map[string]any
		if err := json.Unmarshal([]byte(lastCustomerMsg.Payload), &payloadMap); err == nil {
			if ch, ok := payloadMap["lark_chat_id"].(string); ok && ch != "" {
				targetChatID = ch
			}
		}
	}

	if targetChatID == "" {
		return s.markOutboxFailed(outbox, "unable to resolve target lark chat")
	}
	assetPayload, lookupErr := outboundMessageAssetPayload(message)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "Lark attachment asset", "attachment asset metadata unavailable", lookupErr, s.markOutboxFailed)
	}

	client := lark.NewClient(cfg.Domain, cfg.AppID, cfg.AppSecret)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	textToSend := message.Content
	if message.MessageType == enums.IMMessageTypeImage || message.MessageType == enums.IMMessageTypeAttachment {
		if assetPayload != nil {
			if assetPayload.Provider != "" && assetPayload.StorageKey != "" {
				if provider, err := storage.NewProvider(assetPayload.Provider); err == nil {
					fileURL := provider.GetSignedURL(assetPayload.StorageKey)
					if fileURL != "" {
						if textToSend != "" {
							textToSend += "\n" + fileURL
						} else {
							textToSend = fileURL
						}
					}
				}
			}
		}
	}

	_, sendErr := client.SendMessageText(ctx, targetChatID, textToSend)
	if sendErr != nil {
		return ChannelMessageOutboxService.MarkAmbiguousDeliveryFailure(outbox, sendErr.Error())
	}

	return ChannelMessageOutboxService.UpdateClaimed(outbox, map[string]any{
		"send_status": string(enums.ChannelMessageOutboxStatusSent),
		"sent_at":     time.Now(),
		"updated_at":  time.Now(),
	})
}

func (s *larkOutboundService) markOutboxFailed(outbox *models.ChannelMessageOutbox, errMsg string) error {
	if outbox == nil {
		return nil
	}
	now := time.Now()
	return ChannelMessageOutboxService.UpdateClaimed(outbox, map[string]any{
		"send_status":   string(enums.ChannelMessageOutboxStatusFailed),
		"next_retry_at": nil,
		"last_error":    errMsg,
		"updated_at":    now,
	})
}
