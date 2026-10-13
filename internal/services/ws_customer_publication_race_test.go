package services

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestCustomerConversationPublicationRechecksOwnershipAtEnqueue(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{TablePrefix: "t_", SingularTable: true},
	})
	if err != nil {
		t.Fatalf("open isolated realtime database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(2)
	previousDB := sqls.DB()
	sqls.SetDB(db)
	t.Cleanup(func() {
		sqls.SetDB(previousDB)
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close isolated realtime database: %v", err)
		}
	})
	if err := db.AutoMigrate(&models.Conversation{}); err != nil {
		t.Fatalf("migrate synthetic conversation: %v", err)
	}
	conversation := &models.Conversation{CustomerID: 41}
	if err := db.Create(conversation).Error; err != nil {
		t.Fatalf("create synthetic conversation: %v", err)
	}

	initialScopeRead := make(chan struct{})
	allowPublicationToContinue := make(chan struct{})
	var conversationReads atomic.Int32
	if err := db.Callback().Query().After("gorm:query").Register("test:pause_after_customer_scope_read", func(tx *gorm.DB) {
		if tx.Statement.Table != "t_conversation" || conversationReads.Add(1) != 1 {
			return
		}
		close(initialScopeRead)
		<-allowPublicationToContinue
	}); err != nil {
		t.Fatalf("install deterministic ownership-change barrier: %v", err)
	}

	svc := newWsServiceForTest()
	topic := svc.conversationTopic(conversation.ID)
	session := &ClientSession{
		ID:         "synthetic-customer-scope-race",
		Role:       realtimeRoleUser,
		CustomerID: 41,
		External:   &openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: "synthetic-scope-race"},
		Topics:     make(map[string]struct{}),
		Send:       make(chan []byte, 1),
	}
	svc.manager.Register(session, []string{topic})
	t.Cleanup(func() { svc.manager.Unregister(session) })

	done := make(chan struct{})
	go func() {
		svc.PublishToTopic(topic, RealtimeEvent{EventID: "synthetic-scope-race", Type: enums.IMRealtimeEventMessageCreated})
		close(done)
	}()

	select {
	case <-initialScopeRead:
	case <-time.After(5 * time.Second):
		t.Fatal("publication did not reach its initial ownership check")
	}
	if err := db.Model(&models.Conversation{}).Where("id = ?", conversation.ID).Update("customer_id", 42).Error; err != nil {
		t.Fatalf("transfer synthetic conversation ownership: %v", err)
	}
	close(allowPublicationToContinue)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publication did not finish after ownership transfer")
	}

	select {
	case payload := <-session.Send:
		t.Fatalf("former owner received a protected event after ownership changed: %s", payload)
	default:
	}
}

