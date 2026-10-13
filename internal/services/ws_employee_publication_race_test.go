package services

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

func TestEmployeeConversationPublicationRechecksScopeAtEnqueue(t *testing.T) {
	db, svc, principal := employeeRealtimeScopeFixture(t)
	session := captureEmployeeRealtimeSession(t, svc, "synthetic-employee-scope-race", principal, "conversation:1")
	session.sendMu.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(session.sendMu.Unlock) }
	defer unlock()

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
		svc.PublishToTopic("conversation:1", RealtimeEvent{
			EventID: "synthetic-employee-scope-race",
			Type:    enums.IMRealtimeEventMessageCreated,
			Topic:   "conversation:1",
			Data:    RealtimeMessageCreatedPayload{ConversationID: 1, MessageID: 91, Content: "protected-synthetic-content"},
		})
	}()

	id := <-publisherID
	if id == "" {
		t.Fatal("could not identify publication goroutine")
	}
	waitWsGoroutineStacks(t, "employee publication waiting on sendMu after initial authorization", func(stacks map[string]string) bool {
		stack := stacks[id]
		return strings.Contains(stack, "(*ClientSession).enqueueWithAuthorization(") && strings.Contains(stack, "sync.(*Mutex).Lock(")
	})

	if err := db.Model(&models.Conversation{}).Where("id = ?", 1).Update("current_assignee_id", 102).Error; err != nil {
		t.Fatalf("transfer synthetic conversation before enqueue: %v", err)
	}
	unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publication did not finish after releasing sendMu")
	}
	requireNoCapturedRealtimeEvent(t, session)
}

