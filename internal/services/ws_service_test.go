package services

// This file has been modified for the KestrelDesk project.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestWsNotificationTopic(t *testing.T) {
	svc := newWsServiceForTest()
	if got := svc.notificationTopic(123); got != "notification:123" {
		t.Fatalf("expected notification:123, got %q", got)
	}
}

func TestWsPendingMetadataRequired(t *testing.T) {
	for _, role := range []string{realtimeRoleAdmin, realtimeRoleNotification, realtimeRoleUser} {
		for _, mode := range []string{"no principal", "principal only", "client identity"} {
			t.Run(role+"/"+mode, func(t *testing.T) {
				svc := newWsServiceForTest()
				router := gin.New()
				router.GET("/ws", func(ctx *gin.Context) {
					var principal *dto.AuthPrincipal
					if mode != "no principal" {
						principal = &dto.AuthPrincipal{UserID: 101}
						ctx.Set(authPrincipalContextKey, principal)
					}
					_ = svc.upgradeConnection(ctx, principal, nil, role)
				})
				server := httptest.NewServer(router)
				defer server.Close()
				var header http.Header
				path := "/ws"
				if mode == "client identity" {
					header = http.Header{"LoginSessionID": []string{"1000101"}}
					path += "?loginSessionId=1000101"
				}
				peer, res, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+path, header)
				if peer != nil {
					defer peer.Close()
				}
				if err == nil || res == nil || res.StatusCode != http.StatusUnauthorized {
					t.Fatal("employee handshake without private metadata accepted")
				}
				if svc.manager.Count() != 0 {
					t.Fatal("untrusted employee entered manager")
				}
			})
		}
	}
}

func openPendingTestSocket(t *testing.T, svc *wsService, role string, beforeUpgrade func(*gin.Context)) (*websocket.Conn, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	router := gin.New()
	router.GET("/ws", func(ctx *gin.Context) {
		principal := employeeViewPrincipal()
		bindEmployeeWsTestSession(t, svc, ctx, principal)
		if beforeUpgrade != nil {
			beforeUpgrade(ctx)
		}
		done <- svc.upgradeConnection(ctx, AuthService.GetAuthPrincipal(ctx), nil, role)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	peer, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	return peer, done
}

func pendingTestSession(t *testing.T, svc *wsService) *ClientSession {
	t.Helper()
	svc.manager.mu.RLock()
	defer svc.manager.mu.RUnlock()
	if len(svc.manager.sessions) != 1 {
		t.Fatalf("manager session count = %d", len(svc.manager.sessions))
	}
	for _, session := range svc.manager.sessions {
		return session
	}
	return nil
}

func assertPendingConfidentiality(t *testing.T, svc *wsService, session *ClientSession) {
	t.Helper()
	for _, topic := range []string{svc.adminTopic(session.EmployeeID), "admin:all", "conversation:42", svc.notificationTopic(session.EmployeeID), svc.userTopic(session.EmployeeID)} {
		svc.PublishToTopic(topic, RealtimeEvent{EventID: "message-marker", Type: enums.IMRealtimeEventMessageCreated, Data: RealtimeMessageCreatedPayload{ConversationID: 42, Content: "protected-message-marker"}})
		svc.PublishToTopic(topic, RealtimeEvent{EventID: "queue-marker", Type: enums.IMRealtimeEventConversationUpdated, Data: RealtimeConversationChangedPayload{ConversationID: 42, LastMessageSummary: "protected-queue-marker"}})
		svc.PublishNotificationCreated(session.EmployeeID, response.NotificationResponse{ID: 1})
	}
	svc.manager.mu.RLock()
	phase, protectedTopics := employeeLifecyclePhase(session.employeePhase.Load()), len(session.Topics)
	svc.manager.mu.RUnlock()
	if phase != employeePhasePending || protectedTopics != 0 || len(session.Send) != 0 {
		t.Fatal("protected data or connected output escaped while pending")
	}
	if got := svc.subscribeTopics(session, []string{"conversation:42"}); len(got) != 0 {
		t.Fatal("pending subscription admitted")
	}
}

func TestWsPendingConfidentiality(t *testing.T) {
	for _, role := range []string{realtimeRoleAdmin, realtimeRoleNotification, realtimeRoleUser} {
		t.Run(role, func(t *testing.T) {
			svc := newWsServiceForTest()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			svc.lifecycle.(*employeeWsTestLifecycle).beforeRead = func(int64) { close(entered); <-release }
			_, done := openPendingTestSocket(t, svc, role, nil)
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("authoritative revalidation never invoked; legacy immediate registration escaped")
			}
			session := pendingTestSession(t, svc)
			assertPendingConfidentiality(t, svc, session)
			svc.closeSession(session)
			unblock()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("closed pending upgrade accepted")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("closed upgrade did not finish")
			}
		})
	}
}

