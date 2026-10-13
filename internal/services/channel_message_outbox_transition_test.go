package services

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/glebarez/sqlite"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestWxWorkFailureActionsDoNotOverwriteConcurrentDispatch(t *testing.T) {
	for _, action := range []struct {
		name string
		call func(*channelMessageOutboxService, int64) error
	}{
		{
			name: "retry",
			call: func(service *channelMessageOutboxService, id int64) error {
				return service.RetryWxWorkFailure(id, &dto.AuthPrincipal{UserID: 7, Username: "synthetic-operator"})
			},
		},
		{
			name: "ignore",
			call: func(service *channelMessageOutboxService, id int64) error {
				return service.IgnoreWxWorkFailure(id, &dto.AuthPrincipal{UserID: 7, Username: "synthetic-operator"})
			},
		},
	} {
		t.Run(action.name, func(t *testing.T) {
			db := setupChannelOutboxTransitionTestDB(t)
			item := &models.ChannelMessageOutbox{
				ChannelType: string(enums.ChannelTypeWxWorkKF),
				SendStatus:  string(enums.ChannelMessageOutboxStatusFailed),
			}
			if err := db.Create(item).Error; err != nil {
				t.Fatal(err)
			}

			var claimWon atomic.Bool
			callbackName := "test:wxwork_transition_claim_" + action.name
			if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement.Table != "t_channel_message_outbox" || claimWon.Swap(true) {
					return
				}
				_, err := tx.Statement.ConnPool.ExecContext(tx.Statement.Context,
					"UPDATE t_channel_message_outbox SET send_status = ? WHERE id = ?",
					string(enums.ChannelMessageOutboxStatusSending), item.ID)
				if err != nil {
					tx.AddError(err)
				}
			}); err != nil {
				t.Fatalf("install deterministic concurrent dispatch: %v", err)
			}

			if err := action.call(newChannelMessageOutboxService(), item.ID); err == nil {
				t.Fatal("manual action succeeded after a concurrent dispatcher claimed the row")
			}
			if !claimWon.Load() {
				t.Fatal("test did not reach the deterministic claim boundary")
			}

			var got models.ChannelMessageOutbox
			if err := db.First(&got, item.ID).Error; err != nil {
				t.Fatal(err)
			}
			if got.SendStatus != string(enums.ChannelMessageOutboxStatusSending) {
				t.Fatalf("manual action overwrote concurrent state: send_status=%q, want sending", got.SendStatus)
			}
		})
	}
}

func TestPermanentOutboundPreflightFailuresAreNotRetried(t *testing.T) {
	tests := []struct {
		name        string
		channelType string
		markFailed  func(*models.ChannelMessageOutbox) error
	}{
		{"discord", string(enums.ChannelTypeDiscord), func(outbox *models.ChannelMessageOutbox) error {
			return newDiscordOutboundService().markOutboxFailed(outbox, "synthetic permanent preflight failure")
		}},
		{"lark", string(enums.ChannelTypeLark), func(outbox *models.ChannelMessageOutbox) error {
			return newLarkOutboundService().markOutboxFailed(outbox, "synthetic permanent preflight failure")
		}},
		{"slack", string(enums.ChannelTypeSlack), func(outbox *models.ChannelMessageOutbox) error {
			return newSlackOutboundService().markOutboxFailed(outbox, "synthetic permanent preflight failure")
		}},
		{"telegram", string(enums.ChannelTypeTelegram), func(outbox *models.ChannelMessageOutbox) error {
			return newTelegramOutboundService().markOutboxFailed(outbox, "synthetic permanent preflight failure")
		}},
		{"zalo", string(enums.ChannelTypeZaloOA), func(outbox *models.ChannelMessageOutbox) error {
			return newZaloOAOutboundService().markOutboxFailed(outbox, "synthetic permanent preflight failure")
		}},
		{"wxwork", string(enums.ChannelTypeWxWorkKF), func(outbox *models.ChannelMessageOutbox) error {
			return newWxWorkKFOutboundService().markOutboxFailed(outbox, "synthetic permanent preflight failure")
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := setupChannelOutboxTransitionTestDB(t)
			outbox := &models.ChannelMessageOutbox{
				ChannelType: tc.channelType,
				SendStatus:  string(enums.ChannelMessageOutboxStatusPending),
				RetryCount:  2,
			}
			if err := db.Create(outbox).Error; err != nil {
				t.Fatalf("create synthetic outbox row: %v", err)
			}
			if claimed, err := ChannelMessageOutboxService.ClaimForSending(outbox, time.Now()); err != nil || !claimed {
				t.Fatalf("claim synthetic outbox row: claimed=%t err=%v", claimed, err)
			}
			if err := tc.markFailed(outbox); err != nil {
				t.Fatalf("mark permanent preflight failure: %v", err)
			}

			var got models.ChannelMessageOutbox
			if err := db.First(&got, outbox.ID).Error; err != nil {
				t.Fatalf("reload synthetic outbox row: %v", err)
			}
			if got.SendStatus != string(enums.ChannelMessageOutboxStatusFailed) || got.RetryCount != 2 || got.NextRetryAt != nil {
				t.Fatalf("permanent preflight state = (status %q, retry_count %d, next_retry_at %v), want (failed, 2, nil)", got.SendStatus, got.RetryCount, got.NextRetryAt)
			}
			if pending := ChannelMessageOutboxService.ListPending(tc.channelType, 10); len(pending) != 0 {
				t.Fatalf("permanent preflight failure remains eligible for retry: %#v (checked at %s)", pending, time.Now().Format(time.RFC3339))
			}
		})
	}
}

