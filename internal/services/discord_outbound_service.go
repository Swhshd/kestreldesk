package services

// This file has been modified for the KestrelDesk project.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/Swhshd/kestreldesk/internal/discord"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/Swhshd/kestreldesk/internal/services/storage"
	"os"

	"github.com/mlogclub/simple/sqls"
)

const (
	discordOutboxBatchSize = 20
)

var DiscordOutboundService = newDiscordOutboundService()

func newDiscordOutboundService() *discordOutboundService {
	return &discordOutboundService{}
}

type discordOutboundService struct{}

func (s *discordOutboundService) DispatchPendingOutbox() int {
	return s.doDispatchPendingOutbox(discordOutboxBatchSize)
}

func (s *discordOutboundService) doDispatchPendingOutbox(limit int) int {
	if limit <= 0 {
		limit = discordOutboxBatchSize
	}
	items := ChannelMessageOutboxService.ListPending(enums.ChannelTypeDiscord, limit)
	if len(items) == 0 {
		return 0
	}

	successCount := 0
	for i := range items {
		if err := s.processOutbox(items[i].ID); err != nil {
			slog.Warn("process discord outbox failed",
				"outbox_id", items[i].ID,
				"error", err,
			)
			continue
		}
		successCount++
	}
	return successCount
}

func (s *discordOutboundService) processOutbox(outboxID int64) error {
	outbox := ChannelMessageOutboxService.Get(outboxID)
	if outbox == nil {
		return nil
	}
	if outbox.ChannelType != enums.ChannelTypeDiscord {
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
		return handleRequiredOutboundLookupFailure(outbox, "Discord message", "message not found", lookupErr, s.markOutboxFailed)
	}
	if message == nil {
		return s.markOutboxFailed(outbox, "message not found")
	}
	conversation, lookupErr := repositories.ConversationRepository.GetForOutboundDispatch(sqls.DB(), outbox.ConversationID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "Discord conversation", "conversation not found", lookupErr, s.markOutboxFailed)
	}
	if conversation == nil {
		return s.markOutboxFailed(outbox, "conversation not found")
	}

	channel, lookupErr := repositories.ChannelRepository.GetForOutboundDispatch(sqls.DB(), conversation.ChannelID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "Discord channel", "discord channel not found or disabled", lookupErr, s.markOutboxFailed)
	}
	if channel == nil || channel.Status != enums.StatusOk {
		return s.markOutboxFailed(outbox, "discord channel not found or disabled")
	}
	cfg, err := ChannelService.ParseDiscordChannelConfig(channel.ConfigJSON)
	if err != nil {
		return s.markOutboxFailed(outbox, "invalid discord channel config")
	}
	botToken := ""
	if cfg != nil {
		botToken = strings.TrimSpace(cfg.BotToken)
	}
	if botToken == "" {
		botToken = strings.TrimSpace(config.Current().Discord.BotToken)
	}
	if botToken == "" {
		botToken = strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN"))
	}
	if botToken == "" {
		return s.markOutboxFailed(outbox, "discord bot token not configured")
	}

	// Resolve target Discord User ID and/or Channel ID
	var discordUserID string
	customerIdentity, lookupErr := repositories.CustomerIdentityRepository.FindOneForOutboundDispatch(sqls.DB(), sqls.NewCnd().
		Eq("customer_id", conversation.CustomerID).
		Eq("external_source", enums.ExternalSourceDiscord))
	if handled, err := handleOptionalOutboundLookupFailure(outbox, "Discord customer identity", lookupErr, s.markOutboxFailed); handled || err != nil {
		return err
	}
	if customerIdentity != nil {
		discordUserID = strings.TrimSpace(customerIdentity.ExternalID)
	}

	// Check if there is a discord_channel_id in last message payload
	var targetChannelID string
	lastCustomerMsg, lookupErr := repositories.MessageRepository.FindOneForOutboundDispatch(sqls.DB(), sqls.NewCnd().
		Eq("conversation_id", conversation.ID).
		Eq("sender_type", enums.IMSenderTypeCustomer).
		Desc("id"))
	if handled, err := handleOptionalOutboundLookupFailure(outbox, "Discord customer message", lookupErr, s.markOutboxFailed); handled || err != nil {
		return err
	}
	if lastCustomerMsg != nil && lastCustomerMsg.Payload != "" {
		var payloadMap map[string]any
		if err := json.Unmarshal([]byte(lastCustomerMsg.Payload), &payloadMap); err == nil {
			if chID, ok := payloadMap["discord_channel_id"].(string); ok && chID != "" {
				targetChannelID = chID
			}
		}
	}

	assetPayload, lookupErr := outboundMessageAssetPayload(message)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "Discord attachment asset", "attachment asset metadata unavailable", lookupErr, s.markOutboxFailed)
	}

	client := discord.NewClient(botToken)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if targetChannelID == "" {
		if discordUserID == "" {
			return s.markOutboxFailed(outbox, "unable to resolve discord target user or channel")
		}
		dmChannel, err := client.CreateDMChannel(ctx, discordUserID)
		if err != nil {
			return ChannelMessageOutboxService.MarkAmbiguousDeliveryFailure(outbox, "create discord dm channel failed: "+err.Error())
		}
		targetChannelID = dmChannel.ID
	}

	var sendErr error
	if message.MessageType == enums.IMMessageTypeImage {
		var imageURL string
		if assetPayload != nil {
			if assetPayload.Provider != "" && assetPayload.StorageKey != "" {
				if provider, err := storage.NewProvider(assetPayload.Provider); err == nil {
					imageURL = provider.GetSignedURL(assetPayload.StorageKey)
				}
			}
		}
		if imageURL == "" && strings.HasPrefix(strings.TrimSpace(message.Content), "http") {
			imageURL = strings.TrimSpace(message.Content)
		}

		if imageURL != "" {
			embed := discord.Embed{
				Title: "Image Attachment",
				Image: &discord.EmbedMedia{URL: imageURL},
			}
			_, sendErr = client.SendEmbedMessage(ctx, targetChannelID, message.Content, []discord.Embed{embed})
		} else {
			_, sendErr = client.SendMessage(ctx, targetChannelID, message.Content)
		}
	} else if message.MessageType == enums.IMMessageTypeAttachment {
		var fileURL string
		if assetPayload != nil {
			if assetPayload.Provider != "" && assetPayload.StorageKey != "" {
				if provider, err := storage.NewProvider(assetPayload.Provider); err == nil {
					fileURL = provider.GetSignedURL(assetPayload.StorageKey)
				}
			}
		}
		textToSend := message.Content
		if fileURL != "" {
			if textToSend != "" {
				textToSend += "\n" + fileURL
			} else {
				textToSend = fileURL
			}
		}
		_, sendErr = client.SendMessage(ctx, targetChannelID, textToSend)
	} else {
		_, sendErr = client.SendMessage(ctx, targetChannelID, message.Content)
	}

	if sendErr != nil {
		return ChannelMessageOutboxService.MarkAmbiguousDeliveryFailure(outbox, sendErr.Error())
	}

	return ChannelMessageOutboxService.UpdateClaimed(outbox, map[string]any{
		"send_status": string(enums.ChannelMessageOutboxStatusSent),
		"sent_at":     time.Now(),
		"updated_at":  time.Now(),
	})
}

func (s *discordOutboundService) markOutboxFailed(outbox *models.ChannelMessageOutbox, errMsg string) error {
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
