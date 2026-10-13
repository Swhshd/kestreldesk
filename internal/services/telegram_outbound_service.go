package services

// This file has been modified for the KestrelDesk project.

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/Swhshd/kestreldesk/internal/telegram"

	"github.com/mlogclub/simple/sqls"
)

const (
	telegramOutboxBatchSize = 20
)

var TelegramOutboundService = newTelegramOutboundService()

func newTelegramOutboundService() *telegramOutboundService {
	return &telegramOutboundService{}
}

type telegramOutboundService struct{}

func (s *telegramOutboundService) DispatchPendingOutbox() int {
	return s.doDispatchPendingOutbox(telegramOutboxBatchSize)
}

func (s *telegramOutboundService) doDispatchPendingOutbox(limit int) int {
	if limit <= 0 {
		limit = telegramOutboxBatchSize
	}
	items := ChannelMessageOutboxService.ListPending(enums.ChannelTypeTelegram, limit)
	if len(items) == 0 {
		return 0
	}

	successCount := 0
	for i := range items {
		if err := s.processOutbox(items[i].ID); err != nil {
			slog.Warn("process telegram outbox failed",
				"outbox_id", items[i].ID,
				"error", err,
			)
			continue
		}
		successCount++
	}
	return successCount
}

func (s *telegramOutboundService) processOutbox(outboxID int64) error {
	outbox := ChannelMessageOutboxService.Get(outboxID)
	if outbox == nil {
		return nil
	}
	if outbox.ChannelType != enums.ChannelTypeTelegram {
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
		return handleRequiredOutboundLookupFailure(outbox, "Telegram message", "message not found", lookupErr, s.markOutboxFailed)
	}
	if message == nil {
		return s.markOutboxFailed(outbox, "message not found")
	}
	conversation, lookupErr := repositories.ConversationRepository.GetForOutboundDispatch(sqls.DB(), outbox.ConversationID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "Telegram conversation", "conversation not found", lookupErr, s.markOutboxFailed)
	}
	if conversation == nil {
		return s.markOutboxFailed(outbox, "conversation not found")
	}

	channel, lookupErr := repositories.ChannelRepository.GetForOutboundDispatch(sqls.DB(), conversation.ChannelID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "Telegram channel", "telegram channel not found or disabled", lookupErr, s.markOutboxFailed)
	}
	if channel == nil || channel.Status != enums.StatusOk {
		return s.markOutboxFailed(outbox, "telegram channel not found or disabled")
	}
	cfg, err := ChannelService.ParseTelegramChannelConfig(channel.ConfigJSON)
	if err != nil || cfg == nil || cfg.BotToken == "" {
		return s.markOutboxFailed(outbox, "telegram bot token not configured")
	}

	// Resolve target Telegram ChatID
	var chatID int64
	customerIdentity, lookupErr := repositories.CustomerIdentityRepository.FindOneForOutboundDispatch(sqls.DB(), sqls.NewCnd().
		Eq("customer_id", conversation.CustomerID).
		Eq("external_source", enums.ExternalSourceTelegram))
	if handled, err := handleOptionalOutboundLookupFailure(outbox, "Telegram customer identity", lookupErr, s.markOutboxFailed); handled || err != nil {
		return err
	}
	if customerIdentity != nil {
		if id, err := strconv.ParseInt(customerIdentity.ExternalID, 10, 64); err == nil {
			chatID = id
		}
	}
	if chatID == 0 {
		return s.markOutboxFailed(outbox, "unable to resolve telegram chat_id")
	}

	// Send message via Telegram Client
	client := telegram.NewClient(cfg.BotToken)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, sendErr := client.SendMessage(ctx, telegram.SendMessageRequest{
		ChatID: chatID,
		Text:   message.Content,
	})

	if sendErr != nil {
		return ChannelMessageOutboxService.MarkAmbiguousDeliveryFailure(outbox, sendErr.Error())
	}

	return ChannelMessageOutboxService.UpdateClaimed(outbox, map[string]any{
		"send_status": string(enums.ChannelMessageOutboxStatusSent),
		"sent_at":     time.Now(),
		"updated_at":  time.Now(),
	})
}

func (s *telegramOutboundService) markOutboxFailed(outbox *models.ChannelMessageOutbox, errMsg string) error {
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
