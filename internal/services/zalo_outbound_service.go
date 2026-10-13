package services

// This file has been modified for the KestrelDesk project.

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/Swhshd/kestreldesk/internal/zalo"

	"github.com/mlogclub/simple/sqls"
)

const (
	zaloOAOutboxBatchSize = 20
)

var ZaloOAOutboundService = newZaloOAOutboundService()

func newZaloOAOutboundService() *zaloOAOutboundService {
	return &zaloOAOutboundService{}
}

type zaloOAOutboundService struct{}

func (s *zaloOAOutboundService) DispatchPendingOutbox() int {
	return s.doDispatchPendingOutbox(zaloOAOutboxBatchSize)
}

func (s *zaloOAOutboundService) doDispatchPendingOutbox(limit int) int {
	if limit <= 0 {
		limit = zaloOAOutboxBatchSize
	}
	items := ChannelMessageOutboxService.ListPending(enums.ChannelTypeZaloOA, limit)
	if len(items) == 0 {
		return 0
	}

	successCount := 0
	for i := range items {
		if err := s.processOutbox(items[i].ID); err != nil {
			slog.Warn("process zalo oa outbox failed",
				"outbox_id", items[i].ID,
				"error", err,
			)
			continue
		}
		successCount++
	}
	return successCount
}

func (s *zaloOAOutboundService) processOutbox(outboxID int64) error {
	outbox := ChannelMessageOutboxService.Get(outboxID)
	if outbox == nil {
		return nil
	}
	if outbox.ChannelType != enums.ChannelTypeZaloOA {
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
		return handleRequiredOutboundLookupFailure(outbox, "Zalo message", "message not found", lookupErr, s.markOutboxFailed)
	}
	if message == nil {
		return s.markOutboxFailed(outbox, "message not found")
	}
	conversation, lookupErr := repositories.ConversationRepository.GetForOutboundDispatch(sqls.DB(), outbox.ConversationID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "Zalo conversation", "conversation not found", lookupErr, s.markOutboxFailed)
	}
	if conversation == nil {
		return s.markOutboxFailed(outbox, "conversation not found")
	}

	channel, lookupErr := repositories.ChannelRepository.GetForOutboundDispatch(sqls.DB(), conversation.ChannelID)
	if lookupErr != nil {
		return handleRequiredOutboundLookupFailure(outbox, "Zalo channel", "zalo oa channel not found or disabled", lookupErr, s.markOutboxFailed)
	}
	if channel == nil || channel.Status != enums.StatusOk {
		return s.markOutboxFailed(outbox, "zalo oa channel not found or disabled")
	}
	cfg, err := ChannelService.ParseZaloOAChannelConfig(channel.ConfigJSON)
	if err != nil || cfg == nil || cfg.AccessToken == "" {
		return s.markOutboxFailed(outbox, "zalo oa access token not configured")
	}

	// Resolve target Zalo User ID
	var zaloUserID string
	customerIdentity, lookupErr := repositories.CustomerIdentityRepository.FindOneForOutboundDispatch(sqls.DB(), sqls.NewCnd().
		Eq("customer_id", conversation.CustomerID).
		Eq("external_source", enums.ExternalSourceZaloOA))
	if handled, err := handleOptionalOutboundLookupFailure(outbox, "Zalo customer identity", lookupErr, s.markOutboxFailed); handled || err != nil {
		return err
	}
	if customerIdentity != nil {
		zaloUserID = strings.TrimSpace(customerIdentity.ExternalID)
	}
	if zaloUserID == "" {
		return s.markOutboxFailed(outbox, "unable to resolve zalo user_id")
	}

	// Send message via Zalo Client
	client := zalo.NewClient(cfg.AccessToken)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, sendErr := client.SendCSMessage(ctx, zaloUserID, message.Content)
	if sendErr != nil {
		return ChannelMessageOutboxService.MarkAmbiguousDeliveryFailure(outbox, sendErr.Error())
	}

	return ChannelMessageOutboxService.UpdateClaimed(outbox, map[string]any{
		"send_status": string(enums.ChannelMessageOutboxStatusSent),
		"sent_at":     time.Now(),
		"updated_at":  time.Now(),
	})
}

func (s *zaloOAOutboundService) markOutboxFailed(outbox *models.ChannelMessageOutbox, errMsg string) error {
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
