package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func TestWsPendingMutationBeforeRegistration(t *testing.T) { testWsPendingRevokeOrdering(t, true) }
func TestWsPendingRegistrationBeforeCallback(t *testing.T) { testWsPendingRevokeOrdering(t, false) }

func TestEmployeeDeliveryDeadlineAtSendLock(t *testing.T) {
	svc := newWsServiceForTest()
	session := &ClientSession{ID: "deadline-at-send", Role: realtimeRoleNotification, Principal: employeeViewPrincipal(), Topics: make(map[string]struct{}), Send: make(chan []byte, 64)}
	markActiveEmployeeTestSession(session)
	deadline := time.Now().Add(5 * time.Second)
	session.LifecycleDeadline = deadline
	svc.manager.Register(session, []string{"notification:101"})
	var logs wsSessionConcurrencyLogBuffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)
	session.sendMu.Lock()
	var unlock sync.Once
	release := func() { unlock.Do(session.sendMu.Unlock) }
	defer release()
	done := make(chan struct{})
	publisherID := make(chan string, 1)
	go func() {
		defer close(done)
		stack := make([]byte, 64)
		n := runtime.Stack(stack, false)
		fields := strings.Fields(string(stack[:n]))
		if len(fields) >= 2 {
			publisherID <- fields[1]
		} else {
			publisherID <- ""
		}
		svc.PublishToTopic("notification:101", RealtimeEvent{EventID: "protected-deadline-marker", Type: enums.IMRealtimeEventNotificationCreated, Data: RealtimeNotificationCreatedPayload{}})
	}()
	id := <-publisherID
	if id == "" {
		t.Fatal("could not identify publication goroutine")
	}
	// Stack observation fences the actual post-validation enqueue lock wait;
	// restrict it to this publication goroutine so unrelated stacks cannot
	// make the deadline mutation race with initial validation.
	waitWsGoroutineStacks(t, "employee enqueue waiting on sendMu", func(stacks map[string]string) bool {
		stack := stacks[id]
		return strings.Contains(stack, "(*ClientSession).enqueueWithAuthorization(") && strings.Contains(stack, "sync.(*Mutex).Lock(")
	})
	if remaining := time.Until(deadline); remaining > 0 {
		timer := time.NewTimer(remaining)
		<-timer.C
	}
	release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deadline enqueue did not finish after lock release")
	}
	if len(session.Send) != 0 {
		t.Fatal("protected output crossed deadline while waiting for sendMu")
	}
	if !session.Closed.Load() || svc.manager.Count() != 0 {
		t.Fatal("expired enqueue did not close outside sendMu")
	}
	svc.closeSession(session)
	if logs.count([]byte(`"connId":"deadline-at-send"`)) != 1 {
		t.Fatal("expired enqueue did not close exactly once")
	}
}

func TestEmployeeDeliveryDeadlineAtSubscriptionLock(t *testing.T) {
	_, _, principal := employeeRealtimeScopeFixture(t)
	svc := newWsServiceForTest()
	session := &ClientSession{ID: "deadline-at-subscribe", Role: realtimeRoleAdmin, Principal: principal, Topics: make(map[string]struct{}), Send: make(chan []byte, 64)}
	markActiveEmployeeTestSession(session)
	svc.manager.Register(session, nil)
	var logs wsSessionConcurrencyLogBuffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)
	svc.manager.mu.Lock()
	var unlock sync.Once
	release := func() { unlock.Do(svc.manager.mu.Unlock) }
	defer release()
	done := make(chan []string, 1)
	go func() { done <- svc.subscribeTopics(session, []string{"conversation:42"}) }()
	waitWsGoroutineStacks(t, "employee subscription waiting on manager.mu", func(stacks map[string]string) bool {
		for _, stack := range stacks {
			if strings.Contains(stack, "(*WsConnectionManager).Subscribe(") && strings.Contains(stack, "sync.(*RWMutex).Lock(") {
				return true
			}
		}
		return false
	})
	session.sendMu.Lock()
	session.LifecycleDeadline = time.Now()
	session.sendMu.Unlock()
	release()
	select {
	case topics := <-done:
		if len(topics) != 0 {
			t.Fatalf("expired subscription acknowledged %v", topics)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expired admission deadlocked closing with manager.mu held")
	}
	if svc.manager.HasTopic("conversation:42") || len(session.Topics) != 0 {
		t.Fatal("expired membership inserted after manager lock wait")
	}
	if !session.Closed.Load() || svc.manager.Count() != 0 {
		t.Fatal("expired admission did not close after releasing manager.mu")
	}
	svc.closeSession(session)
	if logs.count([]byte(`"connId":"deadline-at-subscribe"`)) != 1 {
		t.Fatal("expired admission did not close exactly once")
	}
}

