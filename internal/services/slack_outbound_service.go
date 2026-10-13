package services

// This file has been modified for the KestrelDesk project.

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/Swhshd/kestreldesk/internal/services/storage"
	"github.com/Swhshd/kestreldesk/internal/slack"

	"github.com/mlogclub/simple/sqls"
)

const (
	slackOutboxBatchSize = 20
)

var SlackOutboundService = newSlackOutboundService()

func newSlackOutboundService() *slackOutboundService {
	return &slackOutboundService{}
}

type slackOutboundService struct{}

func (s *slackOutboundService) DispatchPendingOutbox() int {
	return s.doDispatchPendingOutbox(slackOutboxBatchSize)
}

func (s *slackOutboundService) doDispatchPendingOutbox(limit int) int {
	if limit <= 0 {
		limit = slackOutboxBatchSize
	}
	items := ChannelMessageOutboxService.ListPending(enums.ChannelTypeSlack, limit)
	if len(items) == 0 {
		return 0
	}

	successCount := 0
	for i := range items {
		if err := s.processOutbox(items[i].ID); err != nil {
			slog.Warn("process slack outbox failed",
				"outbox_id", items[i].ID,
				"error", err,
			)
			continue
		}
		successCount++
	}
	return successCount
}

func (s *slackOutboundService) processOutbox(outboxID int64) error {
	outbox := ChannelMessageOutboxService.Get(outboxID)
	if outbox == nil {
		return nil
	}
	if outbox.ChannelType != enums.ChannelTypeSlack {
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
		return handleRequiredOutboundLookupFailure(outbox, "Slack message", "message not found", lookupErr, s.markOutboxFailed)
	}
	if message == nil {
		return s.markOutboxFailed(outbox, "message not found")
	}
	conversation, lookupErr := repositories.ConversationRepository.GetForOutboundDispatch(sqls.DB(), outbox.ConversationID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "Slack conversation", "conversation not found", lookupErr, s.markOutboxFailed)
	}
	if conversation == nil {
		return s.markOutboxFailed(outbox, "conversation not found")
	}

	channel, lookupErr := repositories.ChannelRepository.GetForOutboundDispatch(sqls.DB(), conversation.ChannelID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "Slack channel", "slack channel not found or disabled", lookupErr, s.markOutboxFailed)
	}
	if channel == nil || channel.Status != enums.StatusOk {
		return s.markOutboxFailed(outbox, "slack channel not found or disabled")
	}
	cfg, err := ChannelService.ParseSlackChannelConfig(channel.ConfigJSON)
	if err != nil || cfg == nil {
		return s.markOutboxFailed(outbox, "invalid slack channel config")
	}

	// The channel's own bot token wins; the deployment-wide SLACK_BOT_TOKEN is
	// the fallback for a single shared Slack app.
	botToken := config.ResolveSlack(cfg.BotToken, cfg.SigningSecret).BotToken
	if botToken == "" {
		return s.markOutboxFailed(outbox, "slack bot token not configured")
	}

	// Resolve target Slack Channel ID and Thread TS
	var targetChannel string
	var threadTS string

	lastCustomerMsg, lookupErr := repositories.MessageRepository.FindOneForOutboundDispatch(sqls.DB(), sqls.NewCnd().
		Eq("conversation_id", conversation.ID).
		Eq("sender_type", enums.IMSenderTypeCustomer).
		Desc("id"))
	if handled, err := handleOptionalOutboundLookupFailure(outbox, "Slack customer message", lookupErr, s.markOutboxFailed); handled || err != nil {
		return err
	}
	if lastCustomerMsg != nil && lastCustomerMsg.Payload != "" {
		var payloadMap map[string]any
		if err := json.Unmarshal([]byte(lastCustomerMsg.Payload), &payloadMap); err == nil {
			if ch, ok := payloadMap["slack_channel"].(string); ok && ch != "" {
				targetChannel = ch
			}
			if ts, ok := payloadMap["slack_thread_ts"].(string); ok && ts != "" {
				threadTS = ts
			}
		}
	}

	if targetChannel == "" {
		targetChannel = cfg.DefaultChannel
	}
	if targetChannel == "" {
		return s.markOutboxFailed(outbox, "unable to resolve target slack channel")
	}
	assetPayload, lookupErr := outboundMessageAssetPayload(message)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "Slack attachment asset", "attachment asset metadata unavailable", lookupErr, s.markOutboxFailed)
	}

	client := slack.NewClient(botToken)
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

	_, sendErr := client.PostMessage(ctx, targetChannel, textToSend, threadTS)
	if sendErr != nil {
		return ChannelMessageOutboxService.MarkAmbiguousDeliveryFailure(outbox, sendErr.Error())
	}

	return ChannelMessageOutboxService.UpdateClaimed(outbox, map[string]any{
		"send_status": string(enums.ChannelMessageOutboxStatusSent),
		"sent_at":     time.Now(),
		"updated_at":  time.Now(),
	})
}

func (s *slackOutboundService) markOutboxFailed(outbox *models.ChannelMessageOutbox, errMsg string) error {
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
