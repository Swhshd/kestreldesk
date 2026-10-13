package services

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

func TestCustomerDefaultTopicsUseVerifiedCustomerID(t *testing.T) {
	svc := newWsServiceForTest()
	external := &openidentity.ExternalUser{ExternalID: "collision-id"}
	for _, tc := range []struct {
		name    string
		session *ClientSession
		want    []string
	}{
		{"verified customer", &ClientSession{Role: realtimeRoleUser, CustomerID: 41, External: external}, []string{"customer:41"}},
		{"missing customer", &ClientSession{Role: realtimeRoleUser, External: external}, nil},
		{"zero customer", &ClientSession{Role: realtimeRoleUser, CustomerID: 0, External: external}, nil},
		{"negative customer", &ClientSession{Role: realtimeRoleUser, CustomerID: -1, External: external}, nil},
		{"employee user", &ClientSession{Role: realtimeRoleUser, Principal: &dto.AuthPrincipal{UserID: 101}}, []string{"user:101"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := svc.defaultTopics(tc.session); !slices.Equal(got, tc.want) {
				t.Fatalf("default topics = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCustomerRealtimeSubscriptionAdmission(t *testing.T) {
	for _, topic := range []string{"customer:41", "customer:42", "customer:", "customer:0", "customer:-1", "customer:not-a-number"} {
		t.Run(topic, func(t *testing.T) {
			svc := newWsServiceForTest()
			conn := openCustomerIdentityTestSocket(t, svc, 41, &openidentity.ExternalUser{ExternalID: "collision-id"})
			readCustomerIdentityTestEvent(t, conn, enums.IMRealtimeEventConnected)
			if err := conn.WriteJSON(map[string]any{"type": "subscribe", "topics": []string{topic, topic}}); err != nil {
				t.Fatal(err)
			}
			// Pong fences subscribe processing; an unexpected subscribed frame fails immediately.
			customerIdentityTestPing(t, conn)
			readCustomerIdentityTestEvent(t, conn, enums.IMRealtimeEventPong)
			members := svc.manager.FindByTopics([]string{topic})
			if topic == "customer:41" {
				if len(members) != 1 || members[0].CustomerID != 41 {
					t.Fatal("own customer topic must remain registered exactly once")
				}
				if got := svc.filterAllowedTopics(members[0], []string{"customer:41"}); !slices.Equal(got, []string{"customer:41"}) {
					t.Fatalf("own topic admission = %v, want [customer:41]", got)
				}
				// Repeating an admitted default topic cannot produce another acknowledgement.
				if err := conn.WriteJSON(map[string]any{"type": "subscribe", "topics": []string{topic}}); err != nil {
					t.Fatal(err)
				}
				customerIdentityTestPing(t, conn)
				readCustomerIdentityTestEvent(t, conn, enums.IMRealtimeEventPong)
			} else if len(members) != 0 {
				t.Fatalf("rejected topic registered %d sessions", len(members))
			}
			svc.PublishToTopic(topic, svc.newEvent(topic, RealtimeResyncRequiredEvent{Payload: RealtimeResyncRequiredPayload{Reason: "synthetic-subscription-marker"}}))
			if topic == "customer:41" {
				event := readCustomerIdentityTestEvent(t, conn, enums.IMRealtimeEventResyncRequired)
				if event.Data["reason"] != "synthetic-subscription-marker" {
					t.Fatal("own marker lost")
				}
			}
			// Publication enqueues synchronously before this ping: pong proves no forbidden marker.
			customerIdentityTestPing(t, conn)
			readCustomerIdentityTestEvent(t, conn, enums.IMRealtimeEventPong)
		})
	}
}

func TestCustomerConversationSubscriptionOwnership(t *testing.T) {
	db := openHumanDispatchRealtimeTestDB(t, false)
	for _, conversation := range []models.Conversation{{ID: 501, CustomerID: 41}, {ID: 502, CustomerID: 42}} {
		if err := db.Create(&conversation).Error; err != nil {
			t.Fatal(err)
		}
	}
	var identityLookups atomic.Int64
	if err := db.Callback().Query().Before("gorm:query").Register("test:customer_subscription_identity_lookup", func(tx *gorm.DB) {
		if tx.Statement.Table == "t_customer_identity" {
			identityLookups.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name           string
		customerID     int64
		conversationID int64
		topic          string
		allowed        bool
	}{
		{"A owns A", 41, 501, "conversation:501", true},
		{"B owns B", 42, 502, "conversation:502", true},
		{"A cannot own B", 41, 502, "conversation:502", false},
		{"B cannot own A", 42, 501, "conversation:501", false},
		{"zero cannot own A", 0, 501, "conversation:501", false},
		{"negative cannot own A", -1, 501, "conversation:501", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newWsServiceForTest()
			// No identity mappings exist: external source/ID cannot provide authority.
			external := &openidentity.ExternalUser{ExternalID: "synthetic-unmapped-owner", ExternalSource: enums.ExternalSourceGuest}
			if got := svc.canSubscribeConversation(&ClientSession{Role: realtimeRoleUser, CustomerID: tc.customerID, External: external}, tc.conversationID); got != tc.allowed {
				t.Errorf("conversation admission = %v, want %v", got, tc.allowed)
			}
			conn := openCustomerIdentityTestSocket(t, svc, tc.customerID, external)
			readCustomerIdentityTestEvent(t, conn, enums.IMRealtimeEventConnected)
			if err := conn.WriteJSON(map[string]any{"type": "subscribe", "topics": []string{tc.topic}}); err != nil {
				t.Fatal(err)
			}
			if tc.allowed {
				ack := readCustomerIdentityTestEvent(t, conn, enums.IMRealtimeEventSubscribed)
				if topics, ok := ack.Data["topics"].([]any); !ok || len(topics) != 1 || topics[0] != tc.topic {
					t.Fatal("owner acknowledgement lost topic")
				}
			}
			customerIdentityTestPing(t, conn)
			readCustomerIdentityTestEvent(t, conn, enums.IMRealtimeEventPong)
			members := svc.manager.FindByTopics([]string{tc.topic})
			if tc.allowed && (len(members) != 1 || members[0].CustomerID != tc.customerID) || !tc.allowed && len(members) != 0 {
				t.Fatal("conversation registry violates verified ownership")
			}
		})
	}
	if got := identityLookups.Load(); got != 0 {
		t.Fatalf("subscription made %d source/ID owner re-lookups", got)
	}
}

func captureCustomerRealtimeSession(t *testing.T, svc *wsService, id string, customerID int64, external *openidentity.ExternalUser) *ClientSession {
	t.Helper()
	session := &ClientSession{ID: id, Role: realtimeRoleUser, CustomerID: customerID, External: external, Topics: make(map[string]struct{}), Send: make(chan []byte, realtimeSendBufferSize)}
	svc.manager.Register(session, svc.defaultTopics(session))
	t.Cleanup(func() { svc.manager.Unregister(session) })
	return session
}

func openCustomerIdentityTestSocket(t *testing.T, svc *wsService, customerID int64, external *openidentity.ExternalUser) *websocket.Conn {
	t.Helper()
	router := gin.New()
	router.GET("/ws", func(ctx *gin.Context) {
		if err := svc.upgradeConnection(ctx, nil, external, realtimeRoleUser, &CustomerSessionVerifyResult{CustomerID: customerID}); err != nil {
			t.Errorf("upgrade: %v", err)
		}
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("customer handshake: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func readCustomerIdentityTestEvent(t *testing.T, conn *websocket.Conn, want string) capturedRealtimeEvent {
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

func customerIdentityTestPing(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	if err := conn.WriteJSON(map[string]any{"type": "ping"}); err != nil {
		t.Fatal(err)
	}
}