type expireDuringMarshal struct {
	session   *ClientSession
	marshaled *bool
}

func (expireDuringMarshal) realtimeEventPayload() {}

func (v expireDuringMarshal) MarshalJSON() ([]byte, error) {
	if v.marshaled != nil {
		*v.marshaled = true
	}
	v.session.LifecycleDeadline = time.Now()
	return []byte(`{"marker":"protected"}`), nil
}

func TestEmployeeDeliveryDeadlineRecheck(t *testing.T) {
	for _, role := range []string{realtimeRoleAdmin, realtimeRoleNotification, realtimeRoleUser} {
		for _, mode := range []string{"new", "pending", "expired", "closed active", "expires during marshal"} {
			if role == realtimeRoleAdmin && mode == "expires during marshal" {
				continue
			} // Admin payload policy requires concrete DTOs; other roles fence the shared enqueue check.
			if role == realtimeRoleNotification && mode == "expires during marshal" {
				continue
			} // Ticket notification delivery rejects untyped payloads before JSON marshaling; send-lock expiry remains covered separately.
			t.Run(role+"/"+mode, func(t *testing.T) {
				svc := newWsServiceForTest()
				session := &ClientSession{ID: "delivery", Role: role, Principal: employeeViewPrincipal(), Topics: map[string]struct{}{}, Send: make(chan []byte, 64)}
				markActiveEmployeeTestSession(session)
				topic, eventType := "conversation:42", enums.IMRealtimeEventMessageCreated
				if role == realtimeRoleNotification {
					topic, eventType = "notification:101", enums.IMRealtimeEventNotificationCreated
				}
				if role == realtimeRoleUser {
					topic = "user:101"
				}
				svc.manager.Register(session, []string{topic})
				switch mode {
				case "new":
					session.employeePhase.Store(uint32(employeePhaseNew))
				case "pending":
					session.employeePhase.Store(uint32(employeePhasePending))
				case "expired":
					session.LifecycleDeadline = time.Now()
				case "closed active":
					session.Closed.Store(true)
				}
				event := RealtimeEvent{Type: eventType, Data: RealtimeMessageCreatedPayload{Content: "protected"}}
				var marshaled bool
				if role != realtimeRoleAdmin && (mode == "new" || mode == "pending") {
					event.Data = expireDuringMarshal{session: session, marshaled: &marshaled}
				}
				if mode == "expires during marshal" {
					event.Data = expireDuringMarshal{session: session}
				}
				svc.PublishToTopic(topic, event)
				if marshaled {
					t.Fatal("inactive employee payload selected and serialized")
				}
				if len(session.Send) != 0 {
					t.Fatal("protected delivery escaped inactive, closed, or expired employee")
				}
				if strings.Contains(mode, "expire") && !session.Closed.Load() {
					t.Fatal("expired delivery did not close employee")
				}
				svc.closeSession(session)
			})
		}
	}
}

func TestEmployeeDeliverySubscriptionGuard(t *testing.T) {
	for _, mode := range []string{"new", "pending", "expired", "closed active"} {
		t.Run(mode, func(t *testing.T) {
			svc := newWsServiceForTest()
			session := &ClientSession{ID: "subscription", Role: realtimeRoleAdmin, Principal: employeeViewPrincipal(), Topics: make(map[string]struct{}), Send: make(chan []byte, 64)}
			markActiveEmployeeTestSession(session)
			svc.manager.Register(session, nil)
			switch mode {
			case "new":
				session.employeePhase.Store(uint32(employeePhaseNew))
			case "pending":
				session.employeePhase.Store(uint32(employeePhasePending))
			case "expired":
				session.LifecycleDeadline = time.Now()
			case "closed active":
				session.Closed.Store(true)
			}
			if len(svc.subscribeTopics(session, []string{"conversation:42"})) != 0 || svc.manager.HasTopic("conversation:42") {
				t.Fatal("inactive, closed, or expired employee subscription admitted")
			}
			svc.closeSession(session)
		})
	}
}

