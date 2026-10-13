package services

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

type presenceWrite struct {
	userID int64
	at     time.Time
}

type testEmployeePresenceStore struct {
	calls chan presenceWrite
	count atomic.Int64
	write func(context.Context, int64, time.Time) error
}

func (s *testEmployeePresenceStore) TouchOnlineAt(ctx context.Context, userID int64, at time.Time) error {
	s.count.Add(1)
	s.calls <- presenceWrite{userID: userID, at: at}
	if s.write != nil {
		return s.write(ctx, userID, at)
	}
	return nil
}

func TestEmployeePresenceActivationAndPongRenewalAreThrottled(t *testing.T) {
	base := time.Now()
	clock := &atomic.Int64{}
	clock.Store(base.UnixNano())
	store := &testEmployeePresenceStore{calls: make(chan presenceWrite, 8)}
	coordinator := newEmployeePresenceCoordinatorWithOptions(store, employeePresenceOptions{
		now: func() time.Time { return time.Unix(0, clock.Load()) }, touchInterval: time.Minute,
		writeTimeout: 200 * time.Millisecond, retryBase: 20 * time.Millisecond, retryMax: 40 * time.Millisecond,
	})
	t.Cleanup(coordinator.Close)
	session := newActivePresenceTestSession(101)
	t.Cleanup(func() { session.cancelPresence() })

	coordinator.request(session, true)
	if got := receivePresenceWrite(t, store.calls); got.userID != 101 {
		t.Fatalf("activation touched employee %d, want 101", got.userID)
	}
	coordinator.request(session, false)
	assertNoPresenceWrite(t, store.calls)

	clock.Store(base.Add(time.Minute).UnixNano())
	coordinator.request(session, false)
	if got := receivePresenceWrite(t, store.calls); got.userID != 101 || !got.at.Equal(base.Add(time.Minute)) {
		t.Fatalf("renewal write = %+v", got)
	}
	if store.count.Load() != 2 {
		t.Fatalf("presence writes = %d, want 2", store.count.Load())
	}
}

