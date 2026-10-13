package services

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func TestDashboardRealtimeDefaultTopics(t *testing.T) {
	svc := newWsServiceForTest()
	for _, tc := range []struct {
		name    string
		session *ClientSession
		want    []string
	}{
		{"nil session", nil, nil},
		{"admin missing principal", &ClientSession{Role: realtimeRoleAdmin}, nil},
		{"admin zero user", &ClientSession{Role: realtimeRoleAdmin, Principal: &dto.AuthPrincipal{}}, nil},
		{"admin no view", &ClientSession{Role: realtimeRoleAdmin, Principal: &dto.AuthPrincipal{UserID: 101}}, []string{"admin:101"}},
		{"admin with view", &ClientSession{Role: realtimeRoleAdmin, Principal: &dto.AuthPrincipal{UserID: 101, Permissions: []string{constants.PermissionConversationView.Code}}}, []string{"admin:101", "admin:all"}},
		{"notification", &ClientSession{Role: realtimeRoleNotification, Principal: &dto.AuthPrincipal{UserID: 101}}, []string{"notification:101"}},
		{"user", &ClientSession{Role: realtimeRoleUser, Principal: &dto.AuthPrincipal{UserID: 101}}, []string{"user:101"}},
		{"guest", &ClientSession{Role: realtimeRoleUser, CustomerID: 101, External: &openidentity.ExternalUser{ExternalID: "guest-101"}}, []string{"customer:101"}},
		{"external missing customer", &ClientSession{Role: realtimeRoleUser, External: &openidentity.ExternalUser{ExternalID: "guest-101"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := svc.defaultTopics(tc.session)
			if isEmployeeRealtimeSession(tc.session) {
				got = svc.employeeDefaultTopics(tc.session, EmployeeSessionSnapshot{Principal: tc.session.Principal})
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("default topics = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEmployeeRealtimeEventClassification(t *testing.T) {
	for _, tc := range []struct {
		eventType string
		want      employeeRealtimeEventClass
	}{
		{enums.IMRealtimeEventConnected, employeeEventControl},
		{enums.IMRealtimeEventPong, employeeEventControl},
		{enums.IMRealtimeEventSubscribed, employeeEventControl},
		{enums.IMRealtimeEventUnsubscribed, employeeEventControl},
		{enums.IMRealtimeEventConversationCreated, employeeEventConversationChanged},
		{enums.IMRealtimeEventConversationUpdated, employeeEventConversationChanged},
		{enums.IMRealtimeEventConversationAssigned, employeeEventConversationChanged},
		{enums.IMRealtimeEventConversationTransferred, employeeEventConversationChanged},
		{enums.IMRealtimeEventConversationClosed, employeeEventConversationChanged},
		{enums.IMRealtimeEventConversationRead, employeeEventConversationChanged},
		{enums.IMRealtimeEventMessageCreated, employeeEventMessageCreated},
		{enums.IMRealtimeEventMessageRecalled, employeeEventMessageRecalled},
		{enums.IMRealtimeEventResyncRequired, employeeEventResync},
		{"conversation.future", employeeEventUnknown},
		{"message.future", employeeEventUnknown},
		{enums.IMRealtimeEventNotificationCreated, employeeEventUnknown},
		{enums.IMRealtimeEventCustomerSessionRefresh, employeeEventUnknown},
		{"", employeeEventUnknown},
	} {
		t.Run(tc.eventType, func(t *testing.T) {
			if got := classifyEmployeeRealtimeEvent(tc.eventType); got != tc.want {
				t.Fatalf("event class = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEmployeeRealtimeDeliveryPolicy(t *testing.T) {
	_, svc, principal := employeeRealtimeScopeFixture(t)
	withView := &ClientSession{Role: realtimeRoleAdmin, EmployeeID: 101, Principal: principal}
	noViewPrincipal := *principal
	noViewPrincipal.Permissions = nil
	noView := &ClientSession{Role: realtimeRoleAdmin, EmployeeID: 101, Principal: &noViewPrincipal}
	for _, tc := range []struct {
		name    string
		session *ClientSession
		hasView bool
	}{
		{"nil", nil, false},
		{"missing principal", &ClientSession{Role: realtimeRoleAdmin}, false},
		{"zero user", &ClientSession{Role: realtimeRoleAdmin, Principal: &dto.AuthPrincipal{Permissions: []string{constants.PermissionConversationView.Code}}}, false},
		{"negative user", &ClientSession{Role: realtimeRoleAdmin, Principal: &dto.AuthPrincipal{UserID: -1, Permissions: []string{constants.PermissionConversationView.Code}}}, false},
		{"wrong role", &ClientSession{Role: realtimeRoleUser, Principal: withView.Principal}, false},
		{"no view", noView, false},
		{"with view", withView, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := employeeHasConversationView(tc.session); got != tc.hasView {
				t.Fatalf("conversation view = %v, want %v", got, tc.hasView)
			}
			for _, class := range []employeeRealtimeEventClass{employeeEventConversationChanged, employeeEventMessageCreated, employeeEventMessageRecalled, employeeEventResync} {
				for _, topic := range []string{"admin:101", "admin:all", "conversation:42"} {
					if got := svc.CanReceiveEvent(tc.session, topic, class); got != tc.hasView {
						t.Fatalf("receipt on %q class %v = %v, want %v", topic, class, got, tc.hasView)
					}
				}
			}
			if tc.session == nil || tc.session.Principal == nil || tc.session.Principal.UserID <= 0 || tc.session.Role != realtimeRoleAdmin {
				if svc.CanSubscribeTopic(tc.session, "admin:101") || svc.CanReceiveEvent(tc.session, "admin:101", employeeEventControl) {
					t.Fatal("invalid employee identity admitted")
				}
			}
		})
	}
	for _, topic := range []string{"admin:202", "user:101", "guest:101", "notification:101", "unknown", "", "conversation:", "conversation:0", "conversation:-1", "conversation:abc"} {
		if svc.CanSubscribeTopic(withView, topic) {
			t.Errorf("unknown or unauthorized destination admitted: %q", topic)
		}
		if svc.CanReceiveEvent(withView, topic, employeeEventMessageCreated) {
			t.Errorf("unknown or unauthorized delivery allowed: %q", topic)
		}
	}
	for _, topic := range []string{"admin:101", "admin:all", "conversation:42"} {
		if !svc.CanSubscribeTopic(withView, topic) || !svc.CanReceiveEvent(withView, topic, employeeEventControl) {
			t.Errorf("valid destination rejected: %q", topic)
		}
		for _, class := range []employeeRealtimeEventClass{employeeEventUnknown, employeeRealtimeEventClass(255)} {
			if svc.CanReceiveEvent(withView, topic, class) || svc.employeeDeliveryAudience(withView, topic, class) != employeeAudienceDenied {
				t.Errorf("unknown event class allowed on %q", topic)
			}
		}
	}
	if !svc.CanSubscribeTopic(noView, "admin:101") || !svc.CanReceiveEvent(noView, "admin:101", employeeEventControl) {
		t.Fatal("no-view own personal control destination rejected")
	}
	for _, topic := range []string{"admin:all", "conversation:42"} {
		if svc.CanSubscribeTopic(noView, topic) || svc.CanReceiveEvent(noView, topic, employeeEventControl) {
			t.Errorf("no-view protected destination admitted: %q", topic)
		}
	}
	for _, principal := range []*dto.AuthPrincipal{nil, {UserID: 101}} {
		isolated := newWsServiceForTest()
		session := captureEmployeeRealtimeSession(t, isolated, "unauthorized", principal, "admin:all", "conversation:42")
		deliveries := isolated.manager.FindDeliveries([]string{"admin:all", "conversation:42"})
		if len(deliveries) != 2 {
			t.Fatalf("bypass fixture delivery pairs = %d, want 2", len(deliveries))
		}
		for _, delivery := range deliveries {
			if delivery.Session != session || svc.CanReceiveEvent(delivery.Session, delivery.DeliveryTopic, employeeEventMessageCreated) {
				t.Fatal("registry membership bypassed receipt authorization")
			}
		}
		requireNoCapturedRealtimeEvent(t, session)
	}
}

func TestEmployeeRealtimePolicyIgnoresEnvelopeTopic(t *testing.T) {
	_, svc, principal := employeeRealtimeScopeFixture(t)
	withView := &ClientSession{Role: realtimeRoleAdmin, EmployeeID: 101, Principal: principal}
	noViewPrincipal := *principal
	noViewPrincipal.Permissions = nil
	noView := &ClientSession{Role: realtimeRoleAdmin, EmployeeID: 101, Principal: &noViewPrincipal}
	event := RealtimeEvent{Type: enums.IMRealtimeEventMessageCreated, Topic: "conversation:42"}
	class := classifyEmployeeRealtimeEvent(event.Type)
	for _, tc := range []struct {
		deliveryTopic string
		want          employeeRealtimeAudience
	}{
		{"admin:all", employeeAudienceQueue}, {"admin:101", employeeAudienceQueue}, {"conversation:42", employeeAudienceFull}, {"admin:202", employeeAudienceDenied},
	} {
		if got := svc.employeeDeliveryAudience(withView, tc.deliveryTopic, class); got != tc.want {
			t.Fatalf("audience for delivery %q, envelope %q = %v, want %v", tc.deliveryTopic, event.Topic, got, tc.want)
		}
		if got := svc.employeeDeliveryAudience(noView, tc.deliveryTopic, class); got != employeeAudienceDenied {
			t.Fatalf("no-view audience = %v, want denied", got)
		}
	}
}

func TestWsConnectionManagerFindDeliveries(t *testing.T) {
	svc := newWsServiceForTest()
	session := captureEmployeeRealtimeSession(t, svc, "two-destinations", &dto.AuthPrincipal{UserID: 101}, "admin:101", "conversation:42")
	deliveries := svc.manager.FindDeliveries([]string{"admin:101", "conversation:42", "missing"})
	if len(deliveries) != 2 {
		t.Fatalf("delivery pairs = %d, want 2", len(deliveries))
	}
	seen := map[string]bool{}
	for _, delivery := range deliveries {
		if delivery.Session != session || seen[delivery.DeliveryTopic] {
			t.Fatal("duplicate or incorrect session/destination pair")
		}
		seen[delivery.DeliveryTopic] = true
	}
	if !seen["admin:101"] || !seen["conversation:42"] {
		t.Fatalf("lost delivery identity: %v", seen)
	}
	svc.manager.Unsubscribe(session, []string{"conversation:42"}, nil)
	if got := svc.manager.FindDeliveries([]string{"conversation:42"}); len(got) != 0 {
		t.Fatal("unsubscribed destination retained")
	}
	svc.manager.Unregister(session)
	if got := svc.manager.FindDeliveries([]string{"admin:101"}); len(got) != 0 {
		t.Fatal("unregistered session retained")
	}
}

func TestDashboardWSControlAndSubscription(t *testing.T) {
	for _, withView := range []bool{false, true} {
		name := "no view"
		wantTopics := []string{"admin:101"}
		if withView {
			name = "with view"
			wantTopics = append(wantTopics, "admin:all")
		}
		t.Run(name, func(t *testing.T) {
			_, svc, principal := employeeRealtimeScopeFixture(t)
			if !withView {
				principal.Permissions = nil
			}
			router := gin.New()
			router.GET("/ws", func(ctx *gin.Context) { bindEmployeeWsTestSession(t, svc, ctx, principal); svc.HandleDashboardWS(ctx) })
			server := httptest.NewServer(router)
			defer server.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
			if err != nil {
				t.Fatalf("dashboard handshake: %v", err)
			}
			defer conn.Close()
			readEvent := func(want string) capturedRealtimeEvent {
				t.Helper()
				if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
					t.Fatal(err)
				}
				_, body, err := conn.ReadMessage()
				if err != nil {
					t.Fatalf("read %q: %v", want, err)
				}
				var event capturedRealtimeEvent
				if err := json.Unmarshal(body, &event); err != nil {
					t.Fatal(err)
				}
				if event.Type != want {
					t.Fatalf("frame type = %q, want %q", event.Type, want)
				}
				return event
			}
			connected := readEvent(enums.IMRealtimeEventConnected)
			topics, ok := connected.Data["topics"].([]any)
			if !ok {
				t.Fatal("connected frame omitted topics")
			}
			var gotTopics []string
			for _, topic := range topics {
				gotTopics = append(gotTopics, topic.(string))
			}
			slices.Sort(gotTopics)
			slices.Sort(wantTopics)
			if !slices.Equal(gotTopics, wantTopics) {
				t.Fatalf("connected topics = %v, want %v", gotTopics, wantTopics)
			}
			if err := conn.WriteJSON(map[string]any{"type": "ping"}); err != nil {
				t.Fatal(err)
			}
			readEvent(enums.IMRealtimeEventPong)
			if err := conn.WriteJSON(map[string]any{"type": "subscribe", "topics": []string{"conversation:42"}}); err != nil {
				t.Fatal(err)
			}
			if withView {
				ack := readEvent(enums.IMRealtimeEventSubscribed)
				if topics, ok := ack.Data["topics"].([]any); !ok || len(topics) != 1 || topics[0] != "conversation:42" {
					t.Fatal("subscription acknowledgement omitted admitted topic")
				}
			}
			// The following pong fences processing of subscribe without a timeout
			// being used as evidence that a forbidden acknowledgement was absent.
			if err := conn.WriteJSON(map[string]any{"type": "ping"}); err != nil {
				t.Fatal(err)
			}
			readEvent(enums.IMRealtimeEventPong)
			got := svc.manager.FindByTopics([]string{"conversation:42"})
			if withView && len(got) != 1 || !withView && len(got) != 0 {
				t.Fatalf("conversation registration count = %d, withView=%v", len(got), withView)
			}
		})
	}
}

func TestDashboardRealtimeSubscriptionAdmission(t *testing.T) {
	for _, withView := range []bool{false, true} {
		name := "no view"
		want := []string{"admin:101"}
		if withView {
			name = "with view"
			want = []string{"conversation:42", "admin:101", "admin:all"}
		}
		t.Run(name, func(t *testing.T) {
			_, _, principal := employeeRealtimeScopeFixture(t)
			if !withView {
				principal.Permissions = nil
			}
			svc := newWsServiceForTest()
			snapshot := EmployeeSessionSnapshot{EmployeeID: 101, LoginSessionID: 1000101, LoginSessionExpiresAt: time.Now().Add(time.Hour), Principal: principal}
			session := &ClientSession{ID: name, Role: realtimeRoleAdmin, Principal: principal, EmployeeID: 101, LoginSessionID: 1000101, Topics: make(map[string]struct{}), Send: make(chan []byte, realtimeSendBufferSize)}
			if !svc.manager.RegisterPending(session) {
				t.Fatal("pending fixture registration failed")
			}
			if !svc.lifecycle.InstallLifecycleTimer(session, employeeLifecycleDeadline(snapshot)) {
				t.Fatal("fixture lifecycle timer failed")
			}
			if _, activated := svc.manager.Activate(session, snapshot, nil); !activated {
				t.Fatal("fixture activation failed")
			}
			t.Cleanup(func() { svc.closeSession(session) })
			// The existing explicit-conversation rejection must remain effective.
			got := svc.filterAllowedTopics(session, []string{"conversation:42"})
			if withView && !slices.Equal(got, []string{"conversation:42"}) {
				t.Fatalf("current scope did not admit its authorized conversation: %v", got)
			}
			if !withView && len(got) != 0 {
				t.Fatalf("unauthorized explicit conversation admitted: %v", got)
			}
			t.Log("explicit conversation permission behavior passed")
			topics := []string{" conversation:42 ", "conversation:42", " admin:101 ", "admin:all", "admin:202", "unknown", "conversation:", "conversation:0", "conversation:-1", "conversation:abc", " "}
			if got := svc.subscribeTopics(session, topics); !slices.Equal(got, want) {
				t.Fatalf("registered topics = %v, want %v", got, want)
			}
			if got := svc.subscribeTopics(session, topics); len(got) != 0 {
				t.Fatalf("duplicate subscriptions acknowledged: %v", got)
			}
			if got := svc.manager.FindByTopics([]string{"admin:202", "unknown", "conversation:0"}); len(got) != 0 {
				t.Fatal("rejected topics reached the registry")
			}
		})
	}
}