func TestPermanentOutboundDatabasePreflightFailuresAreNotRetried(t *testing.T) {
	tests := []struct {
		name        string
		channelType string
		process     func(int64) error
	}{
		{"discord", string(enums.ChannelTypeDiscord), newDiscordOutboundService().processOutbox},
		{"email", string(enums.ChannelTypeEmail), newEmailOutboundService().processOutbox},
		{"lark", string(enums.ChannelTypeLark), newLarkOutboundService().processOutbox},
		{"slack", string(enums.ChannelTypeSlack), newSlackOutboundService().processOutbox},
		{"telegram", string(enums.ChannelTypeTelegram), newTelegramOutboundService().processOutbox},
		{"zalo", string(enums.ChannelTypeZaloOA), newZaloOAOutboundService().processOutbox},
		{"wxwork", string(enums.ChannelTypeWxWorkKF), newWxWorkKFOutboundService().processOutbox},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := setupChannelOutboxTransitionTestDB(t)
			outbox := &models.ChannelMessageOutbox{
				ChannelType: tc.channelType, MessageID: 2001, ConversationID: 3001,
				SendStatus: string(enums.ChannelMessageOutboxStatusPending),
			}
			if err := db.Create(outbox).Error; err != nil {
				t.Fatalf("create synthetic outbox row: %v", err)
			}
			// This test database intentionally omits t_message so that the first
			// dispatch lookup gets a deterministic permanent schema error.
			if err := tc.process(outbox.ID); err != nil {
				t.Fatalf("process synthetic outbox row: %v", err)
			}

			var got models.ChannelMessageOutbox
			if err := db.First(&got, outbox.ID).Error; err != nil {
				t.Fatalf("reload synthetic outbox row: %v", err)
			}
			if got.SendStatus != string(enums.ChannelMessageOutboxStatusFailed) || got.RetryCount != 0 || got.NextRetryAt != nil {
				t.Fatalf("permanent database preflight state = (status %q, retry_count %d, next_retry_at %v), want (failed, 0, nil)", got.SendStatus, got.RetryCount, got.NextRetryAt)
			}
		})
	}
}

func TestTransientOutboundDatabasePreflightFailuresRemainRetryable(t *testing.T) {
	tests := []struct {
		name        string
		channelType string
		process     func(int64) error
	}{
		{"discord", string(enums.ChannelTypeDiscord), newDiscordOutboundService().processOutbox},
		{"email", string(enums.ChannelTypeEmail), newEmailOutboundService().processOutbox},
		{"lark", string(enums.ChannelTypeLark), newLarkOutboundService().processOutbox},
		{"slack", string(enums.ChannelTypeSlack), newSlackOutboundService().processOutbox},
		{"telegram", string(enums.ChannelTypeTelegram), newTelegramOutboundService().processOutbox},
		{"zalo", string(enums.ChannelTypeZaloOA), newZaloOAOutboundService().processOutbox},
		{"wxwork", string(enums.ChannelTypeWxWorkKF), newWxWorkKFOutboundService().processOutbox},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := setupChannelOutboxTransitionTestDB(t)
			if err := db.AutoMigrate(&models.Message{}); err != nil {
				t.Fatalf("migrate synthetic message table: %v", err)
			}
			message := &models.Message{ID: 2001, ConversationID: 3001}
			if err := db.Create(message).Error; err != nil {
				t.Fatalf("create synthetic message: %v", err)
			}
			outbox := &models.ChannelMessageOutbox{
				ChannelType: tc.channelType, MessageID: message.ID, ConversationID: message.ConversationID,
				SendStatus: string(enums.ChannelMessageOutboxStatusPending),
			}
			if err := db.Create(outbox).Error; err != nil {
				t.Fatalf("create synthetic outbox row: %v", err)
			}

			var injected atomic.Bool
			callbackName := "test:inject_transient_message_lookup_" + tc.name
			if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement.Table == "t_message" && injected.CompareAndSwap(false, true) {
					tx.AddError(&drivermysql.MySQLError{Number: 2013, Message: "synthetic lost connection"})
				}
			}); err != nil {
				t.Fatalf("install synthetic transient database failure: %v", err)
			}

			if err := tc.process(outbox.ID); err != nil {
				t.Fatalf("process synthetic outbox row: %v", err)
			}
			if !injected.Load() {
				t.Fatal("test did not inject a transient message lookup failure")
			}

			var got models.ChannelMessageOutbox
			if err := db.First(&got, outbox.ID).Error; err != nil {
				t.Fatalf("reload synthetic outbox row: %v", err)
			}
			if got.SendStatus != string(enums.ChannelMessageOutboxStatusPending) || got.RetryCount != 1 || got.NextRetryAt == nil || !got.NextRetryAt.After(time.Now()) {
				t.Fatalf("transient database preflight state = (status %q, retry_count %d, next_retry_at %v), want (pending, 1, future)", got.SendStatus, got.RetryCount, got.NextRetryAt)
			}
		})
	}
}