func TestEmployeeTransportRoleCoverage(t *testing.T) {
	for _, role := range []string{realtimeRoleAdmin, realtimeRoleNotification, realtimeRoleUser} {
		t.Run(role, func(t *testing.T) {
			svc := newWsServiceForTest()
			peer, done := openPendingTestSocket(t, svc, role, nil)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("upgrade incomplete")
			}
			_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, _, err := peer.ReadMessage(); err != nil {
				t.Fatal(err)
			}
			session := pendingTestSession(t, svc)
			if employeeLifecyclePhase(session.employeePhase.Load()) != employeePhaseActive || session.LoginSessionID != 1000101 || session.LifecycleDeadline.IsZero() {
				t.Fatal("transport bypassed activation")
			}
			svc.closeSession(session)
		})
	}
	for _, source := range []enums.ExternalSource{enums.ExternalSourceGuest, enums.ExternalSourceUser} {
		t.Run("customer/"+string(source), func(t *testing.T) {
			svc := newWsServiceForTest()
			svc.lifecycle.(*employeeWsTestLifecycle).beforeRead = func(int64) { t.Error("customer entered employee revalidation") }
			router := gin.New()
			done := make(chan error, 1)
			router.GET("/ws", func(ctx *gin.Context) {
				done <- svc.upgradeConnection(ctx, nil, &openidentity.ExternalUser{ExternalSource: source, ExternalID: "synthetic-guest"}, realtimeRoleUser, &CustomerSessionVerifyResult{CustomerID: 202})
			})
			server := httptest.NewServer(router)
			defer server.Close()
			peer, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("customer upgrade incomplete")
			}
			session := pendingTestSession(t, svc)
			if session.EmployeeID != 0 || !session.LifecycleDeadline.IsZero() || employeeLifecyclePhase(session.employeePhase.Load()) != employeePhaseNew || !svc.manager.HasTopic("customer:202") {
				t.Fatal("customer lifecycle changed")
			}
			svc.closeSession(session)
		})
	}
}

func TestWsPendingRevalidationRejectsInvalidSnapshot(t *testing.T) {
	for _, mode := range []string{"read failure", "employee mismatch", "login mismatch", "principal mismatch", "missing principal", "expired", "authz deadline reached"} {
		t.Run(mode, func(t *testing.T) {
			svc := newWsServiceForTest()
			lifecycle := svc.lifecycle.(*employeeWsTestLifecycle)
			lifecycle.readCurrent = func(id int64, now time.Time) (EmployeeSessionSnapshot, error) {
				snapshot := EmployeeSessionSnapshot{EmployeeID: 101, LoginSessionID: id, LoginSessionExpiresAt: now.Add(time.Hour), Principal: employeeViewPrincipal()}
				switch mode {
				case "read failure":
					return EmployeeSessionSnapshot{}, fmt.Errorf("synthetic read failure")
				case "employee mismatch":
					snapshot.EmployeeID = 202
				case "login mismatch":
					snapshot.LoginSessionID++
				case "principal mismatch":
					snapshot.Principal = &dto.AuthPrincipal{UserID: 202}
				case "missing principal":
					snapshot.Principal = nil
				case "expired":
					snapshot.LoginSessionExpiresAt = now
				case "authz deadline reached":
					snapshot.NextAuthzChangeAt = &now
				}
				return snapshot, nil
			}
			peer, done := openPendingTestSocket(t, svc, realtimeRoleAdmin, nil)
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("invalid authoritative snapshot accepted")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("invalid upgrade incomplete")
			}
			if svc.manager.Count() != 0 || svc.manager.HasTopic("admin:all") {
				t.Fatal("invalid snapshot left protected membership")
			}
			_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, body, err := peer.ReadMessage(); err == nil {
				t.Fatalf("invalid snapshot produced frame %s", body)
			}
		})
	}
}

