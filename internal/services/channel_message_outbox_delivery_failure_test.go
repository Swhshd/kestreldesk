package services

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/discord"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
)

func TestDiscordAcceptedRequestWithDroppedResponseIsNotScheduledForRetry(t *testing.T) {
	var acceptedRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read synthetic Discord request: %v", err)
		}
		acceptedRequests.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("drop synthetic Discord response: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()

	client := discord.NewClient("synthetic-token")
	client.SetBaseURL(server.URL + "/api/v10")
	_, sendErr := client.SendMessage(t.Context(), "synthetic-channel", "accepted synthetic message")
	if sendErr == nil {
		t.Fatal("synthetic Discord client unexpectedly received a successful response")
	}
	if acceptedRequests.Load() != 1 {
		t.Fatalf("synthetic Discord accepted request count = %d, want 1", acceptedRequests.Load())
	}

	db := setupChannelOutboxTransitionTestDB(t)
	outbox := &models.ChannelMessageOutbox{
		ChannelType: string(enums.ChannelTypeDiscord),
		SendStatus:  string(enums.ChannelMessageOutboxStatusPending),
	}
	if err := db.Create(outbox).Error; err != nil {
		t.Fatalf("create synthetic Discord outbox record: %v", err)
	}
	if claimed, err := ChannelMessageOutboxService.ClaimForSending(outbox, time.Now()); err != nil || !claimed {
		t.Fatalf("claim synthetic Discord outbox record: claimed=%t err=%v", claimed, err)
	}
	if err := ChannelMessageOutboxService.MarkAmbiguousDeliveryFailure(outbox, sendErr.Error()); err != nil {
		t.Fatalf("record synthetic Discord send failure: %v", err)
	}

	var got models.ChannelMessageOutbox
	if err := db.First(&got, outbox.ID).Error; err != nil {
		t.Fatalf("reload synthetic Discord outbox record: %v", err)
	}
	if got.SendStatus != string(enums.ChannelMessageOutboxStatusFailed) {
		t.Fatalf("send_status = %q, want failed", got.SendStatus)
	}
	if got.NextRetryAt != nil {
		t.Fatalf("ambiguous delivery was scheduled for retry at %v", *got.NextRetryAt)
	}
	if pending := ChannelMessageOutboxService.ListPending(string(enums.ChannelTypeDiscord), 10); len(pending) != 0 {
		t.Fatalf("ambiguous delivery is eligible for automatic retry: %#v", pending)
	}
}