func testWsPendingRevokeOrdering(t *testing.T, revokeBeforeRegistration bool) {
	db := lifecycleAuthTestDB(t)
	grant := createRBACTestGrant(t, db, "conversation.view", enums.StatusOk, enums.StatusOk, true, nil, nil)
	row := seedEmployeeSnapshotSession(t, db, grant.UserID, time.Now())
	initial, err := (employeeAuthStateReader{}).ReadEmployeeSession(row.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	svc := newWsServiceForTest()
	lifecycle := svc.lifecycle.(*employeeWsTestLifecycle)
	lifecycle.readCurrent = lifecycle.employeeRealtimeLifecycle.RevalidateEmployeeSession
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	lifecycle.beforeRead = func(id int64) {
		if id != row.ID {
			t.Errorf("revalidated wrong login ID %d", id)
		}
		close(entered)
		<-release
	}
	revoke := func() {
		if err := db.Model(&row).Update("revoked_at", time.Now()).Error; err != nil {
			t.Fatal(err)
		}
	}
	peer, done := openPendingTestSocket(t, svc, realtimeRoleAdmin, func(ctx *gin.Context) {
		ctx.Set(authPrincipalContextKey, initial.Principal)
		AuthService.setAuthenticatedEmployeeSession(ctx, authenticatedEmployeeSession{EmployeeID: initial.EmployeeID, LoginSessionID: initial.LoginSessionID, LoginSessionExpiresAt: initial.LoginSessionExpiresAt, Principal: initial.Principal})
		if revokeBeforeRegistration {
			revoke()
			if n := lifecycle.InvalidateLoginSession(row.ID); n != 0 {
				t.Errorf("pre-registration scan closed %d sessions", n)
			}
		}
	})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("revalidator barrier never reached")
	}
	session := pendingTestSession(t, svc)
	assertPendingConfidentiality(t, svc, session)
	if !revokeBeforeRegistration {
		revoke()
		if n := lifecycle.InvalidateLoginSession(row.ID); n != 1 {
			t.Fatalf("pending callback closed %d", n)
		}
	}
	unblock()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("revoked pending upgrade accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revoked upgrade did not finish")
	}
	if employeeLifecyclePhase(session.employeePhase.Load()) == employeePhaseActive || svc.manager.HasTopic("admin:all") || !session.Closed.Load() || len(session.Send) != 0 {
		t.Fatal("pending session was resurrected")
	}
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, body, err := peer.ReadMessage(); err == nil {
		t.Fatalf("frame escaped revoked pending socket: %s", body)
	} else {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			t.Fatal("revoked peer did not close")
		}
	}
}

func TestWsPendingActivationAfterCloseRejected(t *testing.T) {
	svc := newWsServiceForTest()
	lifecycle := svc.lifecycle.(*employeeWsTestLifecycle)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	lifecycle.beforeRead = func(int64) { close(entered); <-release }
	_, done := openPendingTestSocket(t, svc, realtimeRoleNotification, nil)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("revalidator never invoked")
	}
	session := pendingTestSession(t, svc)
	assertPendingConfidentiality(t, svc, session)
	if lifecycle.InvalidateLoginSession(session.LoginSessionID) != 1 {
		t.Fatal("pending session absent from invalidation scan")
	}
	unblock()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed pending upgrade accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closed upgrade did not finish")
	}
	if _, activated := svc.manager.Activate(session, lifecycleTestSnapshot(101, 1000101), []string{"notification:101"}); activated || svc.manager.HasTopic("notification:101") || !session.Closed.Load() {
		t.Fatal("pending session was resurrected")
	}
}

func TestWsSessionNormalDelivery(t *testing.T) {
	service, peer, session := newWsPumpTestSocket(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.writePump(session)
	}()
	t.Cleanup(func() {
		closeWsSessionWithinDeadline(t, service, session)
		waitWsPumpDone(t, done, "writer")
	})

	payload := []byte("normal delivery")
	if !session.enqueue(payload) {
		t.Fatal("enqueue on open session = false, want true")
	}
	assertWsPeerText(t, peer, payload)
}

func TestWsWriterPumpTerminatesAfterSessionClose(t *testing.T) {
	service, _, session := newWsPumpTestSocket(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.writePump(session)
	}()
	closeWsSessionWithinDeadline(t, service, session)
	waitWsPumpDone(t, done, "writer")
}

