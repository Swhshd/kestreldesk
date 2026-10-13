package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

type malformedTicketNotificationPayload struct {
	BizType string `json:"bizType"`
	Title   string `json:"title"`
}

func (malformedTicketNotificationPayload) realtimeEventPayload() {}

func TestWsTicketNotificationDeliveryRechecksCurrentTicketScope(t *testing.T) {
	t.Run("current assignee receives ticket notification", func(t *testing.T) {
		_, svc, userID, _, _, _, session := ticketNotificationFixture(t, constants.RoleCodeCsUser)
		ticket := createTicketNotificationForTest(t, "current assignment", userID)

		publishTicketNotificationForTest(svc, userID, userID, "ticket", ticket.ID)

		assertTicketNotificationDeliveredForTest(t, session, "ticket", ticket.ID)
	})

	t.Run("reassignment suppresses stale ticket notification", func(t *testing.T) {
		db, svc, userID, peerID, _, _, session := ticketNotificationFixture(t, constants.RoleCodeCsUser)
		ticket := createTicketNotificationForTest(t, "reassigned ticket", userID)
		if err := db.Model(&models.Ticket{}).Where("id = ?", ticket.ID).Update("current_assignee_id", peerID).Error; err != nil {
			t.Fatal(err)
		}

		publishTicketNotificationForTest(svc, userID, userID, "ticket", ticket.ID)

		assertNoTicketNotificationForTest(t, session)
	})

	t.Run("missing ticket suppresses notification", func(t *testing.T) {
		_, svc, userID, _, _, _, session := ticketNotificationFixture(t, constants.RoleCodeCsUser)

		publishTicketNotificationForTest(svc, userID, userID, "ticket", 987654321)

		assertNoTicketNotificationForTest(t, session)
	})

	t.Run("team leader receives active teammate ticket notification", func(t *testing.T) {
		_, svc, leaderID, teammateID, _, _, session := ticketNotificationFixture(t, constants.RoleCodeCsTeamLeader)
		ticket := createTicketNotificationForTest(t, "same team ticket", teammateID)

		publishTicketNotificationForTest(svc, leaderID, leaderID, "ticket", ticket.ID)

		assertTicketNotificationDeliveredForTest(t, session, "ticket", ticket.ID)
	})

	t.Run("team leader cannot receive other team ticket notification", func(t *testing.T) {
		_, svc, _, _, otherTeamID, principal, session := ticketNotificationFixture(t, constants.RoleCodeCsTeamLeader)
		otherAssigneeID := createTicketNotificationAgentForTest(t, "ticket-notification-outside-team", otherTeamID, enums.StatusOk)
		ticket := createTicketNotificationForTest(t, "other team ticket", otherAssigneeID)

		publishTicketNotificationForTest(svc, principal.UserID, principal.UserID, "ticket", ticket.ID)

		assertNoTicketNotificationForTest(t, session)
	})

	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *gorm.DB, int64, int64)
	}{
		{"disabled profile", func(t *testing.T, db *gorm.DB, userID, _ int64) {
			if err := db.Model(&models.AgentProfile{}).Where("user_id = ?", userID).Update("status", enums.StatusDisabled).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"missing profile", func(t *testing.T, db *gorm.DB, userID, _ int64) {
			if err := db.Where("user_id = ?", userID).Delete(&models.AgentProfile{}).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"disabled team", func(t *testing.T, db *gorm.DB, userID, _ int64) {
			var profile models.AgentProfile
			if err := db.Where("user_id = ?", userID).Take(&profile).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&models.AgentTeam{}).Where("id = ?", profile.TeamID).Update("status", enums.StatusDisabled).Error; err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name+" denies ticket notification", func(t *testing.T) {
			db, svc, userID, _, _, _, session := ticketNotificationFixture(t, constants.RoleCodeCsUser)
			ticket := createTicketNotificationForTest(t, tc.name, userID)
			tc.mutate(t, db, userID, 0)

			publishTicketNotificationForTest(svc, userID, userID, "ticket", ticket.ID)

			assertNoTicketNotificationForTest(t, session)
		})
	}

	t.Run("admin retains global ticket scope without profile", func(t *testing.T) {
		_, svc, _, _, otherTeamID, _, _ := ticketNotificationFixture(t, constants.RoleCodeAdmin)
		adminID := createTicketNotificationEmployeeForTest(t, "ticket-notification-admin")
		principal := ticketNotificationPrincipalForTest(adminID, constants.RoleCodeAdmin)
		session := ticketNotificationSessionForTest(t, svc, principal)
		otherAssigneeID := createTicketNotificationAgentForTest(t, "ticket-notification-admin-target", otherTeamID, enums.StatusOk)
		ticket := createTicketNotificationForTest(t, "admin global ticket", otherAssigneeID)

		publishTicketNotificationForTest(svc, adminID, adminID, "ticket", ticket.ID)

		assertTicketNotificationDeliveredForTest(t, session, "ticket", ticket.ID)
	})

	t.Run("recipient identity mismatch suppresses ticket notification", func(t *testing.T) {
		_, svc, userID, peerID, _, _, session := ticketNotificationFixture(t, constants.RoleCodeCsUser)
		ticket := createTicketNotificationForTest(t, "identity mismatch", userID)

		publishTicketNotificationForTest(svc, userID, peerID, "ticket", ticket.ID)

		assertNoTicketNotificationForTest(t, session)
	})

	t.Run("principal employee mismatch suppresses ticket notification", func(t *testing.T) {
		_, svc, userID, _, _, _, session := ticketNotificationFixture(t, constants.RoleCodeCsUser)
		ticket := createTicketNotificationForTest(t, "session identity mismatch", userID)
		session.EmployeeID++

		publishTicketNotificationForTest(svc, userID, userID, "ticket", ticket.ID)

		assertNoTicketNotificationForTest(t, session)
	})

	t.Run("invalid principal status suppresses ticket notification", func(t *testing.T) {
		_, svc, userID, _, _, _, session := ticketNotificationFixture(t, constants.RoleCodeCsUser)
		ticket := createTicketNotificationForTest(t, "invalid principal", userID)
		session.Principal.Status = enums.StatusDisabled

		publishTicketNotificationForTest(svc, userID, userID, "ticket", ticket.ID)

		assertNoTicketNotificationForTest(t, session)
	})

	t.Run("ticket lookup errors suppress ticket notification", func(t *testing.T) {
		db, svc, userID, _, _, _, session := ticketNotificationFixture(t, constants.RoleCodeCsUser)
		ticket := createTicketNotificationForTest(t, "lookup error", userID)
		const callbackName = "test:ticket_notification_lookup_error"
		if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Table == "t_ticket" {
				tx.AddError(errors.New("synthetic Ticket lookup error"))
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

		publishTicketNotificationForTest(svc, userID, userID, "ticket", ticket.ID)

		assertNoTicketNotificationForTest(t, session)
	})

	t.Run("non-ticket notification keeps legacy delivery", func(t *testing.T) {
		setupTicketNotificationDBForTest(t)
		svc := newWsServiceForTest()
		userID := createTicketNotificationEmployeeForTest(t, "ticket-notification-no-profile")
		principal := ticketNotificationPrincipalForTest(userID, constants.RoleCodeCsUser)
		session := ticketNotificationSessionForTest(t, svc, principal)

		publishTicketNotificationForTest(svc, userID, userID, "conversation", 42)

		assertTicketNotificationDeliveredForTest(t, session, "conversation", 42)
	})

	t.Run("malformed notification payload is denied", func(t *testing.T) {
		_, svc, userID, _, _, _, session := ticketNotificationFixture(t, constants.RoleCodeCsUser)
		ticket := createTicketNotificationForTest(t, "malformed ticket notification", userID)

		svc.PublishToTopic(svc.notificationTopic(userID), RealtimeEvent{
			Type: enums.IMRealtimeEventNotificationCreated,
			Data: malformedTicketNotificationPayload{BizType: "ticket", Title: ticket.Title},
		})

		assertNoTicketNotificationForTest(t, session)
	})
}

func TestWsTicketNotificationDeliveryRechecksScopeAtSocketWrite(t *testing.T) {
	db, svc, userID, peerID, _, _, session := ticketNotificationFixture(t, constants.RoleCodeCsUser)
	ticket := createTicketNotificationForTest(t, "ticket reassigned while queued", userID)
	publishTicketNotificationForTest(svc, userID, userID, "ticket", ticket.ID)
	if got := len(session.Send); got != 1 {
		t.Fatalf("queued notifications = %d, want one", got)
	}
	if err := db.Model(&models.Ticket{}).Where("id = ?", ticket.ID).Update("current_assignee_id", peerID).Error; err != nil {
		t.Fatal(err)
	}

	const callbackName = "test:ticket_notification_write_scope_check"
	ticketLookupStarted := make(chan struct{}, 1)
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "t_ticket" {
			select {
			case ticketLookupStarted <- struct{}{}:
			default:
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	clientConn, serverConn := ticketNotificationWebSocketPairForTest(t)
	session.Conn = serverConn
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		svc.writePump(session)
	}()
	t.Cleanup(func() {
		svc.closeSession(session)
		_ = clientConn.Close()
		select {
		case <-pumpDone:
		case <-time.After(time.Second):
			t.Error("write pump did not stop after session close")
		}
	})

	select {
	case <-ticketLookupStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("write pump did not recheck Ticket scope before socket delivery")
	}
	_ = clientConn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	_, payload, err := clientConn.ReadMessage()
	if err == nil {
		t.Fatalf("stale Ticket notification reached the socket: %s", payload)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("socket read error = %v, want timeout after stale notification was suppressed", err)
	}
}

func TestWsTicketNotificationInScopeReachesSocket(t *testing.T) {
	_, svc, userID, _, _, _, session := ticketNotificationFixture(t, constants.RoleCodeCsUser)
	ticket := createTicketNotificationForTest(t, "still assigned ticket", userID)
	publishTicketNotificationForTest(svc, userID, userID, "ticket", ticket.ID)
	clientConn, serverConn := ticketNotificationWebSocketPairForTest(t)
	session.Conn = serverConn
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		svc.writePump(session)
	}()
	t.Cleanup(func() {
		svc.closeSession(session)
		_ = clientConn.Close()
		select {
		case <-pumpDone:
		case <-time.After(time.Second):
			t.Error("write pump did not stop after session close")
		}
	})

	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, payload, err := clientConn.ReadMessage()
	if err != nil {
		t.Fatalf("read authorized Ticket notification: %v", err)
	}
	var event struct {
		Type string `json:"type"`
		Data struct {
			Notification response.NotificationResponse `json:"notification"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("decode authorized Ticket notification: %v", err)
	}
	if event.Type != enums.IMRealtimeEventNotificationCreated || event.Data.Notification.BizType != "ticket" ||
		event.Data.Notification.BizID != ticket.ID || event.Data.Notification.RecipientUserID != userID {
		t.Fatalf("socket notification = %#v, want current recipient's Ticket %d", event, ticket.ID)
	}
}

func TestWsTicketNotificationMalformedPayloadFailsClosedAtSocketWrite(t *testing.T) {
	svc := newWsServiceForTest()
	session := &ClientSession{Role: realtimeRoleNotification}
	for _, payload := range [][]byte{
		[]byte("not-json"),
		[]byte(`{"type":"notification.created"}`),
		[]byte(`{"type":"notification.created","data":{"notification":{"bizType":"ticket"}}}`),
	} {
		if svc.notificationPayloadAllowedAtSocketWrite(session, payload) {
			t.Errorf("malformed notification payload was allowed: %s", payload)
		}
	}
	legacyPayload := []byte(`{"type":"notification.created","data":{"notification":{"bizType":""}}}`)
	if !svc.notificationPayloadAllowedAtSocketWrite(session, legacyPayload) {
		t.Fatal("legacy notification with no business type was suppressed")
	}
}

func ticketNotificationWebSocketPairForTest(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	serverConn := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			serverConn <- conn
		}
	}))
	t.Cleanup(server.Close)

	clientConn, _, err := websocket.DefaultDialer.Dial("ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("dial test WebSocket: %v", err)
	}
	var accepted *websocket.Conn
	select {
	case accepted = <-serverConn:
	case <-time.After(time.Second):
		_ = clientConn.Close()
		t.Fatal("test WebSocket was not accepted")
	}
	t.Cleanup(func() { _ = accepted.Close() })
	return clientConn, accepted
}

func ticketNotificationFixture(t *testing.T, role string) (*gorm.DB, *wsService, int64, int64, int64, *dto.AuthPrincipal, *ClientSession) {
	t.Helper()
	db := setupTicketNotificationDBForTest(t)
	teamID := createTicketNotificationTeamForTest(t, "ticket-notification-team")
	otherTeamID := createTicketNotificationTeamForTest(t, "ticket-notification-other-team")
	userID := createTicketNotificationAgentForTest(t, "ticket-notification-agent", teamID, enums.StatusOk)
	peerID := createTicketNotificationAgentForTest(t, "ticket-notification-peer", teamID, enums.StatusOk)
	createTicketNotificationAgentForTest(t, "ticket-notification-other-agent", otherTeamID, enums.StatusOk)
	principal := ticketNotificationPrincipalForTest(userID, role)
	svc := newWsServiceForTest()
	session := ticketNotificationSessionForTest(t, svc, principal)
	return db, svc, userID, peerID, otherTeamID, principal, session
}

func ticketNotificationSessionForTest(t *testing.T, svc *wsService, principal *dto.AuthPrincipal) *ClientSession {
	t.Helper()
	session := &ClientSession{
		ID:        fmt.Sprintf("ticket-notification-%d", principal.UserID),
		Role:      realtimeRoleNotification,
		Principal: principal,
		Topics:    make(map[string]struct{}),
		Send:      make(chan []byte, realtimeSendBufferSize),
	}
	markActiveEmployeeTestSession(session)
	svc.manager.Register(session, []string{svc.notificationTopic(principal.UserID)})
	t.Cleanup(func() { svc.manager.Unregister(session) })
	return session
}

func publishTicketNotificationForTest(svc *wsService, topicUserID, recipientUserID int64, bizType string, bizID int64) {
	svc.PublishNotificationCreated(topicUserID, response.NotificationResponse{
		ID:              1,
		RecipientUserID: recipientUserID,
		BizType:         bizType,
		BizID:           bizID,
		Title:           "synthetic notification title",
		Content:         "synthetic notification content",
	})
}

func assertTicketNotificationDeliveredForTest(t *testing.T, session *ClientSession, bizType string, bizID int64) {
	t.Helper()
	event := requireCapturedRealtimeEvent(t, session, enums.IMRealtimeEventNotificationCreated)
	notification, ok := event.Data["notification"].(map[string]any)
	if !ok {
		t.Fatalf("notification.created data = %#v, want notification object", event.Data)
	}
	if notification["bizType"] != bizType || notification["bizId"] != float64(bizID) {
		t.Fatalf("delivered notification business target = (%v, %v), want (%q, %d)", notification["bizType"], notification["bizId"], bizType, bizID)
	}
	if notification["id"] != float64(1) || notification["recipientUserId"] != float64(session.EmployeeID) ||
		notification["title"] != "synthetic notification title" || notification["content"] != "synthetic notification content" {
		t.Fatalf("notification.created payload fields changed: %#v", notification)
	}
	if event.Topic != fmt.Sprintf("notification:%d", session.EmployeeID) {
		t.Fatalf("notification topic = %q, want recipient topic", event.Topic)
	}
}

func assertNoTicketNotificationForTest(t *testing.T, session *ClientSession) {
	t.Helper()
	requireNoCapturedRealtimeEvent(t, session)
}

func createTicketNotificationTeamForTest(t *testing.T, name string) int64 {
	t.Helper()
	team := &models.AgentTeam{Name: fmt.Sprintf("%s-%d", name, time.Now().UnixNano()), Status: enums.StatusOk}
	if err := sqls.DB().Create(team).Error; err != nil {
		t.Fatalf("create team: %v", err)
	}
	return team.ID
}

func createTicketNotificationAgentForTest(t *testing.T, name string, teamID int64, profileStatus enums.Status) int64 {
	t.Helper()
	user := &models.User{
		Username: fmt.Sprintf("%s-%d", name, time.Now().UnixNano()),
		Nickname: name,
		UserType: enums.UserTypeEmployee,
		Status:   enums.StatusOk,
	}
	if err := sqls.DB().Create(user).Error; err != nil {
		t.Fatalf("create employee: %v", err)
	}
	profile := &models.AgentProfile{
		UserID:      user.ID,
		TeamID:      teamID,
		AgentCode:   fmt.Sprintf("%s-%d", name, time.Now().UnixNano()),
		DisplayName: name,
		Status:      profileStatus,
	}
	if err := sqls.DB().Create(profile).Error; err != nil {
		t.Fatalf("create profile: %v", err)
	}
	return user.ID
}

func createTicketNotificationEmployeeForTest(t *testing.T, name string) int64 {
	t.Helper()
	user := &models.User{
		Username: fmt.Sprintf("%s-%d", name, time.Now().UnixNano()),
		Nickname: name,
		UserType: enums.UserTypeEmployee,
		Status:   enums.StatusOk,
	}
	if err := sqls.DB().Create(user).Error; err != nil {
		t.Fatalf("create employee: %v", err)
	}
	return user.ID
}

func createTicketNotificationForTest(t *testing.T, title string, assigneeID int64) *models.Ticket {
	t.Helper()
	ticket := &models.Ticket{
		TicketNo:          fmt.Sprintf("ticket-notification-%d", time.Now().UnixNano()),
		Title:             title,
		Description:       title,
		Status:            enums.TicketStatusPending,
		CurrentAssigneeID: assigneeID,
	}
	if err := sqls.DB().Create(ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	return ticket
}

func ticketNotificationPrincipalForTest(userID int64, role string) *dto.AuthPrincipal {
	return &dto.AuthPrincipal{
		UserID:      userID,
		UserType:    enums.UserTypeEmployee,
		Status:      enums.StatusOk,
		Roles:       []string{role},
		Permissions: []string{constants.PermissionTicketView.Code},
	}
}

func setupTicketNotificationDBForTest(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := sqls.DB()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ticket-notification.db")), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		NamingStrategy: schema.NamingStrategy{TablePrefix: "t_", SingularTable: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.AgentTeam{}, &models.AgentProfile{}, &models.Ticket{}); err != nil {
		t.Fatal(err)
	}
	sqls.SetDB(db)
	t.Cleanup(func() {
		sqls.SetDB(previousDB)
		if raw, err := db.DB(); err == nil {
			_ = raw.Close()
		}
	})
	return db
}