func TestEmployeePresenceCooldownDoesNotRetainClosedSessionAndSchedulesExpiry(t *testing.T) {
	store := &testEmployeePresenceStore{calls: make(chan presenceWrite, 8)}
	coordinator := newEmployeePresenceCoordinatorWithOptions(store, employeePresenceOptions{
		touchInterval: time.Minute, stateTTL: 100 * time.Millisecond,
		writeTimeout: 200 * time.Millisecond, retryBase: 20 * time.Millisecond, retryMax: 40 * time.Millisecond,
	})
	t.Cleanup(coordinator.Close)
	session := newActivePresenceTestSession(107)
	manager := newWsConnectionManager()
	coordinator.request(session, true)
	_ = receivePresenceWrite(t, store.calls)
	waitForPresenceWriteCompletion(t, coordinator, session.EmployeeID)
	coordinator.request(session, false) // A throttled heartbeat must not retain this socket.
	manager.CloseSession(session)

	coordinator.mu.Lock()
	entry := coordinator.entries[session.EmployeeID]
	retained := false
	if entry != nil {
		_, retained = entry.sessions[session]
	}
	coordinator.mu.Unlock()
	if retained {
		t.Fatal("cooldown heartbeat retained a closed employee session")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		coordinator.mu.Lock()
		_, retainedEntry := coordinator.entries[session.EmployeeID]
		coordinator.mu.Unlock()
		if !retainedEntry {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("background TTL timer did not clean up idle presence state")
}

func TestEmployeeWebSocketActivationPongAndValidActivityUsePresenceLease(t *testing.T) {
	base := time.Now()
	clock := &atomic.Int64{}
	clock.Store(base.UnixNano())
	store := &testEmployeePresenceStore{calls: make(chan presenceWrite, 8)}
	coordinator := newEmployeePresenceCoordinatorWithOptions(store, employeePresenceOptions{
		now: func() time.Time { return time.Unix(0, clock.Load()) }, touchInterval: time.Minute,
		writeTimeout: 200 * time.Millisecond, retryBase: 20 * time.Millisecond, retryMax: 40 * time.Millisecond,
	})
	t.Cleanup(coordinator.Close)
	svc := newWsServiceForTest()
	svc.presence = coordinator
	peer, done := openPendingTestSocket(t, svc, realtimeRoleNotification, nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("employee websocket did not activate")
	}
	session := pendingTestSession(t, svc)
	if _, _, err := peer.ReadMessage(); err != nil { // Connected event.
		t.Fatal(err)
	}
	if got := receivePresenceWrite(t, store.calls); got.userID != 101 {
		t.Fatalf("activation presence user ID = %d, want 101", got.userID)
	}

	before := time.Now().Add(-5 * time.Second).Unix()
	session.LastActiveAt.Store(before)
	if err := peer.WriteControl(websocket.PongMessage, nil, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForSessionActivity(t, session, before)
	assertNoPresenceWrite(t, store.calls)

	before = time.Now().Add(-5 * time.Second).Unix()
	session.LastActiveAt.Store(before)
	if err := peer.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`)); err != nil {
		t.Fatal(err)
	}
	waitForSessionActivity(t, session, before)
	if _, _, err := peer.ReadMessage(); err != nil { // Application pong.
		t.Fatal(err)
	}
	assertNoPresenceWrite(t, store.calls)

	clock.Store(base.Add(time.Minute).UnixNano())
	before = time.Now().Add(-5 * time.Second).Unix()
	session.LastActiveAt.Store(before)
	if err := peer.WriteControl(websocket.PongMessage, nil, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waitForSessionActivity(t, session, before)
	if got := receivePresenceWrite(t, store.calls); got.userID != 101 {
		t.Fatalf("throttled Pong renewal user ID = %d, want 101", got.userID)
	}
	_ = peer.Close()
}

func TestRevokedEmployeeSessionCannotRenewPresence(t *testing.T) {
	store := &testEmployeePresenceStore{calls: make(chan presenceWrite, 8)}
	coordinator := newEmployeePresenceCoordinatorWithOptions(store, employeePresenceOptions{
		touchInterval: time.Minute, writeTimeout: 200 * time.Millisecond,
		retryBase: 20 * time.Millisecond, retryMax: 40 * time.Millisecond,
	})
	t.Cleanup(coordinator.Close)
	svc := newWsServiceForTest()
	svc.presence = coordinator
	peer, done := openPendingTestSocket(t, svc, realtimeRoleNotification, nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("employee websocket did not activate")
	}
	session := pendingTestSession(t, svc)
	if _, _, err := peer.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	_ = receivePresenceWrite(t, store.calls)
	lifecycle := svc.lifecycle.(*employeeWsTestLifecycle)
	if got := lifecycle.InvalidateLoginSession(session.LoginSessionID); got != 1 {
		t.Fatalf("revocation closed %d sessions, want 1", got)
	}
	svc.touchEmployeePresence(session, false)
	assertNoPresenceWrite(t, store.calls)
	_ = peer.Close()
}

func TestExpiredEmployeeSessionCannotRenewPresence(t *testing.T) {
	base := time.Now()
	clock := &atomic.Int64{}
	clock.Store(base.UnixNano())
	store := &testEmployeePresenceStore{calls: make(chan presenceWrite, 8)}
	coordinator := newEmployeePresenceCoordinatorWithOptions(store, employeePresenceOptions{
		now:           func() time.Time { return time.Unix(0, clock.Load()) },
		touchInterval: time.Minute, writeTimeout: 200 * time.Millisecond,
		retryBase: 20 * time.Millisecond, retryMax: 40 * time.Millisecond,
	})
	t.Cleanup(coordinator.Close)
	session := newActivePresenceTestSession(108)
	session.LifecycleDeadline = base.Add(time.Second)
	t.Cleanup(func() { session.cancelPresence() })

	coordinator.request(session, true)
	_ = receivePresenceWrite(t, store.calls)
	waitForPresenceWriteCompletion(t, coordinator, session.EmployeeID)

	clock.Store(session.LifecycleDeadline.Add(time.Nanosecond).UnixNano())
	coordinator.request(session, false)
	assertNoPresenceWrite(t, store.calls)
}

func TestEmployeePresenceSkipsWorkThatExpiresAfterSelection(t *testing.T) {
	base := time.Now()
	clock := &atomic.Int64{}
	clock.Store(base.UnixNano())
	store := &testEmployeePresenceStore{calls: make(chan presenceWrite, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := &employeePresenceCoordinator{
		store: store,
		options: employeePresenceOptions{
			now:           func() time.Time { return time.Unix(0, clock.Load()) },
			touchInterval: time.Minute,
			writeTimeout:  time.Second,
		},
		ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1),
		entries: make(map[int64]*employeePresenceEntry),
	}
	t.Cleanup(coordinator.Close)
	session := newActivePresenceTestSession(109)
	session.LifecycleDeadline = base.Add(time.Minute)
	t.Cleanup(func() { session.cancelPresence() })

	coordinator.request(session, true)
	work, _ := coordinator.nextWork(base)
	if work == nil {
		t.Fatal("active employee presence work was not selected")
	}
	clock.Store(session.LifecycleDeadline.Add(time.Nanosecond).UnixNano())
	coordinator.write(*work)
	assertNoPresenceWrite(t, store.calls)
}

func TestEmployeePresenceSkipsWorkRevokedAfterSelection(t *testing.T) {
	base := time.Now()
	store := &testEmployeePresenceStore{calls: make(chan presenceWrite, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := &employeePresenceCoordinator{
		store: store,
		options: employeePresenceOptions{
			now:           func() time.Time { return base },
			touchInterval: time.Minute,
			writeTimeout:  time.Second,
		},
		ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1),
		entries: make(map[int64]*employeePresenceEntry),
	}
	t.Cleanup(coordinator.Close)
	session := newActivePresenceTestSession(111)
	session.LifecycleDeadline = base.Add(time.Hour)
	t.Cleanup(func() { session.cancelPresence() })
	manager := newWsConnectionManager()
	manager.Register(session, nil)

	coordinator.request(session, true)
	work, _ := coordinator.nextWork(base)
	if work == nil {
		t.Fatal("active employee presence work was not selected")
	}
	invalidator := newEmployeeRealtimeLifecycle(manager, employeeAuthStateReader{})
	if got := invalidator.InvalidateLoginSession(session.LoginSessionID); got != 1 {
		t.Fatalf("revoke closed %d sessions, want 1", got)
	}
	coordinator.write(*work)
	if got := store.count.Load(); got != 0 {
		t.Fatalf("presence writes from selected but revoked session = %d, want 0", got)
	}
	coordinator.mu.Lock()
	_, retained := coordinator.entries[session.EmployeeID]
	coordinator.mu.Unlock()
	if retained {
		t.Fatal("revoked session retained pending presence work")
	}
}

func TestEmployeePresenceWriteDeadlineIsBoundedBySessionExpiry(t *testing.T) {
	base := time.Now()
	clock := &atomic.Int64{}
	clock.Store(base.UnixNano())
	deadline := base.Add(10 * time.Minute)
	store := &testEmployeePresenceStore{calls: make(chan presenceWrite, 2)}
	observedDeadline := make(chan time.Time, 1)
	store.write = func(ctx context.Context, _ int64, _ time.Time) error {
		got, ok := ctx.Deadline()
		if !ok {
			return errors.New("presence write context has no deadline")
		}
		observedDeadline <- got
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := &employeePresenceCoordinator{
		store: store,
		options: employeePresenceOptions{
			now:           func() time.Time { return time.Unix(0, clock.Load()) },
			touchInterval: time.Minute,
			writeTimeout:  20 * time.Minute,
		},
		ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1),
		entries: make(map[int64]*employeePresenceEntry),
	}
	t.Cleanup(coordinator.Close)
	session := newActivePresenceTestSession(110)
	session.LifecycleDeadline = deadline
	t.Cleanup(func() { session.cancelPresence() })

	coordinator.request(session, true)
	work, _ := coordinator.nextWork(base)
	if work == nil {
		t.Fatal("active employee presence work was not selected")
	}
	coordinator.write(*work)
	got := <-observedDeadline
	if !got.Equal(deadline) {
		t.Fatalf("presence write deadline = %v, want session deadline %v", got, deadline)
	}
}

func TestEmployeePresenceRetriesFailedWriteWithoutBlockingActivity(t *testing.T) {
	store := &testEmployeePresenceStore{calls: make(chan presenceWrite, 8)}
	var attempts atomic.Int64
	store.write = func(context.Context, int64, time.Time) error {
		if attempts.Add(1) == 1 {
			return errors.New("temporary database failure")
		}
		return nil
	}
	coordinator := newEmployeePresenceCoordinatorWithOptions(store, employeePresenceOptions{
		touchInterval: time.Minute, writeTimeout: 200 * time.Millisecond,
		retryBase: 20 * time.Millisecond, retryMax: 40 * time.Millisecond,
	})
	t.Cleanup(coordinator.Close)
	session := newActivePresenceTestSession(102)
	t.Cleanup(func() { session.cancelPresence() })

	started := time.Now()
	coordinator.request(session, true)
	if time.Since(started) > 50*time.Millisecond {
		t.Fatal("presence request waited on persistence")
	}
	_ = receivePresenceWrite(t, store.calls)
	_ = receivePresenceWrite(t, store.calls)
	if got := store.count.Load(); got != 2 {
		t.Fatalf("presence write calls = %d, want 2", got)
	}
}

func TestPresenceWriteFailureDoesNotRejectEmployeeWebSocketActivation(t *testing.T) {
	store := &testEmployeePresenceStore{calls: make(chan presenceWrite, 8)}
	var attempts atomic.Int64
	store.write = func(context.Context, int64, time.Time) error {
		if attempts.Add(1) == 1 {
			return errors.New("temporary database failure")
		}
		return nil
	}
	coordinator := newEmployeePresenceCoordinatorWithOptions(store, employeePresenceOptions{
		touchInterval: time.Minute, writeTimeout: 200 * time.Millisecond,
		retryBase: 20 * time.Millisecond, retryMax: 40 * time.Millisecond,
	})
	t.Cleanup(coordinator.Close)
	svc := newWsServiceForTest()
	svc.presence = coordinator
	peer, done := openPendingTestSocket(t, svc, realtimeRoleNotification, nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("presence database failure rejected authenticated activation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("presence persistence blocked authenticated activation")
	}
	if _, _, err := peer.ReadMessage(); err != nil {
		t.Fatalf("presence persistence failure interrupted WebSocket delivery: %v", err)
	}
	_ = receivePresenceWrite(t, store.calls)
	_ = receivePresenceWrite(t, store.calls)
	if attempts.Load() != 2 {
		t.Fatalf("presence retry attempts = %d, want 2", attempts.Load())
	}
	_ = peer.Close()
}

func TestEmployeePresenceSlowWriteCannotBlockPongAndIsCanceledOnSessionClose(t *testing.T) {
	store := &testEmployeePresenceStore{calls: make(chan presenceWrite, 8)}
	store.write = func(ctx context.Context, _ int64, _ time.Time) error {
		<-ctx.Done()
		return ctx.Err()
	}
	coordinator := newEmployeePresenceCoordinatorWithOptions(store, employeePresenceOptions{
		touchInterval: time.Minute, writeTimeout: 60 * time.Millisecond,
		retryBase: time.Second, retryMax: time.Second,
	})
	t.Cleanup(coordinator.Close)
	manager := newWsConnectionManager()
	session := newActivePresenceTestSession(103)

	started := time.Now()
	coordinator.request(session, true)
	if time.Since(started) > 50*time.Millisecond {
		t.Fatal("activation request waited for slow persistence")
	}
	_ = receivePresenceWrite(t, store.calls)
	started = time.Now()
	coordinator.request(session, false) // Same cheap path used by Pong and valid activity.
	if time.Since(started) > 50*time.Millisecond {
		t.Fatal("Pong renewal waited for slow persistence")
	}
	manager.CloseSession(session)
	select {
	case <-session.presenceCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("closing/revoking socket did not cancel its bounded presence write")
	}
	assertNoPresenceWrite(t, store.calls)
}

func TestEmployeePresenceExcludesCustomerAndRevokedSessions(t *testing.T) {
	store := &testEmployeePresenceStore{calls: make(chan presenceWrite, 8)}
	coordinator := newEmployeePresenceCoordinatorWithOptions(store, employeePresenceOptions{
		touchInterval: time.Minute, writeTimeout: 100 * time.Millisecond,
		retryBase: time.Second, retryMax: time.Second,
	})
	t.Cleanup(coordinator.Close)

	customer := newActivePresenceTestSession(104)
	customer.Role = realtimeRoleUser
	customer.CustomerID = 104
	customer.Principal = nil
	customer.External = &structExternalUser
	coordinator.request(customer, true)
	assertNoPresenceWrite(t, store.calls)

	revoked := newActivePresenceTestSession(105)
	manager := newWsConnectionManager()
	manager.CloseSession(revoked) // Same terminal path used by login-session invalidation.
	coordinator.request(revoked, true)
	assertNoPresenceWrite(t, store.calls)
}

func TestEmployeePresenceKeepsLeaseAcrossMultipleSessions(t *testing.T) {
	store := &testEmployeePresenceStore{calls: make(chan presenceWrite, 8)}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	store.write = func(ctx context.Context, _ int64, _ time.Time) error {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	coordinator := newEmployeePresenceCoordinatorWithOptions(store, employeePresenceOptions{
		touchInterval: time.Minute, writeTimeout: time.Second,
		retryBase: 20 * time.Millisecond, retryMax: 40 * time.Millisecond,
	})
	t.Cleanup(coordinator.Close)
	manager := newWsConnectionManager()
	first := newActivePresenceTestSession(106)
	second := newActivePresenceTestSession(106)
	coordinator.request(first, true)
	_ = receivePresenceWrite(t, store.calls)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first presence write did not start")
	}
	coordinator.request(second, true)
	manager.CloseSession(first)
	close(release)
	if got := receivePresenceWrite(t, store.calls); got.userID != 106 {
		t.Fatalf("second-session presence write = %+v", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("multi-session write calls = %d, want first plus surviving-session retry", calls.Load())
	}
	manager.CloseSession(second)
}

func TestAgentProfilePresenceUpdatesOnlyActiveProfile(t *testing.T) {
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&models.AgentProfile{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	previousDB := sqls.DB()
	sqls.SetDB(db)
	t.Cleanup(func() { sqls.SetDB(previousDB) })

	base := time.Date(2026, time.October, 10, 2, 0, 0, 0, time.UTC)
	active := models.AgentProfile{UserID: 201, TeamID: 1, AgentCode: "presence-active", DisplayName: "Active", Status: enums.StatusOk, AuditFields: models.AuditFields{CreatedAt: base.Add(-time.Hour), UpdatedAt: base.Add(-time.Hour)}}
	disabled := models.AgentProfile{UserID: 202, TeamID: 1, AgentCode: "presence-disabled", DisplayName: "Disabled", Status: enums.StatusDisabled, AuditFields: models.AuditFields{CreatedAt: base.Add(-time.Hour), UpdatedAt: base.Add(-time.Hour)}}
	if err := db.Create(&active).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&disabled).Error; err != nil {
		t.Fatal(err)
	}
	if err := AgentProfileService.TouchOnlineAt(context.Background(), active.UserID, base); err != nil {
		t.Fatalf("touch active profile: %v", err)
	}
	if err := AgentProfileService.TouchOnlineAt(context.Background(), disabled.UserID, base); err != nil {
		t.Fatalf("touch disabled profile: %v", err)
	}
	var actual models.AgentProfile
	if err := db.First(&actual, "user_id = ?", active.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if actual.LastOnlineAt == nil || !actual.LastOnlineAt.Equal(base) {
		t.Fatalf("active LastOnlineAt = %v, want %v", actual.LastOnlineAt, base)
	}
	if !actual.UpdatedAt.Equal(active.UpdatedAt) {
		t.Fatalf("presence touch changed UpdatedAt: got %v, want %v", actual.UpdatedAt, active.UpdatedAt)
	}
	actual = models.AgentProfile{}
	if err := db.First(&actual, "user_id = ?", disabled.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if actual.LastOnlineAt != nil {
		t.Fatalf("disabled profile was marked online at %v", actual.LastOnlineAt)
	}
}

var structExternalUser = openidentity.ExternalUser{ExternalID: "customer"}

func newActivePresenceTestSession(userID int64) *ClientSession {
	ctx, cancel := context.WithCancel(context.Background())
	session := &ClientSession{
		EmployeeID: userID, LoginSessionID: userID + 1_000_000,
		Principal: &dto.AuthPrincipal{UserID: userID}, Role: realtimeRoleAdmin,
		LifecycleDeadline: time.Now().Add(time.Hour), Send: make(chan []byte, 1),
		presenceCtx: ctx, presenceCancel: cancel,
	}
	session.employeePhase.Store(uint32(employeePhaseActive))
	return session
}

func receivePresenceWrite(t *testing.T, calls <-chan presenceWrite) presenceWrite {
	t.Helper()
	select {
	case got := <-calls:
		return got
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for employee presence write")
		return presenceWrite{}
	}
}

func waitForPresenceWriteCompletion(t *testing.T, coordinator *employeePresenceCoordinator, userID int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		coordinator.mu.Lock()
		entry := coordinator.entries[userID]
		inFlight := entry != nil && entry.inFlight
		coordinator.mu.Unlock()
		if !inFlight {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("presence write did not finish")
}

func assertNoPresenceWrite(t *testing.T, calls <-chan presenceWrite) {
	t.Helper()
	select {
	case got := <-calls:
		t.Fatalf("unexpected employee presence write: %+v", got)
	case <-time.After(80 * time.Millisecond):
	}
}

func waitForSessionActivity(t *testing.T, session *ClientSession, previous int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if session.LastActiveAt.Load() != previous {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("WebSocket activity callback did not update the session")
}