func TestWsPendingUsesFreshAuthorization(t *testing.T) {
	svc := newWsServiceForTest()
	svc.lifecycle.(*employeeWsTestLifecycle).readCurrent = func(id int64, now time.Time) (EmployeeSessionSnapshot, error) {
		return EmployeeSessionSnapshot{EmployeeID: 101, LoginSessionID: id, LoginSessionExpiresAt: now.Add(time.Hour), Principal: &dto.AuthPrincipal{UserID: 101}}, nil
	}
	peer, done := openPendingTestSocket(t, svc, realtimeRoleAdmin, nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upgrade incomplete")
	}
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, body, err := peer.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("admin:all")) {
		t.Fatal("initial view permission survived fresh reduction")
	}
	session := pendingTestSession(t, svc)
	if len(session.Principal.Permissions) != 0 || len(svc.subscribeTopics(session, []string{"conversation:42"})) != 0 {
		t.Fatal("activation retained stale principal")
	}
	svc.closeSession(session)
}

func TestWsEmployeeSessionRevocationFromAnotherProcessClosesSocket(t *testing.T) {
	svc := newWsServiceForTest()
	svc.employeeSessionRevalidationGap = 10 * time.Millisecond
	lifecycle := svc.lifecycle.(*employeeWsTestLifecycle)
	revoked := false
	lifecycle.readCurrent = func(id int64, now time.Time) (EmployeeSessionSnapshot, error) {
		lifecycle.mu.Lock()
		defer lifecycle.mu.Unlock()
		if revoked {
			return EmployeeSessionSnapshot{}, fmt.Errorf("synthetic session revoked")
		}
		snapshot, ok := lifecycle.snapshots[id]
		if !ok {
			return EmployeeSessionSnapshot{}, fmt.Errorf("synthetic session missing")
		}
		return snapshot, nil
	}
	peer, done := openPendingTestSocket(t, svc, realtimeRoleAdmin, nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("employee websocket upgrade did not finish")
	}
	if _, _, err := peer.ReadMessage(); err != nil {
		t.Fatalf("read initial connected event: %v", err)
	}
	session := pendingTestSession(t, svc)
	lifecycle.mu.Lock()
	revoked = true
	lifecycle.mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !session.Closed.Load() {
		time.Sleep(5 * time.Millisecond)
	}
	if !session.Closed.Load() || svc.manager.Count() != 0 {
		t.Fatal("employee websocket remained active after authoritative session revocation")
	}
}

func TestEmployeeConnectedPayloadKeepsWireContract(t *testing.T) {
	svc := newWsServiceForTest()
	peer, done := openPendingTestSocket(t, svc, realtimeRoleAdmin, nil)
	<-done
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, body, err := peer.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	tokenMarker := []byte("synthetic-secret-token")
	if bytes.Contains(body, []byte("loginSessionId")) || bytes.Contains(body, tokenMarker) || bytes.Contains(body, []byte("ExpiresAt")) || bytes.Contains(body, []byte("LifecycleDeadline")) {
		t.Fatal("internal authentication metadata leaked")
	}
	var event struct {
		Type string                     `json:"type"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != enums.IMRealtimeEventConnected || len(event.Data) != 5 {
		t.Fatalf("connected wire shape changed: %s", body)
	}
	for _, key := range []string{"connId", "userId", "role", "terminalType", "topics"} {
		if _, ok := event.Data[key]; !ok {
			t.Fatalf("connected omitted %s", key)
		}
	}
	svc.closeSession(pendingTestSession(t, svc))
}

func TestWsNotificationCreatedEventType(t *testing.T) {
	event := RealtimeNotificationCreatedEvent{
		Payload: RealtimeNotificationCreatedPayload{
			Notification: response.NotificationResponse{ID: 1},
		},
	}
	if got := event.EventType(); got != "notification.created" {
		t.Fatalf("expected notification.created, got %q", got)
	}
	if payload := event.EventPayload(); payload == nil {
		t.Fatalf("expected payload")
	}
}