func TestCustomerQueuedPublicationReauthorizesAtSocketWriteWithoutSharedResult(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{TablePrefix: "t_", SingularTable: true},
	})
	if err != nil {
		t.Fatalf("open isolated realtime database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(2)
	previousDB := sqls.DB()
	sqls.SetDB(db)
	t.Cleanup(func() {
		sqls.SetDB(previousDB)
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close isolated realtime database: %v", err)
		}
	})
	if err := db.AutoMigrate(&models.Conversation{}); err != nil {
		t.Fatalf("migrate synthetic conversation: %v", err)
	}
	conversation := &models.Conversation{CustomerID: 41}
	if err := db.Create(conversation).Error; err != nil {
		t.Fatalf("create synthetic conversation: %v", err)
	}

	var conversationReads atomic.Int32
	writeScopeCheckStarted := make(chan struct{})
	if err := db.Callback().Query().Before("gorm:query").Register("test:customer_write_scope_race", func(tx *gorm.DB) {
		if tx.Statement.Table == "t_conversation" && conversationReads.Add(1) == 3 {
			close(writeScopeCheckStarted)
		}
	}); err != nil {
		t.Fatalf("install socket-write scope observer: %v", err)
	}
	if err := db.Callback().Query().After("gorm:query").Register("test:transfer_customer_after_enqueue_authorization", func(tx *gorm.DB) {
		if tx.Statement.Table != "t_conversation" || conversationReads.Load() != 2 {
			return
		}
		if err := db.Model(&models.Conversation{}).Where("id = ?", conversation.ID).Update("customer_id", 42).Error; err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatalf("install deterministic ownership transfer: %v", err)
	}

	svc := newWsServiceForTest()
	topic := svc.conversationTopic(conversation.ID)
	session := &ClientSession{
		ID:         "synthetic-customer-write-scope-race",
		Role:       realtimeRoleUser,
		CustomerID: 41,
		External:   &openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: "synthetic-customer-write-scope-race"},
		Topics:     make(map[string]struct{}),
		Send:       make(chan []byte, 1),
	}
	svc.manager.Register(session, []string{topic})
	t.Cleanup(func() { svc.manager.Unregister(session) })

	writePumpDone := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	router := gin.New()
	router.GET("/ws", func(ctx *gin.Context) {
		conn, err := upgrader.Upgrade(ctx.Writer, ctx.Request, nil)
		if err != nil {
			return
		}
		session.Conn = conn
		svc.writePump(session)
		close(writePumpDone)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	peer, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial synthetic websocket: %v", err)
	}
	defer peer.Close()
	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set synthetic websocket read deadline: %v", err)
	}

	publishDone := make(chan struct{})
	go func() {
		svc.PublishToTopic(topic, RealtimeEvent{
			EventID: "synthetic-customer-write-scope-race",
			Type:    enums.IMRealtimeEventMessageCreated,
			Topic:   topic,
			Data:    RealtimeMessageCreatedPayload{ConversationID: conversation.ID, MessageID: 903, Content: "protected-synthetic-content"},
		})
		close(publishDone)
	}()

	select {
	case <-publishDone:
	case <-time.After(3 * time.Second):
		t.Fatal("customer publication did not finish")
	}
	select {
	case <-writeScopeCheckStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("queued event did not reach socket-write authorization")
	}
	if got := conversationReads.Load(); got != 3 {
		t.Fatalf("conversation scope checks = %d, want initial, enqueue, and socket-write checks", got)
	}
	var owner models.Conversation
	if err := db.First(&owner, conversation.ID).Error; err != nil {
		t.Fatalf("reload transferred synthetic conversation: %v", err)
	}
	if owner.CustomerID != 42 {
		t.Fatalf("synthetic conversation owner = %d, want transfer to 42", owner.CustomerID)
	}

	_, payload, readErr := peer.ReadMessage()
	if readErr == nil {
		t.Fatalf("former owner received protected payload after ownership transfer: %s", payload)
	}
	var networkErr net.Error
	if !errors.As(readErr, &networkErr) || !networkErr.Timeout() {
		t.Fatalf("websocket read error = %v, want timeout with no protected event", readErr)
	}

	svc.closeSession(session)
	select {
	case <-writePumpDone:
	case <-time.After(2 * time.Second):
		t.Fatal("websocket write pump did not stop after session close")
	}
}

func TestCustomerTopicPublicationChecksConversationOwnership(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{TablePrefix: "t_", SingularTable: true},
	})
	if err != nil {
		t.Fatalf("open isolated realtime database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := sqls.DB()
	sqls.SetDB(db)
	t.Cleanup(func() {
		sqls.SetDB(previousDB)
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close isolated realtime database: %v", err)
		}
	})
	if err := db.AutoMigrate(&models.Conversation{}); err != nil {
		t.Fatalf("migrate synthetic conversation: %v", err)
	}
	conversation := &models.Conversation{CustomerID: 42}
	if err := db.Create(conversation).Error; err != nil {
		t.Fatalf("create synthetic conversation: %v", err)
	}

	svc := newWsServiceForTest()
	customerTopic := svc.customerTopic(41)
	session := &ClientSession{
		ID:         "synthetic-customer-topic-scope",
		Role:       realtimeRoleUser,
		CustomerID: 41,
		External:   &openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: "synthetic-customer-topic-scope"},
		Topics:     make(map[string]struct{}),
		Send:       make(chan []byte, 1),
	}
	svc.manager.Register(session, []string{customerTopic})
	t.Cleanup(func() { svc.manager.Unregister(session) })

	svc.PublishToTopics([]string{customerTopic}, RealtimeEvent{
		EventID: "synthetic-customer-topic-scope",
		Type:    enums.IMRealtimeEventMessageCreated,
		Topic:   svc.conversationTopic(conversation.ID),
		Data:    RealtimeMessageCreatedPayload{ConversationID: conversation.ID, Content: "protected synthetic message"},
	})

	select {
	case payload := <-session.Send:
		t.Fatalf("non-owner received a protected event through the customer topic: %s", payload)
	default:
	}
}
