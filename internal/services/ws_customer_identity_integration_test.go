package services

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/mlogclub/simple/sqls"
)

// External-ID presence or dropping a customer after its first socket closes
// must fail this test. Legacy-only registrations cannot establish presence.
func TestIsCustomerOnlineByCustomerID(t *testing.T) {
	svc := newWsServiceForTest()
	external := &openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: "synthetic-presence-collision"}
	a1 := captureCustomerRealtimeSession(t, svc, "synthetic-A1", 41, external)
	a2 := captureCustomerRealtimeSession(t, svc, "synthetic-A2", 41, external)
	b1 := captureCustomerRealtimeSession(t, svc, "synthetic-B1", 42, external)
	legacy := &ClientSession{ID: "synthetic-legacy-presence", Topics: make(map[string]struct{})}
	svc.manager.Register(legacy, []string{"guest:synthetic-presence-collision", "guest:41", "guest:42", "guest:0", "guest:-1", "customer:0", "customer:-1"})
	t.Cleanup(func() { svc.manager.Unregister(legacy) })
	check := func(wantA, wantB bool) {
		t.Helper()
		if got := svc.IsCustomerOnline(41); got != wantA {
			t.Errorf("A online = %v, want %v", got, wantA)
		}
		if got := svc.IsCustomerOnline(42); got != wantB {
			t.Errorf("B online = %v, want %v", got, wantB)
		}
		for _, id := range []int64{0, -1} {
			if svc.IsCustomerOnline(id) {
				t.Errorf("invalid customer %d must be offline even with registered topics", id)
			}
		}
	}
	check(true, true)
	svc.manager.Unregister(a1)
	check(true, true)
	svc.manager.Unregister(a2)
	check(false, true)
	svc.manager.Unregister(b1)
	check(false, false)
}