func TestEmployeeQueuedConversationPublicationRechecksScopeAtSocketWrite(t *testing.T) {
	db, svc, principal := employeeRealtimeScopeFixture(t)
	session := captureEmployeeRealtimeSession(t, svc, "synthetic-employee-queued-scope-race", principal, "conversation:1")
	startWritePump := make(chan struct{})
	writePumpStarted := make(chan struct{})
	writePumpDone := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	router := gin.New()
	router.GET("/ws", func(ctx *gin.Context) {
		conn, err := upgrader.Upgrade(ctx.Writer, ctx.Request, nil)
		if err != nil {
			return
		}
		session.Conn = conn
		close(writePumpStarted)
		<-startWritePump
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
	select {
	case <-writePumpStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("websocket session did not upgrade")
	}

	svc.PublishToTopic("conversation:1", RealtimeEvent{
		EventID: "synthetic-employee-queued-scope-race",
		Type:    enums.IMRealtimeEventMessageCreated,
		Topic:   "conversation:1",
		Data:    RealtimeMessageCreatedPayload{ConversationID: 1, MessageID: 92, Content: "queued-protected-synthetic-content"},
	})
	if got := len(session.Send); got != 1 {
		t.Fatalf("queued protected event count = %d, want 1 before scope transfer", got)
	}
	if err := db.Model(&models.Conversation{}).Where("id = ?", 1).Update("current_assignee_id", 102).Error; err != nil {
		t.Fatalf("transfer synthetic conversation before socket write: %v", err)
	}

	scopeReadStarted := make(chan struct{})
	allowScopeReadToFinish := make(chan struct{})
	var readOnce sync.Once
	if err := db.Callback().Query().After("gorm:query").Register("test:pause_employee_scope_at_socket_write", func(tx *gorm.DB) {
		readOnce.Do(func() {
			close(scopeReadStarted)
			<-allowScopeReadToFinish
		})
	}); err != nil {
		t.Fatalf("install socket-write authorization barrier: %v", err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set synthetic websocket read deadline: %v", err)
	}
	readResult := make(chan error, 1)
	go func() {
		_, payload, err := peer.ReadMessage()
		if err == nil {
			readResult <- fmt.Errorf("received protected event after assignment changed: %s", payload)
			return
		}
		var networkErr net.Error
		if errors.As(err, &networkErr) && networkErr.Timeout() {
			readResult <- nil
			return
		}
		readResult <- err
	}()
	close(startWritePump)

	select {
	case <-scopeReadStarted:
		close(allowScopeReadToFinish)
	case err := <-readResult:
		if err == nil {
			t.Fatal("socket returned no event before write-time scope check ran")
		}
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("queued event did not reach socket-write authorization")
	}
	if err := <-readResult; err != nil {
		t.Fatal(err)
	}

	svc.closeSession(session)
	select {
	case <-writePumpDone:
	case <-time.After(2 * time.Second):
		t.Fatal("socket write pump did not stop after session close")
	}
}

func TestEmployeeQueuedPublicationExpiresBeforeSocketWrite(t *testing.T) {
	_, svc, principal := employeeRealtimeScopeFixture(t)
	session := captureEmployeeRealtimeSession(t, svc, "synthetic-employee-queued-deadline-race", principal, "admin:101")
	payload := []byte("queued-synthetic-protected-content")
	if !session.enqueue(payload) {
		t.Fatal("could not enqueue event before session deadline")
	}

	startWritePump := make(chan struct{})
	writePumpDone := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	router := gin.New()
	router.GET("/ws", func(ctx *gin.Context) {
		conn, err := upgrader.Upgrade(ctx.Writer, ctx.Request, nil)
		if err != nil {
			return
		}
		session.Conn = conn
		<-startWritePump
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

	// The queue accepts the event while this session is active; expiry happens
	// before the writer dequeues it.
	session.LifecycleDeadline = time.Now().Add(-time.Second)
	close(startWritePump)
	_, got, err := peer.ReadMessage()
	if err == nil {
		t.Fatalf("received queued event after employee session deadline: %q", got)
	}
	select {
	case <-writePumpDone:
	case <-time.After(2 * time.Second):
		t.Fatal("write pump did not stop after expired session close")
	}
	if !session.Closed.Load() {
		t.Fatal("expired employee session remained open after queued dequeue")
	}
}

func TestEmployeeScopeRechecksUseBoundedDatabaseContexts(t *testing.T) {
	for _, check := range []struct {
		name string
		call func(*wsService, *ClientSession) bool
	}{
		{name: "admin scope", call: func(svc *wsService, session *ClientSession) bool {
			return svc.hasCurrentEmployeeRealtimeScope(session)
		}},
		{name: "conversation scope", call: func(svc *wsService, session *ClientSession) bool {
			return svc.canReceiveEmployeeConversation(session, "conversation:1", 1)
		}},
	} {
		t.Run(check.name, func(t *testing.T) {
			db, svc, principal := employeeRealtimeScopeFixture(t)
			session := captureEmployeeRealtimeSession(t, svc, "synthetic-bounded-employee-scope", principal, "admin:101")
			var gotDeadline time.Time
			if err := db.Callback().Query().Before("gorm:query").Register("test:employee_scope_query_deadline", func(tx *gorm.DB) {
				if deadline, ok := tx.Statement.Context.Deadline(); ok {
					gotDeadline = deadline
				}
			}); err != nil {
				t.Fatalf("install employee scope deadline observer: %v", err)
			}
			if !check.call(svc, session) {
				t.Fatal("scope check unexpectedly rejected the synthetic employee")
			}
			if gotDeadline.IsZero() {
				t.Fatal("employee scope DB query did not receive a deadline")
			}
			remaining := time.Until(gotDeadline)
			if remaining <= 0 || remaining > 2*time.Second {
				t.Fatalf("employee scope DB query deadline has %s remaining, want (0, 2s]", remaining)
			}
		})
	}
}

func TestCustomerConversationScopeRecheckUsesBoundedDatabaseContext(t *testing.T) {
	db, svc, _ := employeeRealtimeScopeFixture(t)
	if err := db.Model(&models.Conversation{}).Where("id = ?", 1).Update("customer_id", 303).Error; err != nil {
		t.Fatalf("assign synthetic customer to conversation: %v", err)
	}
	session := &ClientSession{Role: realtimeRoleUser, CustomerID: 303}
	var gotDeadline time.Time
	if err := db.Callback().Query().Before("gorm:query").Register("test:customer_scope_query_deadline", func(tx *gorm.DB) {
		if deadline, ok := tx.Statement.Context.Deadline(); ok {
			gotDeadline = deadline
		}
	}); err != nil {
		t.Fatalf("install customer scope deadline observer: %v", err)
	}
	if !svc.canSubscribeConversation(session, 1) {
		t.Fatal("customer scope check unexpectedly rejected its synthetic conversation")
	}
	if gotDeadline.IsZero() {
		t.Fatal("customer scope DB query did not receive a deadline")
	}
	remaining := time.Until(gotDeadline)
	if remaining <= 0 || remaining > 2*time.Second {
		t.Fatalf("customer scope DB query deadline has %s remaining, want (0, 2s]", remaining)
	}
}