func TestWsReaderPumpTerminatesAfterSessionClose(t *testing.T) {
	service, peer, session := newWsPumpTestSocket(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.readPump(session)
	}()
	if err := peer.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := peer.WriteJSON(map[string]string{"type": "ping"}); err != nil {
		t.Fatalf("send reader pump probe: %v", err)
	}
	select {
	case payload, ok := <-session.Send:
		if !ok || !bytes.Contains(payload, []byte(`"type":"pong"`)) {
			t.Fatalf("reader pump probe response = %q, open=%t; want pong", payload, ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader pump did not process probe within two seconds")
	}
	closeWsSessionWithinDeadline(t, service, session)
	waitWsPumpDone(t, done, "reader")
}

func TestWsSessionCloseAcrossHandlerRoles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		role  string
		topic string
		open  func(*testing.T, *wsService) *websocket.Conn
	}{
		{
			name: "dashboard employee", role: realtimeRoleAdmin, topic: "admin:101",
			open: func(t *testing.T, service *wsService) *websocket.Conn {
				return openWsDashboardPumpTestSocket(t, service, 101, service.HandleDashboardWS)
			},
		},
		{
			name: "dashboard notification", role: realtimeRoleNotification, topic: "notification:102",
			open: func(t *testing.T, service *wsService) *websocket.Conn {
				return openWsDashboardPumpTestSocket(t, service, 102, service.HandleDashboardNotificationWS)
			},
		},
		{
			name: "synthetic customer/open", role: realtimeRoleUser, topic: "customer:103",
			open: func(t *testing.T, service *wsService) *websocket.Conn {
				return openCustomerIdentityTestSocket(t, service, 103, &openidentity.ExternalUser{ExternalID: "pump-test-customer"})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Keep role cases serial: the only new pump pair must belong to this
			// one handler connection. Ambiguous observations fail the test.
			before := waitWsGoroutineStacks(t, "initial snapshot", func(map[string]string) bool { return true })
			service := newWsServiceForTest()
			peer := tc.open(t, service)
			if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if messageType, _, err := peer.ReadMessage(); err != nil || messageType != websocket.TextMessage {
				t.Fatalf("connected frame: type=%d, error=%v", messageType, err)
			}
			members := service.manager.FindByTopics([]string{tc.topic})
			if len(members) != 1 || members[0].Role != tc.role {
				t.Fatalf("registered %s sessions = %v, want one %s session", tc.topic, members, tc.role)
			}
			session := members[0]
			t.Cleanup(func() { closeWsSessionWithinDeadline(t, service, session) })
			payload := []byte("queued role delivery")
			if tc.role == realtimeRoleNotification {
				var err error
				payload, err = json.Marshal(RealtimeEvent{
					Type:  enums.IMRealtimeEventNotificationCreated,
					Topic: tc.topic,
					Data: RealtimeNotificationCreatedPayload{Notification: response.NotificationResponse{
						ID:              1,
						RecipientUserID: 102,
					}},
				})
				if err != nil {
					t.Fatalf("marshal ordinary notification test payload: %v", err)
				}
			}
			if !session.enqueue(payload) {
				t.Fatal("enqueue on handler session = false, want true")
			}
			assertWsPeerText(t, peer, payload)

			pumps := make(map[string]string)
			waitWsGoroutineStacks(t, "handler pump startup", func(stacks map[string]string) bool {
				clear(pumps)
				counts := make(map[string]int)
				for id, stack := range stacks {
					if _, existed := before[id]; existed {
						continue
					}
					if !strings.Contains(stack, "\ncreated by github.com/Swhshd/kestreldesk/internal/services.(*wsService).upgradeConnection ") {
						continue
					}
					for _, pump := range []string{"readPump", "writePump"} {
						if strings.Contains(stack, "\ngithub.com/Swhshd/kestreldesk/internal/services.(*wsService)."+pump+"(") {
							pumps[id] = pump
							counts[pump]++
						}
					}
				}
				return len(pumps) == 2 && counts["readPump"] == 1 && counts["writePump"] == 1
			})
			closeWsSessionWithinDeadline(t, service, session)
			waitWsGoroutineStacks(t, "handler pump termination", func(stacks map[string]string) bool {
				for id := range pumps {
					if _, alive := stacks[id]; alive {
						return false
					}
				}
				return true
			})
			if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := peer.ReadMessage(); err == nil {
				t.Fatal("peer read succeeded after server-side close")
			} else {
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					t.Fatalf("peer read timed out after server-side close: %v", err)
				}
			}
			if !session.Closed.Load() || len(service.manager.FindByTopics([]string{tc.topic})) != 0 {
				t.Fatal("closed handler session remains open or registered")
			}
		})
	}
}