// in-process synthetic WebSocket runtime/integration acceptance: exercises the
// actual /api/ws/open handler with synthetic sessions and an in-memory database.
// Restoring external-ID routing or admitting a foreign topic must fail here.
func TestCustomerRealtimeIdentityRuntime(t *testing.T) {
	previousDB, previousConfig := sqls.DB(), config.GetCurrent()
	t.Cleanup(func() {
		sqls.SetDB(previousDB)
		config.SetCurrent(previousConfig)
	})
	db := openHumanDispatchRealtimeTestDB(t, false)
	if err := db.Create(&models.User{
		ID:       701,
		Username: "synthetic-runtime-admin",
		Nickname: "synthetic runtime admin",
		UserType: enums.UserTypeEmployee,
		Status:   enums.StatusOk,
	}).Error; err != nil {
		t.Fatal("create synthetic global employee principal")
	}
	signingKey := make([]byte, 32)
	if _, err := rand.Read(signingKey); err != nil {
		t.Fatal("generate in-memory signing key")
	}
	config.SetCurrent(&config.Config{CustomerSession: config.CustomerSessionConfig{Secret: base64.RawURLEncoding.EncodeToString(signingKey)}})
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatal("generate synthetic markers")
	}
	suffix := hex.EncodeToString(random)
	externalA, externalB := createCustomerPublicationCollision(t, db, "synthetic-runtime-collision-"+suffix)
	channel := &models.Channel{ID: 301, ChannelID: "synthetic-runtime-channel", Status: enums.StatusOk}
	conversationA := &models.Conversation{ID: 501, ChannelID: channel.ID, CustomerID: 41, Status: enums.IMConversationStatusAIServing}
	conversationB := &models.Conversation{ID: 502, ChannelID: channel.ID, CustomerID: 42, Status: enums.IMConversationStatusAIServing}
	for _, row := range []any{channel, conversationA, conversationB} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	tokens := make([]string, 2)
	for i, external := range []openidentity.ExternalUser{externalA, externalB} {
		customerID := int64(41 + i)
		var err error
		tokens[i], _, err = CustomerSessionService.Sign(channel, &models.Customer{ID: customerID, Name: "synthetic-runtime-customer"}, external)
		if err != nil {
			t.Fatal("sign synthetic customer session")
		}
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/ws/open", nil)
		ctx.Request.Header.Set("Authorization", "Bearer "+tokens[i])
		verified, err := CustomerSessionService.VerifyRequest(ctx, channel)
		if err != nil || verified == nil || verified.CustomerID != customerID || verified.ExternalUser == nil || verified.ExternalUser.ExternalSource != external.ExternalSource || verified.ExternalUser.ExternalID != external.ExternalID {
			t.Fatal("verified synthetic session did not retain its customer/source/ID mapping")
		}
	}
	for _, tc := range []struct {
		name         string
		conversation *models.Conversation
		customerID   int64
		want         bool
	}{
		{"A owns A", conversationA, 41, true},
		{"B owns B", conversationB, 42, true},
		{"A cannot own B", conversationB, 41, false},
		{"B cannot own A", conversationA, 42, false},
	} {
		if got := ConversationService.IsVerifiedCustomerConversationOwner(tc.conversation, tc.customerID); got != tc.want {
			t.Fatalf("REST ownership %s = %v, want %v", tc.name, got, tc.want)
		}
	}

	svc := newWsServiceForTest()
	router := gin.New()
	router.GET("/api/ws/open", svc.HandleOpenWS)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	connect := func(token, query, ownTopic string, customerID int64) (*websocket.Conn, *ClientSession) {
		t.Helper()
		headers := http.Header{"Authorization": {"Bearer " + token}, "X-Channel-ID": {channel.ChannelID}}
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/ws/open"+query, headers)
		if err != nil {
			t.Fatal("synthetic customer WebSocket handshake failed")
		}
		t.Cleanup(func() { _ = conn.Close() })
		connected := readCustomerIdentityTestEvent(t, conn, enums.IMRealtimeEventConnected)
		topics, ok := connected.Data["topics"].([]any)
		if !ok || len(topics) != 1 || topics[0] != ownTopic {
			t.Fatalf("connected topics = %v, want only %s", topics, ownTopic)
		}
		connID, _ := connected.Data["connId"].(string)
		svc.manager.mu.RLock()
		session := svc.manager.sessions[connID]
		svc.manager.mu.RUnlock()
		if session == nil || session.CustomerID != customerID {
			t.Fatal("socket registry did not retain verified CustomerID")
		}
		t.Cleanup(func() { svc.closeSession(session) })
		return conn, session
	}
	a, sessionA := connect(tokens[0], "?customerId=42", "customer:41", 41)
	b, sessionB := connect(tokens[1], "", "customer:42", 42)
	if !svc.IsCustomerOnline(41) || !svc.IsCustomerOnline(42) {
		t.Fatal("both authenticated customers must be online")
	}
	for _, tc := range []struct {
		conn         *websocket.Conn
		session      *ClientSession
		foreignTopic string
	}{
		{a, sessionA, "customer:42"}, {b, sessionB, "customer:41"},
		{a, sessionA, "conversation:502"}, {b, sessionB, "conversation:501"},
	} {
		if err := tc.conn.WriteJSON(map[string]any{"type": "subscribe", "topics": []string{tc.foreignTopic}}); err != nil {
			t.Fatal(err)
		}
		// Ordered pong is the next frame: any foreign acknowledgement fails.
		customerIdentityTestPing(t, tc.conn)
		readCustomerIdentityTestEvent(t, tc.conn, enums.IMRealtimeEventPong)
		if slices.Contains(svc.manager.FindByTopics([]string{tc.foreignTopic}), tc.session) {
			t.Fatalf("foreign registration admitted for %s", tc.foreignTopic)
		}
		// A protected publication on the denied destination must also stay silent.
		svc.PublishToTopic(tc.foreignTopic, svc.newEvent(tc.foreignTopic, RealtimeResyncRequiredEvent{Payload: RealtimeResyncRequiredPayload{Reason: "synthetic-foreign-topic-marker"}}))
		customerIdentityTestPing(t, tc.conn)
		readCustomerIdentityTestEvent(t, tc.conn, enums.IMRealtimeEventPong)
		if tc.foreignTopic == "customer:42" {
			readCustomerIdentityTestEvent(t, b, enums.IMRealtimeEventResyncRequired)
		} else if tc.foreignTopic == "customer:41" {
			readCustomerIdentityTestEvent(t, a, enums.IMRealtimeEventResyncRequired)
		}
	}
	legacyTopic := "guest:" + externalA.ExternalID
	svc.manager.Subscribe(sessionA, []string{legacyTopic})
	svc.manager.Subscribe(sessionB, []string{legacyTopic})
	for _, tc := range []struct {
		conversation   *models.Conversation
		owner, foreign *websocket.Conn
		marker, topic  string
		messageID      int64
	}{
		{conversationA, a, b, "CUSTOMER_WS_A_" + suffix, "conversation:501", 601},
		{conversationB, b, a, "CUSTOMER_WS_B_" + suffix, "conversation:502", 602},
	} {
		svc.PublishMessageCreated(tc.conversation, &models.Message{ID: tc.messageID, ConversationID: tc.conversation.ID, SenderType: enums.IMSenderTypeCustomer, MessageType: enums.IMMessageTypeText, Content: tc.marker})
		event := readCustomerIdentityTestEvent(t, tc.owner, enums.IMRealtimeEventMessageCreated)
		if event.Topic != tc.topic || event.Data["content"] != tc.marker || event.Data["conversationId"] != float64(tc.conversation.ID) {
			t.Fatal("owner publication lost its original conversation envelope or marker")
		}
		// Publication enqueues before ping: these fences reject leaks and duplicates.
		customerIdentityTestPing(t, tc.owner)
		readCustomerIdentityTestEvent(t, tc.owner, enums.IMRealtimeEventPong)
		customerIdentityTestPing(t, tc.foreign)
		readCustomerIdentityTestEvent(t, tc.foreign, enums.IMRealtimeEventPong)
	}
	// A stale explicit subscription must not retain full events after the
	// application's customer-link operation transfers current ownership to B.
	if err := a.WriteJSON(map[string]any{"type": "subscribe", "topics": []string{"conversation:501"}}); err != nil {
		t.Fatal(err)
	}
	ack := readCustomerIdentityTestEvent(t, a, enums.IMRealtimeEventSubscribed)
	if topics, ok := ack.Data["topics"].([]any); !ok || len(topics) != 1 || topics[0] != "conversation:501" {
		t.Fatal("A's owned conversation subscription was not acknowledged")
	}
	if !slices.Contains(svc.manager.FindByTopics([]string{"conversation:501"}), sessionA) {
		t.Fatal("A's owned conversation subscription was not registered")
	}
	previousWsService := WsService
	WsService = svc
	t.Cleanup(func() { WsService = previousWsService })
	operator := &dto.AuthPrincipal{
		UserID:   701,
		Username: "synthetic-runtime-admin",
		UserType: enums.UserTypeEmployee,
		Status:   enums.StatusOk,
		Roles:    []string{constants.RoleCodeSuperAdmin},
	}
	if err := ConversationService.LinkConversationCustomer(501, 42, operator); err != nil {
		t.Fatalf("reassign synthetic conversation through service: %v", err)
	}
	reassigned := ConversationService.Get(501)
	if reassigned == nil || reassigned.CustomerID != 42 {
		t.Fatal("service did not reassign conversation to B")
	}
	updated := readCustomerIdentityTestEvent(t, b, enums.IMRealtimeEventConversationUpdated)
	if updated.Topic != "conversation:501" || updated.Data["conversationId"] != float64(501) || updated.Data["status"] != float64(enums.IMConversationStatusAIServing) {
		t.Fatal("B did not receive the reassigned conversation update")
	}
	customerIdentityTestPing(t, b)
	readCustomerIdentityTestEvent(t, b, enums.IMRealtimeEventPong)
	// Publish enqueues before ping; any protected event precedes the pong and fails.
	assertAReceivesOnlyPong := func() {
		t.Helper()
		customerIdentityTestPing(t, a)
		if err := a.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		for {
			_, body, err := a.ReadMessage()
			if err != nil {
				t.Fatalf("A did not remain connected for pong fence: %v", err)
			}
			var event capturedRealtimeEvent
			if err := json.Unmarshal(body, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == enums.IMRealtimeEventPong {
				return
			}
			t.Errorf("former owner A received unauthorized event type=%q topic=%q conversationId=%v content=%v", event.Type, event.Topic, event.Data["conversationId"], event.Data["content"])
		}
	}
	assertAReceivesOnlyPong()
	marker := "CUSTOMER_WS_REASSIGNED_B_" + suffix
	svc.PublishMessageCreated(reassigned, &models.Message{ID: 603, ConversationID: 501, SenderType: enums.IMSenderTypeCustomer, MessageType: enums.IMMessageTypeText, Content: marker})
	message := readCustomerIdentityTestEvent(t, b, enums.IMRealtimeEventMessageCreated)
	if message.Topic != "conversation:501" || message.Data["conversationId"] != float64(501) || message.Data["content"] != marker {
		t.Fatal("B did not receive the full reassigned conversation message")
	}
	customerIdentityTestPing(t, b)
	readCustomerIdentityTestEvent(t, b, enums.IMRealtimeEventPong)
	assertAReceivesOnlyPong()
	if !svc.IsCustomerOnline(41) || !slices.Contains(svc.manager.FindByTopics([]string{"conversation:501"}), sessionA) {
		t.Fatal("delivery denial must leave A connected with its stale subscription")
	}
	closeAndWait := func(conn *websocket.Conn, session *ClientSession) {
		t.Helper()
		_ = conn.Close()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			svc.manager.mu.RLock()
			_, registered := svc.manager.sessions[session.ID]
			svc.manager.mu.RUnlock()
			if !registered {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("closed synthetic socket remained registered")
	}
	closeAndWait(a, sessionA)
	if svc.IsCustomerOnline(41) || !svc.IsCustomerOnline(42) {
		t.Fatal("A disconnect must leave only B online")
	}
	closeAndWait(b, sessionB)
	b1, sessionB1 := connect(tokens[1], "", "customer:42", 42)
	b2, sessionB2 := connect(tokens[1], "", "customer:42", 42)
	closeAndWait(b1, sessionB1)
	if !svc.IsCustomerOnline(42) {
		t.Fatal("B must remain online until its last socket closes")
	}
	closeAndWait(b2, sessionB2)
	if svc.IsCustomerOnline(42) {
		t.Fatal("B must be offline after its last socket closes")
	}
	for _, id := range []int64{0, -1} {
		invalid := &ClientSession{Role: realtimeRoleUser, CustomerID: id, External: &externalA}
		if topics := svc.defaultTopics(invalid); len(topics) != 0 {
			t.Fatalf("invalid customer %d got default topics %v", id, topics)
		}
	}
}