func TestOutboundAttachmentDatabaseFailuresAreSurfacedBeforeProviderCall(t *testing.T) {
	db := setupChannelOutboxTransitionTestDB(t)
	if err := db.AutoMigrate(&models.Asset{}, &models.Message{}); err != nil {
		t.Fatalf("migrate synthetic attachment tables: %v", err)
	}
	asset := &models.Asset{AssetID: "asset_outbound_synthetic", Provider: enums.AssetProviderLocal, StorageKey: "synthetic.png"}
	if err := db.Create(asset).Error; err != nil {
		t.Fatalf("create synthetic attachment: %v", err)
	}
	message := &models.Message{
		ID: 2001, ConversationID: 3001, MessageType: enums.IMMessageTypeImage,
		Payload: `{"assetId":"asset_outbound_synthetic"}`,
	}
	if err := db.Create(message).Error; err != nil {
		t.Fatalf("create synthetic image message: %v", err)
	}

	var injected atomic.Bool
	if err := db.Callback().Query().After("gorm:query").Register("test:inject_outbound_asset_lookup_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "t_asset" && injected.CompareAndSwap(false, true) {
			tx.AddError(&drivermysql.MySQLError{Number: 2013, Message: "synthetic lost connection"})
		}
	}); err != nil {
		t.Fatalf("install synthetic attachment lookup failure: %v", err)
	}

	_, err := outboundMessageAssetPayload(message)
	if !injected.Load() {
		t.Fatal("test did not inject the attachment database failure")
	}
	if !isRetryableOutboundDatabaseError(err) {
		t.Fatalf("attachment lookup error = %v, want surfaced retryable database error", err)
	}
}

func TestWxWorkAttachmentLookupFailureSchedulesRetryBeforeProviderCall(t *testing.T) {
	db := setupChannelOutboxTransitionTestDB(t)
	if err := db.AutoMigrate(&models.Asset{}); err != nil {
		t.Fatalf("migrate synthetic attachment table: %v", err)
	}
	asset := &models.Asset{AssetID: "asset_wxwork_synthetic", Provider: enums.AssetProviderLocal, StorageKey: "synthetic.png"}
	if err := db.Create(asset).Error; err != nil {
		t.Fatalf("create synthetic attachment: %v", err)
	}
	outbox := &models.ChannelMessageOutbox{
		ChannelType: string(enums.ChannelTypeWxWorkKF), MessageID: 2002, ConversationID: 3002,
		SendStatus: string(enums.ChannelMessageOutboxStatusPending),
	}
	if err := db.Create(outbox).Error; err != nil {
		t.Fatalf("create synthetic outbox row: %v", err)
	}
	if claimed, err := ChannelMessageOutboxService.ClaimForSending(outbox, time.Now()); err != nil || !claimed {
		t.Fatalf("claim synthetic outbox row: claimed=%t err=%v", claimed, err)
	}

	var injected atomic.Bool
	if err := db.Callback().Query().After("gorm:query").Register("test:inject_wxwork_asset_lookup_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "t_asset" && injected.CompareAndSwap(false, true) {
			tx.AddError(&drivermysql.MySQLError{Number: 2013, Message: "synthetic lost connection"})
		}
	}); err != nil {
		t.Fatalf("install synthetic attachment lookup failure: %v", err)
	}

	chunks := []wxWorkKFOutboundChunk{{MessageType: enums.IMMessageTypeImage, AssetID: asset.AssetID}}
	handled, err := newWxWorkKFOutboundService().prepareOutboundChunkAssets(outbox, chunks)
	if err != nil {
		t.Fatalf("handle synthetic attachment lookup failure: %v", err)
	}
	if !handled || !injected.Load() {
		t.Fatalf("attachment lookup handled=%t injected=%t, want handled and injected", handled, injected.Load())
	}
	var got models.ChannelMessageOutbox
	if err := db.First(&got, outbox.ID).Error; err != nil {
		t.Fatalf("reload synthetic outbox row: %v", err)
	}
	if got.SendStatus != string(enums.ChannelMessageOutboxStatusPending) || got.RetryCount != 1 || got.NextRetryAt == nil || !got.NextRetryAt.After(time.Now()) {
		t.Fatalf("transient attachment lookup state = (status %q, retry_count %d, next_retry_at %v), want pending, 1, future", got.SendStatus, got.RetryCount, got.NextRetryAt)
	}
}

func TestOutboundDatabaseLookupRetryClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "connection dropped", err: &drivermysql.MySQLError{Number: 2013}, want: true},
		{name: "server gone away", err: &drivermysql.MySQLError{Number: 2006}, want: true},
		{name: "deadlock", err: &drivermysql.MySQLError{Number: 1213}, want: true},
		{name: "lock timeout", err: &drivermysql.MySQLError{Number: 1205}, want: true},
		{name: "missing row", err: gorm.ErrRecordNotFound, want: false},
		{name: "schema error", err: &drivermysql.MySQLError{Number: 1146}, want: false},
		{name: "syntax error", err: &drivermysql.MySQLError{Number: 1064}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryableOutboundDatabaseError(tc.err); got != tc.want {
				t.Fatalf("isRetryableOutboundDatabaseError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestWxWorkAttachmentStorageFailuresRetryOnlyWhenKnownTransient(t *testing.T) {
	for _, tc := range []struct {
		name      string
		readErr   error
		wantRetry bool
	}{
		{name: "read deadline", readErr: context.DeadlineExceeded, wantRetry: true},
		{name: "OSS throttled", readErr: &oss.ServiceError{StatusCode: 429}, wantRetry: true},
		{name: "OSS unavailable", readErr: &oss.ServiceError{StatusCode: 503}, wantRetry: true},
		{name: "OSS forbidden", readErr: &oss.ServiceError{StatusCode: 403}, wantRetry: false},
		{name: "missing local file", readErr: &os.PathError{Op: "open", Path: "synthetic", Err: os.ErrNotExist}, wantRetry: false},
		{name: "canceled", readErr: context.Canceled, wantRetry: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupChannelOutboxTransitionTestDB(t)
			outbox := &models.ChannelMessageOutbox{
				ChannelType: string(enums.ChannelTypeWxWorkKF), SendStatus: string(enums.ChannelMessageOutboxStatusPending), RetryCount: 2,
			}
			if err := db.Create(outbox).Error; err != nil {
				t.Fatalf("create synthetic outbox row: %v", err)
			}
			if claimed, err := ChannelMessageOutboxService.ClaimForSending(outbox, time.Now()); err != nil || !claimed {
				t.Fatalf("claim synthetic outbox row: claimed=%t err=%v", claimed, err)
			}
			if got := isRetryableOutboundStorageError(tc.readErr); got != tc.wantRetry {
				t.Fatalf("storage retry classification = %t, want %t", got, tc.wantRetry)
			}
			if err := newWxWorkKFOutboundService().handleAttachmentReadFailure(outbox, tc.readErr); err != nil {
				t.Fatalf("handle synthetic storage read failure: %v", err)
			}
			var got models.ChannelMessageOutbox
			if err := db.First(&got, outbox.ID).Error; err != nil {
				t.Fatalf("reload synthetic outbox row: %v", err)
			}
			if tc.wantRetry {
				if got.SendStatus != string(enums.ChannelMessageOutboxStatusPending) || got.RetryCount != 3 || got.NextRetryAt == nil || !got.NextRetryAt.After(time.Now()) {
					t.Fatalf("transient storage failure state = (status %q, retry_count %d, next_retry_at %v), want pending, 3, future", got.SendStatus, got.RetryCount, got.NextRetryAt)
				}
			} else if got.SendStatus != string(enums.ChannelMessageOutboxStatusFailed) || got.RetryCount != 2 || got.NextRetryAt != nil {
				t.Fatalf("permanent storage failure state = (status %q, retry_count %d, next_retry_at %v), want failed, 2, nil", got.SendStatus, got.RetryCount, got.NextRetryAt)
			}
		})
	}
}

func TestOutboundClaimResponseLossDoesNotSilentlyStrandPreProviderWork(t *testing.T) {
	db := setupChannelOutboxTransitionTestDB(t)
	outbox := &models.ChannelMessageOutbox{
		ChannelType: string(enums.ChannelTypeEmail), MessageID: 2010, ConversationID: 3010,
		SendStatus: string(enums.ChannelMessageOutboxStatusPending),
	}
	if err := db.Create(outbox).Error; err != nil {
		t.Fatalf("create synthetic pending outbox row: %v", err)
	}
	var injected atomic.Bool
	if err := db.Callback().Update().After("gorm:update").Register("test:lose_outbox_claim_response", func(tx *gorm.DB) {
		if tx.Statement.Table == "t_channel_message_outbox" && injected.CompareAndSwap(false, true) {
			tx.AddError(&drivermysql.MySQLError{Number: 2013, Message: "synthetic lost connection after claim"})
		}
	}); err != nil {
		t.Fatalf("install synthetic claim response loss: %v", err)
	}

	err := newEmailOutboundService().processOutbox(outbox.ID)
	if err == nil || !injected.Load() {
		t.Fatalf("claim response loss result = %v, injected=%t; want injected claim failure", err, injected.Load())
	}
	var got models.ChannelMessageOutbox
	if err := db.First(&got, outbox.ID).Error; err != nil {
		t.Fatalf("reload synthetic outbox row: %v", err)
	}
	if got.SendStatus != string(enums.ChannelMessageOutboxStatusPending) && got.SendStatus != string(enums.ChannelMessageOutboxStatusFailed) {
		t.Fatalf("pre-provider claim response loss left status %q; want recoverable pending/failed state", got.SendStatus)
	}
}

func TestListPendingMarksStaleSendingClaimsForManualInspection(t *testing.T) {
	db := setupChannelOutboxTransitionTestDB(t)
	now := time.Now()
	channelType := string(enums.ChannelTypeEmail)
	staleRetryAt := now.Add(time.Hour)

	stale := &models.ChannelMessageOutbox{
		ChannelType:        channelType,
		MessageID:          2011,
		SendStatus:         string(enums.ChannelMessageOutboxStatusSending),
		DispatchClaimToken: "synthetic-stale-claim",
		NextRetryAt:        &staleRetryAt,
		LastError:          "previous error",
		AuditFields:        models.AuditFields{UpdatedAt: now.Add(-20 * time.Minute)},
	}
	fresh := &models.ChannelMessageOutbox{
		ChannelType:        channelType,
		MessageID:          2012,
		SendStatus:         string(enums.ChannelMessageOutboxStatusSending),
		DispatchClaimToken: "synthetic-fresh-claim",
		AuditFields:        models.AuditFields{UpdatedAt: now.Add(-2 * time.Minute)},
	}
	pendingOutbox := &models.ChannelMessageOutbox{
		ChannelType: channelType,
		MessageID:   2013,
		SendStatus:  string(enums.ChannelMessageOutboxStatusPending),
	}
	for _, item := range []*models.ChannelMessageOutbox{stale, fresh, pendingOutbox} {
		if err := db.Create(item).Error; err != nil {
			t.Fatalf("create synthetic outbox row: %v", err)
		}
	}
	for _, claim := range []struct {
		item      *models.ChannelMessageOutbox
		updatedAt time.Time
	}{{stale, now.Add(-20 * time.Minute)}, {fresh, now.Add(-2 * time.Minute)}} {
		if err := db.Model(&models.ChannelMessageOutbox{}).Where("id = ?", claim.item.ID).UpdateColumn("updated_at", claim.updatedAt).Error; err != nil {
			t.Fatalf("set synthetic claim age: %v", err)
		}
	}

	pending := ChannelMessageOutboxService.ListPending(channelType, 10)
	if len(pending) != 1 || pending[0].ID != pendingOutbox.ID {
		t.Fatalf("ListPending() = %#v, want only pending outbox %d", pending, pendingOutbox.ID)
	}

	var gotStale models.ChannelMessageOutbox
	if err := db.First(&gotStale, stale.ID).Error; err != nil {
		t.Fatalf("reload stale outbox row: %v", err)
	}
	if gotStale.SendStatus != string(enums.ChannelMessageOutboxStatusFailed) {
		t.Fatalf("stale send_status = %q, want failed for manual inspection", gotStale.SendStatus)
	}
	if gotStale.NextRetryAt != nil {
		t.Fatalf("stale next_retry_at = %v, want nil", gotStale.NextRetryAt)
	}
	if gotStale.DispatchClaimToken != "" {
		t.Fatalf("stale dispatch claim token = %q, want cleared", gotStale.DispatchClaimToken)
	}
	if gotStale.LastError != "delivery outcome unknown after stale sending claim; check provider status before manual retry" {
		t.Fatalf("stale last_error = %q, want a fixed manual-inspection reason", gotStale.LastError)
	}

	var gotFresh models.ChannelMessageOutbox
	if err := db.First(&gotFresh, fresh.ID).Error; err != nil {
		t.Fatalf("reload fresh outbox row: %v", err)
	}
	if gotFresh.SendStatus != string(enums.ChannelMessageOutboxStatusSending) || gotFresh.DispatchClaimToken != fresh.DispatchClaimToken {
		t.Fatalf("fresh claim changed: status=%q token=%q, want sending with token %q", gotFresh.SendStatus, gotFresh.DispatchClaimToken, fresh.DispatchClaimToken)
	}
}

func TestWxWorkDispatchHeartbeatRenewsLeaseAndFencesExpiredClaim(t *testing.T) {
	db := setupChannelOutboxTransitionTestDB(t)
	claimedAt := time.Now().Add(-30 * time.Minute)
	outbox := &models.ChannelMessageOutbox{
		ChannelType: string(enums.ChannelTypeWxWorkKF),
		MessageID:   2088,
		SendStatus:  string(enums.ChannelMessageOutboxStatusPending),
	}
	if err := db.Create(outbox).Error; err != nil {
		t.Fatalf("create pending outbox row: %v", err)
	}
	if claimed, err := ChannelMessageOutboxService.ClaimForSending(outbox, claimedAt); err != nil || !claimed {
		t.Fatalf("claim outbox row: claimed=%t err=%v", claimed, err)
	}

	heartbeatAt := time.Now()
	updated, err := ChannelMessageOutboxService.TouchDispatchClaim(outbox, heartbeatAt)
	if err != nil || !updated {
		t.Fatalf("heartbeat active dispatch claim: updated=%t err=%v", updated, err)
	}

	resolved, err := repositories.ChannelMessageOutboxRepository.ResolveStaleSendingClaims(
		db,
		outbox.ChannelType,
		heartbeatAt.Add(-time.Minute),
		heartbeatAt.Add(channelOutboxDispatchClaimLease),
		staleOutboxClaimManualReviewText,
	)
	if err != nil {
		t.Fatalf("resolve stale claims after heartbeat: %v", err)
	}
	if resolved != 0 {
		t.Fatalf("heartbeat-refreshed claim was resolved as stale %d times, want 0", resolved)
	}

	resolved, err = repositories.ChannelMessageOutboxRepository.ResolveStaleSendingClaims(
		db,
		outbox.ChannelType,
		heartbeatAt.Add(time.Second),
		heartbeatAt.Add(channelOutboxDispatchClaimLease+time.Second),
		staleOutboxClaimManualReviewText,
	)
	if err != nil {
		t.Fatalf("resolve truly expired claim: %v", err)
	}
	if resolved != 1 {
		t.Fatalf("expired claim resolution count = %d, want 1", resolved)
	}

	updated, err = ChannelMessageOutboxService.TouchDispatchClaim(outbox, heartbeatAt.Add(channelOutboxDispatchClaimLease+2*time.Second))
	if err != nil {
		t.Fatalf("heartbeat expired claim: %v", err)
	}
	if updated {
		t.Fatal("expired dispatcher renewed its claim after stale recovery")
	}
	if err := WxWorkKFOutboundService.renewDispatchClaim(outbox, false); err == nil {
		t.Fatal("WxWork dispatch would continue to a provider request without its claim")
	}

	var got models.ChannelMessageOutbox
	if err := db.First(&got, outbox.ID).Error; err != nil {
		t.Fatalf("reload recovered outbox row: %v", err)
	}
	if got.SendStatus != string(enums.ChannelMessageOutboxStatusFailed) || got.DispatchClaimToken != "" {
		t.Fatalf("expired claim changed after rejected heartbeat: status=%q token=%q", got.SendStatus, got.DispatchClaimToken)
	}
}

func TestWxWorkClaimRenewalFailureRetriesOnlyBeforeProviderSideEffects(t *testing.T) {
	t.Run("transient database failure before first provider request uses bounded preflight retry", func(t *testing.T) {
		db := setupChannelOutboxTransitionTestDB(t)
		outbox := &models.ChannelMessageOutbox{
			ChannelType: string(enums.ChannelTypeWxWorkKF),
			MessageID:   2089,
			SendStatus:  string(enums.ChannelMessageOutboxStatusPending),
		}
		if err := db.Create(outbox).Error; err != nil {
			t.Fatalf("create pending outbox row: %v", err)
		}
		if claimed, err := ChannelMessageOutboxService.ClaimForSending(outbox, time.Now()); err != nil || !claimed {
			t.Fatalf("claim outbox row: claimed=%t err=%v", claimed, err)
		}

		failure := &wxWorkDispatchClaimRenewalError{err: context.DeadlineExceeded}
		handled, err := WxWorkKFOutboundService.handleDispatchClaimRenewalFailure(outbox, failure, 0)
		if !handled || err != nil {
			t.Fatalf("handle pre-provider transient renewal error: handled=%t err=%v", handled, err)
		}
		var got models.ChannelMessageOutbox
		if err := db.First(&got, outbox.ID).Error; err != nil {
			t.Fatalf("reload preflight retry row: %v", err)
		}
		if got.SendStatus != string(enums.ChannelMessageOutboxStatusPending) || got.RetryCount != 1 || got.DispatchClaimToken != "" || got.NextRetryAt == nil {
			t.Fatalf("preflight renewal failure state = status:%q retry:%d token:%q next:%v", got.SendStatus, got.RetryCount, got.DispatchClaimToken, got.NextRetryAt)
		}
	})

	t.Run("do not automatically retry after a provider request may have escaped", func(t *testing.T) {
		db := setupChannelOutboxTransitionTestDB(t)
		outbox := &models.ChannelMessageOutbox{
			ChannelType: string(enums.ChannelTypeWxWorkKF),
			MessageID:   2090,
			SendStatus:  string(enums.ChannelMessageOutboxStatusPending),
		}
		if err := db.Create(outbox).Error; err != nil {
			t.Fatalf("create pending outbox row: %v", err)
		}
		if claimed, err := ChannelMessageOutboxService.ClaimForSending(outbox, time.Now()); err != nil || !claimed {
			t.Fatalf("claim outbox row: claimed=%t err=%v", claimed, err)
		}

		failure := &wxWorkDispatchClaimRenewalError{err: context.DeadlineExceeded, providerRequestAlreadyStarted: true}
		if handled, err := WxWorkKFOutboundService.handleDispatchClaimRenewalFailure(outbox, failure, 0); handled || err != nil {
			t.Fatalf("post-provider renewal error classification = (handled=%t, err=%v), want ambiguous-delivery path", handled, err)
		}
		if err := ChannelMessageOutboxService.MarkAmbiguousDeliveryFailure(outbox, failure.Error()); err != nil {
			t.Fatalf("mark potentially delivered outbox for manual inspection: %v", err)
		}
		var got models.ChannelMessageOutbox
		if err := db.First(&got, outbox.ID).Error; err != nil {
			t.Fatalf("reload ambiguous outbox row: %v", err)
		}
		if got.SendStatus != string(enums.ChannelMessageOutboxStatusFailed) || got.RetryCount != 1 || got.DispatchClaimToken != "" {
			t.Fatalf("ambiguous delivery state = status:%q retry:%d token:%q", got.SendStatus, got.RetryCount, got.DispatchClaimToken)
		}
	})
}

func TestExpiredDispatchClaimCannotOverwriteManualOrNewClaimState(t *testing.T) {
	const manualReviewReason = "delivery outcome unknown after stale sending claim; check provider status before manual retry"

	t.Run("late completion cannot overwrite expired state", func(t *testing.T) {
		db := setupChannelOutboxTransitionTestDB(t)
		outbox := &models.ChannelMessageOutbox{
			ChannelType: string(enums.ChannelTypeEmail),
			MessageID:   2014,
			SendStatus:  string(enums.ChannelMessageOutboxStatusPending),
		}
		if err := db.Create(outbox).Error; err != nil {
			t.Fatalf("create pending outbox row: %v", err)
		}
		if claimed, err := ChannelMessageOutboxService.ClaimForSending(outbox, time.Now()); err != nil || !claimed {
			t.Fatalf("claim outbox row: claimed=%t err=%v", claimed, err)
		}

		if err := db.Model(&models.ChannelMessageOutbox{}).Where("id = ?", outbox.ID).Updates(map[string]any{
			"send_status":          string(enums.ChannelMessageOutboxStatusFailed),
			"dispatch_claim_token": "",
			"next_retry_at":        nil,
			"last_error":           manualReviewReason,
		}).Error; err != nil {
			t.Fatalf("simulate expired claim transition: %v", err)
		}

		if err := newEmailOutboundService().markOutboxSent(outbox); err == nil {
			t.Fatal("late completion from the expired claim was accepted")
		}
		var got models.ChannelMessageOutbox
		if err := db.First(&got, outbox.ID).Error; err != nil {
			t.Fatalf("reload outbox row: %v", err)
		}
		if got.SendStatus != string(enums.ChannelMessageOutboxStatusFailed) || got.LastError != manualReviewReason || got.DispatchClaimToken != "" {
			t.Fatalf("late completion changed expired state: status=%q last_error=%q token=%q", got.SendStatus, got.LastError, got.DispatchClaimToken)
		}
	})

	t.Run("late failure cannot overwrite a later claim", func(t *testing.T) {
		db := setupChannelOutboxTransitionTestDB(t)
		outboxA := &models.ChannelMessageOutbox{
			ChannelType: string(enums.ChannelTypeWxWorkKF),
			MessageID:   2015,
			SendStatus:  string(enums.ChannelMessageOutboxStatusPending),
		}
		if err := db.Create(outboxA).Error; err != nil {
			t.Fatalf("create pending outbox row: %v", err)
		}
		if claimed, err := ChannelMessageOutboxService.ClaimForSending(outboxA, time.Now()); err != nil || !claimed {
			t.Fatalf("claim outbox as dispatcher A: claimed=%t err=%v", claimed, err)
		}

		if err := db.Model(&models.ChannelMessageOutbox{}).Where("id = ?", outboxA.ID).Updates(map[string]any{
			"send_status":          string(enums.ChannelMessageOutboxStatusFailed),
			"dispatch_claim_token": "",
			"next_retry_at":        nil,
			"last_error":           manualReviewReason,
		}).Error; err != nil {
			t.Fatalf("simulate expired claim transition: %v", err)
		}
		if err := ChannelMessageOutboxService.RetryWxWorkFailure(outboxA.ID, nil); err != nil {
			t.Fatalf("manually retry after inspection: %v", err)
		}
		pending := ChannelMessageOutboxService.ListPending(string(enums.ChannelTypeWxWorkKF), 10)
		if len(pending) != 1 || pending[0].ID != outboxA.ID {
			t.Fatalf("manual retry ListPending() = %#v, want outbox %d", pending, outboxA.ID)
		}
		outboxB := pending[0]
		if claimed, err := ChannelMessageOutboxService.ClaimForSending(&outboxB, time.Now()); err != nil || !claimed {
			t.Fatalf("claim outbox as dispatcher B: claimed=%t err=%v", claimed, err)
		}

		if err := ChannelMessageOutboxService.MarkAmbiguousDeliveryFailure(outboxA, "late dispatcher A failure"); err == nil {
			t.Fatal("late failure from dispatcher A was accepted after dispatcher B claimed the row")
		}
		var got models.ChannelMessageOutbox
		if err := db.First(&got, outboxA.ID).Error; err != nil {
			t.Fatalf("reload outbox row: %v", err)
		}
		if got.SendStatus != string(enums.ChannelMessageOutboxStatusSending) || got.DispatchClaimToken != outboxB.DispatchClaimToken {
			t.Fatalf("dispatcher A overwrote dispatcher B: status=%q token=%q, want sending with token %q", got.SendStatus, got.DispatchClaimToken, outboxB.DispatchClaimToken)
		}
	})

	t.Run("late success cannot overwrite a later sent claim", func(t *testing.T) {
		db := setupChannelOutboxTransitionTestDB(t)
		outboxA := &models.ChannelMessageOutbox{
			ChannelType: string(enums.ChannelTypeWxWorkKF),
			MessageID:   2016,
			SendStatus:  string(enums.ChannelMessageOutboxStatusPending),
		}
		if err := db.Create(outboxA).Error; err != nil {
			t.Fatalf("create pending outbox row: %v", err)
		}
		if claimed, err := ChannelMessageOutboxService.ClaimForSending(outboxA, time.Now()); err != nil || !claimed {
			t.Fatalf("claim outbox as dispatcher A: claimed=%t err=%v", claimed, err)
		}

		if err := db.Model(&models.ChannelMessageOutbox{}).Where("id = ?", outboxA.ID).Updates(map[string]any{
			"send_status":          string(enums.ChannelMessageOutboxStatusFailed),
			"dispatch_claim_token": "",
			"next_retry_at":        nil,
			"last_error":           manualReviewReason,
		}).Error; err != nil {
			t.Fatalf("simulate expired claim transition: %v", err)
		}
		if err := ChannelMessageOutboxService.RetryWxWorkFailure(outboxA.ID, nil); err != nil {
			t.Fatalf("manually retry after inspection: %v", err)
		}
		pending := ChannelMessageOutboxService.ListPending(string(enums.ChannelTypeWxWorkKF), 10)
		if len(pending) != 1 || pending[0].ID != outboxA.ID {
			t.Fatalf("manual retry ListPending() = %#v, want outbox %d", pending, outboxA.ID)
		}
		outboxB := pending[0]
		if claimed, err := ChannelMessageOutboxService.ClaimForSending(&outboxB, time.Now()); err != nil || !claimed {
			t.Fatalf("claim outbox as dispatcher B: claimed=%t err=%v", claimed, err)
		}
		if err := ChannelMessageOutboxService.UpdateClaimed(&outboxB, map[string]any{
			"send_status": string(enums.ChannelMessageOutboxStatusSent),
			"sent_at":     time.Now(),
			"updated_at":  time.Now(),
		}); err != nil {
			t.Fatalf("complete dispatcher B claim: %v", err)
		}

		if err := ChannelMessageOutboxService.UpdateClaimed(outboxA, map[string]any{
			"send_status": string(enums.ChannelMessageOutboxStatusSent),
			"sent_at":     time.Now(),
			"updated_at":  time.Now(),
		}); err == nil {
			t.Fatal("late success from dispatcher A was accepted after dispatcher B completed")
		}
		var got models.ChannelMessageOutbox
		if err := db.First(&got, outboxA.ID).Error; err != nil {
			t.Fatalf("reload outbox row: %v", err)
		}
		if got.SendStatus != string(enums.ChannelMessageOutboxStatusSent) || got.DispatchClaimToken != outboxB.DispatchClaimToken {
			t.Fatalf("dispatcher A overwrote dispatcher B completion: status=%q token=%q, want sent with dispatcher B token %q", got.SendStatus, got.DispatchClaimToken, outboxB.DispatchClaimToken)
		}
	})
}

func setupChannelOutboxTransitionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{TablePrefix: "t_", SingularTable: true},
	})
	if err != nil {
		t.Fatalf("open isolated outbox database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get isolated outbox database: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	previousDB := sqls.DB()
	sqls.SetDB(db)
	t.Cleanup(func() {
		sqls.SetDB(previousDB)
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close isolated outbox database: %v", err)
		}
	})
	if err := db.AutoMigrate(&models.ChannelMessageOutbox{}); err != nil {
		t.Fatalf("migrate isolated outbox table: %v", err)
	}
	return db
}