func TestWsSessionCloseSyntheticRuntime(t *testing.T) {
	service := newWsServiceForTest()
	router := gin.New()
	router.GET("/target", func(ctx *gin.Context) {
		bindEmployeeWsTestSession(t, service, ctx, &dto.AuthPrincipal{UserID: 101})
		service.HandleDashboardNotificationWS(ctx)
	})
	router.GET("/unaffected", func(ctx *gin.Context) {
		bindEmployeeWsTestSession(t, service, ctx, &dto.AuthPrincipal{UserID: 102})
		service.HandleDashboardNotificationWS(ctx)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	beforeTarget := waitWsGoroutineStacks(t, "synthetic target baseline", func(map[string]string) bool { return true })
	dial := func(path string) *websocket.Conn {
		t.Helper()
		peer, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+path, nil)
		if err != nil {
			t.Fatalf("synthetic runtime handshake for %s: %v", path, err)
		}
		t.Cleanup(func() { _ = peer.Close() })
		if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if messageType, _, err := peer.ReadMessage(); err != nil || messageType != websocket.TextMessage {
			t.Fatalf("synthetic runtime connected frame for %s: type=%d error=%v", path, messageType, err)
		}
		return peer
	}
	targetPeer := dial("/target")
	targetPumps := make(map[string]string)
	waitWsGoroutineStacks(t, "synthetic target pump startup", func(stacks map[string]string) bool {
		clear(targetPumps)
		counts := make(map[string]int)
		for id, stack := range stacks {
			if _, existed := beforeTarget[id]; existed || !strings.Contains(stack, "\ncreated by github.com/Swhshd/kestreldesk/internal/services.(*wsService).upgradeConnection ") {
				continue
			}
			for _, pump := range []string{"readPump", "writePump"} {
				if strings.Contains(stack, "\ngithub.com/Swhshd/kestreldesk/internal/services.(*wsService)."+pump+"(") {
					targetPumps[id] = pump
					counts[pump]++
				}
			}
		}
		return len(targetPumps) == 2 && counts["readPump"] == 1 && counts["writePump"] == 1
	})
	afterTarget := waitWsGoroutineStacks(t, "synthetic unaffected baseline", func(map[string]string) bool { return true })
	unaffectedPeer := dial("/unaffected")
	unaffectedPumps := make(map[string]string)
	waitWsGoroutineStacks(t, "synthetic unaffected pump startup", func(stacks map[string]string) bool {
		clear(unaffectedPumps)
		counts := make(map[string]int)
		for id, stack := range stacks {
			if _, existed := afterTarget[id]; existed || !strings.Contains(stack, "\ncreated by github.com/Swhshd/kestreldesk/internal/services.(*wsService).upgradeConnection ") {
				continue
			}
			for _, pump := range []string{"readPump", "writePump"} {
				if strings.Contains(stack, "\ngithub.com/Swhshd/kestreldesk/internal/services.(*wsService)."+pump+"(") {
					unaffectedPumps[id] = pump
					counts[pump]++
				}
			}
		}
		return len(unaffectedPumps) == 2 && counts["readPump"] == 1 && counts["writePump"] == 1
	})

	targets := service.manager.FindDeliveries([]string{"notification:101"})
	if len(targets) != 1 || targets[0].Session.Role != realtimeRoleNotification {
		t.Fatalf("synthetic target registrations = %v, want one notification session", targets)
	}
	target := targets[0].Session
	unaffected := service.manager.FindDeliveries([]string{"notification:102"})
	if len(unaffected) != 1 || unaffected[0].Session.Role != realtimeRoleNotification {
		t.Fatalf("synthetic unaffected registrations = %v, want one notification session", unaffected)
	}
	t.Cleanup(func() { closeWsSessionWithinDeadline(t, service, unaffected[0].Session) })

	stale := service.manager.FindDeliveries([]string{"notification:101"})
	if len(stale) != 1 || stale[0].Session != target {
		t.Fatalf("synthetic stale delivery targets = %v, want target session %p", stale, target)
	}

	type outcome struct {
		name       string
		panicValue any
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	go func() {
		result := outcome{name: "publish"}
		defer func() {
			result.panicValue = recover()
			outcomes <- result
		}()
		<-start
		service.PublishToTopics(
			[]string{"notification:101", "notification:102"},
			RealtimeEvent{EventID: "synthetic-close-race-marker", Type: "synthetic.marker", Topic: "notification:101", At: "synthetic"},
		)
	}()
	go func() {
		result := outcome{name: "close"}
		defer func() {
			result.panicValue = recover()
			outcomes <- result
		}()
		<-start
		service.closeSession(target)
	}()
	close(start)

	for range 2 {
		select {
		case result := <-outcomes:
			if result.panicValue != nil {
				t.Errorf("synthetic %s panicked during concurrent publish/close: %v", result.name, result.panicValue)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("synthetic concurrent publish/close did not complete within two seconds")
		}
	}
	if target.enqueue([]byte("synthetic stale post-close marker")) {
		t.Fatal("synthetic stale delivery enqueue after close = true, want false")
	}
	service.manager.mu.RLock()
	_, targetRegistered := service.manager.sessions[target.ID]
	service.manager.mu.RUnlock()
	if targetRegistered {
		t.Fatal("synthetic target remains registered after close")
	}

	if err := targetPeer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		if _, _, err := targetPeer.ReadMessage(); err == nil {
			continue // A concurrent publish may enqueue before close linearizes.
		} else {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				t.Fatalf("synthetic target peer did not observe disconnect within two seconds: %v", err)
			}
			break
		}
	}
	waitWsGoroutineStacks(t, "synthetic target pump termination", func(stacks map[string]string) bool {
		for id := range targetPumps {
			if _, alive := stacks[id]; alive {
				return false
			}
		}
		return true
	})
	assertWsPeerText(t, unaffectedPeer, []byte(`{"eventId":"synthetic-close-race-marker","type":"synthetic.marker","topic":"notification:101","at":"synthetic"}`))
	closeWsSessionWithinDeadline(t, service, unaffected[0].Session)
	waitWsGoroutineStacks(t, "synthetic unaffected pump termination", func(stacks map[string]string) bool {
		for id := range unaffectedPumps {
			if _, alive := stacks[id]; alive {
				return false
			}
		}
		return true
	})
}

func newWsPumpTestSocket(t *testing.T) (*wsService, *websocket.Conn, *ClientSession) {
	t.Helper()
	service := newWsServiceForTest()
	sessions := make(chan *ClientSession, 1)
	router := gin.New()
	router.GET("/ws", func(ctx *gin.Context) {
		conn, err := service.upgrader.Upgrade(ctx.Writer, ctx.Request, nil)
		if err != nil {
			return
		}
		sessions <- &ClientSession{Conn: conn, Topics: make(map[string]struct{}), Send: make(chan []byte, realtimeSendBufferSize)}
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	peer, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("pump test handshake: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	select {
	case session := <-sessions:
		t.Cleanup(func() { closeWsSessionWithinDeadline(t, service, session) })
		return service, peer, session
	case <-time.After(2 * time.Second):
		t.Fatal("server-side Gorilla connection was not received")
		return nil, nil, nil
	}
}

func openWsDashboardPumpTestSocket(t *testing.T, service *wsService, userID int64, handler gin.HandlerFunc) *websocket.Conn {
	t.Helper()
	router := gin.New()
	router.GET("/ws", func(ctx *gin.Context) {
		bindEmployeeWsTestSession(t, service, ctx, &dto.AuthPrincipal{UserID: userID})
		handler(ctx)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	peer, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dashboard handshake: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	return peer
}

func assertWsPeerText(t *testing.T, peer *websocket.Conn, want []byte) {
	t.Helper()
	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	messageType, got, err := peer.ReadMessage()
	if err != nil {
		t.Fatalf("read queued text: %v", err)
	}
	if messageType != websocket.TextMessage || !bytes.Equal(got, want) {
		t.Fatalf("queued frame = (type %d, %q), want text %q", messageType, got, want)
	}
}

func waitWsPumpDone(t *testing.T, done <-chan struct{}, pump string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s pump did not terminate within two seconds", pump)
	}
}

func closeWsSessionWithinDeadline(t *testing.T, service *wsService, session *ClientSession) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.closeSession(session)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("closeSession did not complete within two seconds")
	}
}

// Observe full goroutine stacks rather than counts or socket effects. After
// startup, requiring the captured IDs to disappear also covers deferred cleanup;
// merely losing a pump frame is not sufficient evidence of goroutine exit.
func waitWsGoroutineStacks(t *testing.T, operation string, ready func(map[string]string) bool) map[string]string {
	t.Helper()
	done := make(chan map[string]string, 1)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			buffer := make([]byte, 16*1024)
			var n int
			for {
				n = runtime.Stack(buffer, true)
				if n < len(buffer) {
					break
				}
				buffer = make([]byte, len(buffer)*2)
			}
			stacks := make(map[string]string)
			for _, stack := range strings.Split(string(buffer[:n]), "\n\n") {
				header, _, _ := strings.Cut(stack, "\n")
				fields := strings.Fields(header)
				if len(fields) >= 2 && fields[0] == "goroutine" {
					stacks[fields[1]] = stack
				}
			}
			if ready(stacks) {
				done <- stacks
				return
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	select {
	case stacks := <-done:
		return stacks
	case <-time.After(2 * time.Second):
		t.Fatalf("%s was not observed within two seconds", operation)
		return nil
	}
}

func TestClientSessionEnqueueCharacterization(t *testing.T) {
	var nilSession *ClientSession
	if nilSession.enqueue([]byte("nil")) {
		t.Fatal("enqueue on a nil session = true, want false")
	}

	payload := []byte("payload")
	openSession := &ClientSession{Send: make(chan []byte, 1)}
	if !openSession.enqueue(payload) {
		t.Fatal("enqueue on an open session with room = false, want true")
	}
	if got := <-openSession.Send; !bytes.Equal(got, payload) {
		t.Fatalf("queued payload = %q, want %q", got, payload)
	}

	fullSession := &ClientSession{Send: make(chan []byte, 1)}
	queued := []byte("already queued")
	fullSession.Send <- queued
	if fullSession.enqueue(payload) {
		t.Fatal("enqueue on a full buffer = true, want false")
	}
	if got := <-fullSession.Send; !bytes.Equal(got, queued) {
		t.Fatalf("queued payload after full-buffer enqueue = %q, want %q", got, queued)
	}

	closedSession := &ClientSession{Send: make(chan []byte, 1)}
	closedSession.Closed.Store(true)
	if closedSession.enqueue(payload) {
		t.Fatal("enqueue on a closed session = true, want false")
	}
	if got := len(closedSession.Send); got != 0 {
		t.Fatalf("closed session queued %d payloads, want 0", got)
	}
}

func TestClientSessionEnqueueCloseLinearization(t *testing.T) {
	service := newWsServiceForTest()
	session := &ClientSession{Send: make(chan []byte, 1)}
	observedOpen := make(chan struct{})
	resume := make(chan struct{})
	var resumeOnce sync.Once
	release := func() { resumeOnce.Do(func() { close(resume) }) }
	defer release()

	type enqueueOutcome struct {
		queued     bool
		panicValue any
	}
	outcomes := make(chan enqueueOutcome, 1)
	go func() {
		outcome := enqueueOutcome{}
		defer func() {
			outcome.panicValue = recover()
			outcomes <- outcome
		}()
		outcome.queued = session.enqueueWithBeforeSend([]byte("payload"), func() {
			close(observedOpen)
			<-resume
		})
	}()

	select {
	case <-observedOpen:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue did not reach the open-observation barrier")
	}

	closed := make(chan struct{})
	go func() {
		service.closeSession(session)
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("closeSession blocked while enqueue was at the barrier")
	}
	select {
	case payload, ok := <-session.Send:
		if ok {
			t.Fatalf("Send yielded %q before barrier release, want closed channel", payload)
		}
	default:
		t.Fatal("Send is still open after closeSession completed")
	}

	release()
	select {
	case outcome := <-outcomes:
		if outcome.panicValue != nil {
			t.Errorf("enqueue panicked: %v; want no panic", outcome.panicValue)
		}
		if outcome.queued {
			t.Error("enqueue after close = true, want false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue did not complete after barrier release")
	}
	if got := len(session.Send); got != 0 {
		t.Fatalf("queued payloads after close = %d, want 0", got)
	}
}

func TestEmployeeRevocationRejectsPreparedProtectedPublication(t *testing.T) {
	service := newWsServiceForTest()
	topic := service.notificationTopic(101)
	session := &ClientSession{
		ID:        "revoke-prepared-publication",
		Role:      realtimeRoleNotification,
		Principal: &dto.AuthPrincipal{UserID: 101},
		Topics:    make(map[string]struct{}),
		Send:      make(chan []byte, realtimeSendBufferSize),
	}
	markActiveEmployeeTestSession(session)
	service.manager.Register(session, []string{topic})
	staleTargets := service.manager.FindDeliveries([]string{topic})
	if len(staleTargets) != 1 || staleTargets[0].Session != session {
		t.Fatalf("prepared delivery targets = %v, want the employee session", staleTargets)
	}
	event := RealtimeEvent{
		EventID: "synthetic-protected-revoke-marker",
		Type:    enums.IMRealtimeEventNotificationCreated,
		Topic:   topic,
		Data: RealtimeNotificationCreatedPayload{Notification: response.NotificationResponse{
			ID: 1, RecipientUserID: 101,
		}},
	}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal protected publication: %v", err)
	}

	prepared, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releasePublication := func() { releaseOnce.Do(func() { close(release) }) }
	queued := make(chan bool, 1)
	var publisher sync.WaitGroup
	publisher.Add(1)
	go func() {
		defer publisher.Done()
		queued <- staleTargets[0].Session.enqueueWithBeforeSend(payload, func() {
			close(prepared)
			<-release
		})
	}()
	t.Cleanup(func() {
		releasePublication()
		publisher.Wait()
	})
	select {
	case <-prepared:
	case <-time.After(2 * time.Second):
		t.Fatal("publication did not reach the pre-enqueue barrier")
	}

	// This is the PR #5 authoritative terminal transition, reached through the
	// same exact-session invalidation used after persisted LoginSession revoke.
	invalidator := service.lifecycle.(*employeeWsTestLifecycle)
	if got := invalidator.InvalidateLoginSession(session.LoginSessionID); got != 1 {
		t.Fatalf("exact-session invalidation closed %d sockets, want 1", got)
	}
	if !session.Closed.Load() || len(session.Send) != 0 {
		t.Fatal("protected publication was queued after revocation became authoritative")
	}
	releasePublication()
	select {
	case wasQueued := <-queued:
		if wasQueued {
			t.Fatal("prepared protected publication crossed the revocation boundary")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("prepared publication did not finish after revocation")
	}
	if got := len(session.Send); got != 0 {
		t.Fatalf("protected payloads queued after revocation = %d, want 0", got)
	}
	assertWsSessionCleanup(t, service, session, topic)
}

func TestWsSessionConcurrentEnqueueClose(t *testing.T) {
	for round := 0; round < 32; round++ {
		service, session, topic := newWsSessionConcurrencyFixture("concurrent-enqueue-close")
		workers := make([]func(), 0, 40)
		for producer := 0; producer < 32; producer++ {
			workers = append(workers, func() {
				for attempt := 0; attempt < 128; attempt++ {
					session.enqueue([]byte("payload"))
				}
			})
		}
		for closer := 0; closer < 8; closer++ {
			workers = append(workers, func() { service.closeSession(session) })
		}
		runWsSessionConcurrencyWorkers(t, workers)
		assertWsSessionCleanup(t, service, session, topic)
		assertWsSessionSendClosed(t, session)
	}
}

func TestWsSessionConcurrentClose(t *testing.T) {
	service, session, topic := newWsSessionConcurrencyFixture("concurrent-close")
	var logs wsSessionConcurrencyLogBuffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)

	workers := make([]func(), 32)
	for i := range workers {
		workers[i] = func() { service.closeSession(session) }
	}
	runWsSessionConcurrencyWorkers(t, workers)
	service.closeSession(session)
	assertWsSessionCleanup(t, service, session, topic)
	assertWsSessionSendClosed(t, session)
	if got := logs.count([]byte(`"connId":"concurrent-close"`)); got != 1 {
		t.Fatalf("disconnect cleanup records = %d, want exactly 1", got)
	}
}

type wsSessionConcurrencyLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *wsSessionConcurrencyLogBuffer) Write(payload []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(payload)
}

func (b *wsSessionConcurrencyLogBuffer) count(pattern []byte) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Count(b.buffer.Bytes(), pattern)
}

func TestWsSessionQueueFullClose(t *testing.T) {
	service, session, topic := newWsSessionConcurrencyFixture("queue-full-close")
	for i := 0; i < 64; i++ {
		if !session.enqueue([]byte("queued")) {
			t.Fatalf("enqueue entry %d = false, want true", i)
		}
	}
	if session.enqueue([]byte("overflow")) {
		t.Fatal("enqueue on a full 64-entry queue = true, want false")
	}
	runWsSessionConcurrencyWorkers(t, []func(){func() { service.closeSession(session) }})
	if got := len(session.Send); got != 64 {
		t.Fatalf("queued payloads after close = %d, want 64", got)
	}
	assertWsSessionCleanup(t, service, session, topic)
	assertWsSessionSendClosed(t, session)
}

func TestWsSessionEnqueueAfterClose(t *testing.T) {
	service, session, topic := newWsSessionConcurrencyFixture("enqueue-after-close")
	if !session.enqueue([]byte("already queued")) {
		t.Fatal("enqueue before close = false, want true")
	}
	runWsSessionConcurrencyWorkers(t, []func(){func() { service.closeSession(session) }})
	for attempt := 0; attempt < 128; attempt++ {
		if session.enqueue([]byte("after close")) {
			t.Fatalf("post-close enqueue attempt %d = true, want false", attempt)
		}
		if got := len(session.Send); got != 1 {
			t.Fatalf("queue length after post-close enqueue = %d, want 1", got)
		}
	}
	if got := <-session.Send; !bytes.Equal(got, []byte("already queued")) {
		t.Fatalf("queued payload after post-close enqueue = %q, want already queued", got)
	}
	assertWsSessionCleanup(t, service, session, topic)
	assertWsSessionSendClosed(t, session)
}

func TestWsSessionCloseRejectsStaleOperations(t *testing.T) {
	service, session, topic := newWsSessionConcurrencyFixture("stale-operations")
	staleTargets := service.manager.FindDeliveries([]string{topic})
	if len(staleTargets) != 1 || staleTargets[0].Session != session {
		t.Fatalf("captured delivery targets = %v, want session %p", staleTargets, session)
	}

	service.closeSession(session)
	if staleTargets[0].Session.enqueue([]byte("stale delivery")) {
		t.Fatal("enqueue through stale delivery target after close = true, want false")
	}

	lateTopic := "user:late-subscribe"
	got := service.manager.Subscribe(session, []string{lateTopic})
	if _, exists := session.Topics[lateTopic]; exists {
		t.Fatal("late Subscribe added topic to session")
	}
	if service.manager.HasTopic(lateTopic) {
		t.Fatal("late Subscribe added session to topic registry")
	}
	service.manager.mu.RLock()
	_, registered := service.manager.sessions[session.ID]
	service.manager.mu.RUnlock()
	if registered {
		t.Fatal("late Subscribe re-registered managed session")
	}
	if len(got) != 0 {
		t.Fatalf("late Subscribe acknowledgments = %v, want none", got)
	}
}

func TestWsEmployeeReconnectSubscriptionStress(t *testing.T) {
	service := newWsServiceForTest()
	invalidator := service.lifecycle.(*employeeWsTestLifecycle)
	topic := service.notificationTopic(101)
	for round := 0; round < 32; round++ {
		session := &ClientSession{
			ID:        fmt.Sprintf("employee-reconnect-%02d", round),
			Role:      realtimeRoleNotification,
			Principal: &dto.AuthPrincipal{UserID: 101},
			Topics:    make(map[string]struct{}),
			Send:      make(chan []byte, realtimeSendBufferSize),
		}
		markActiveEmployeeTestSession(session)
		session.LoginSessionID = int64(9000 + round) // Each synthetic reconnect uses a fresh login session.
		service.manager.Register(session, service.defaultTopics(session))
		t.Cleanup(func() { service.manager.CloseSession(session) })

		for attempt := 0; attempt < 8; attempt++ {
			if added := service.subscribeTopics(session, []string{topic, topic}); len(added) != 0 {
				t.Fatalf("round %d duplicate subscribe %d added topics %v", round, attempt, added)
			}
		}
		if deliveries := service.manager.FindDeliveries([]string{topic}); len(deliveries) != 1 || deliveries[0].Session != session {
			t.Fatalf("round %d delivery registry = %v, want exactly this session", round, deliveries)
		}

		event := RealtimeEvent{
			EventID: fmt.Sprintf("reconnect-marker-%02d", round),
			Type:    enums.IMRealtimeEventNotificationCreated,
			Topic:   topic,
			Data: RealtimeNotificationCreatedPayload{Notification: response.NotificationResponse{
				ID: int64(round + 1), RecipientUserID: 101,
			}},
		}
		service.PublishToTopics([]string{topic}, event)
		if got := len(session.Send); got != 1 {
			t.Fatalf("round %d protected publication count = %d, want 1", round, got)
		}
		var published struct {
			EventID string `json:"eventId"`
			Type    string `json:"type"`
		}
		if err := json.Unmarshal(<-session.Send, &published); err != nil {
			t.Fatalf("round %d decode publication: %v", round, err)
		}
		if published.EventID != event.EventID || published.Type != event.Type {
			t.Fatalf("round %d publication = (%q, %q), want (%q, %q)", round, published.EventID, published.Type, event.EventID, event.Type)
		}

		if closed := invalidator.InvalidateLoginSession(session.LoginSessionID); closed != 1 {
			t.Fatalf("round %d disconnect closed %d sessions, want 1", round, closed)
		}
		assertWsSessionCleanup(t, service, session, topic)
		service.manager.mu.RLock()
		registeredSessions, topicBuckets := len(service.manager.sessions), len(service.manager.topics)
		service.manager.mu.RUnlock()
		if registeredSessions != 0 || topicBuckets != 0 {
			t.Fatalf("round %d retained registry state: sessions=%d topicBuckets=%d", round, registeredSessions, topicBuckets)
		}
	}
}

func TestWsSessionSubscribeBeforeCloseIsUnregistered(t *testing.T) {
	service := newWsServiceForTest()
	session := &ClientSession{
		ID:     "subscribe-before-close",
		Topics: make(map[string]struct{}),
		Send:   make(chan []byte, realtimeSendBufferSize),
	}
	service.manager.Register(session, nil)
	topic := "user:before-close"
	if got := service.manager.Subscribe(session, []string{topic}); len(got) != 1 || got[0] != topic {
		t.Fatalf("Subscribe acknowledgments = %v, want [%s]", got, topic)
	}

	service.closeSession(session)
	assertWsSessionCleanup(t, service, session, topic)
	if _, exists := session.Topics[topic]; exists {
		t.Fatal("session topic remains after close")
	}
}

func newWsSessionConcurrencyFixture(id string) (*wsService, *ClientSession, string) {
	service := newWsServiceForTest()
	session := &ClientSession{
		ID:     id,
		Topics: make(map[string]struct{}),
		Send:   make(chan []byte, realtimeSendBufferSize),
	}
	topic := "user:concurrency"
	service.manager.Register(session, []string{topic})
	return service, session, topic
}

func runWsSessionConcurrencyWorkers(t *testing.T, workers []func()) {
	t.Helper()
	start := make(chan struct{})
	outcomes := make(chan any, len(workers))
	for _, work := range workers {
		go func(work func()) {
			defer func() { outcomes <- recover() }()
			<-start
			work()
		}(work)
	}
	close(start)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for range workers {
		select {
		case panicValue := <-outcomes:
			if panicValue != nil {
				t.Errorf("session worker panicked: %v", panicValue)
			}
		case <-timer.C:
			t.Fatal("session workers did not complete within five seconds")
		}
	}
}

func assertWsSessionCleanup(t *testing.T, service *wsService, session *ClientSession, topic string) {
	t.Helper()
	if !session.Closed.Load() {
		t.Error("session remains open after closeSession")
	}
	service.manager.mu.RLock()
	_, registered := service.manager.sessions[session.ID]
	service.manager.mu.RUnlock()
	if registered {
		t.Error("session remains registered after closeSession")
	}
	if service.manager.HasTopic(topic) {
		t.Error("session topic remains registered after closeSession")
	}
}

func assertWsSessionSendClosed(t *testing.T, session *ClientSession) {
	t.Helper()
	for {
		select {
		case _, ok := <-session.Send:
			if !ok {
				return
			}
		default:
			t.Fatal("Send remains open after closeSession")
		}
	}
}
