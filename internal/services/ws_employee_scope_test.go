package services

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func employeeRealtimeScopeFixture(t *testing.T) (*gorm.DB, *wsService, *dto.AuthPrincipal) {
	t.Helper()
	previousDB := sqls.DB()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "employee-realtime-scope.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.AgentTeam{}, &models.AgentProfile{}, &models.Conversation{}); err != nil {
		t.Fatal(err)
	}
	for _, teamID := range []int64{10, 20} {
		if err := db.Create(&models.AgentTeam{ID: teamID, Name: fmt.Sprintf("scope-team-%d", teamID), Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ userID, teamID int64 }{{101, 10}, {102, 10}, {201, 20}} {
		if err := db.Create(&models.User{ID: item.userID, Username: fmt.Sprintf("scope-agent-%d", item.userID), UserType: enums.UserTypeEmployee, Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.AgentProfile{UserID: item.userID, TeamID: item.teamID, AgentCode: fmt.Sprintf("scope-%d", item.userID), Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
	}
	conversations := []models.Conversation{
		{ID: 1, Status: enums.IMConversationStatusActive, ServiceMode: enums.IMConversationServiceModeAIFirst, CurrentAssigneeID: 101, CurrentTeamID: 10},
		{ID: 2, Status: enums.IMConversationStatusActive, ServiceMode: enums.IMConversationServiceModeAIFirst, CurrentAssigneeID: 102, CurrentTeamID: 10},
		{ID: 3, Status: enums.IMConversationStatusPending, ServiceMode: enums.IMConversationServiceModeAIFirst, CurrentTeamID: 10},
		{ID: 4, Status: enums.IMConversationStatusPending, ServiceMode: enums.IMConversationServiceModeAIFirst, CurrentTeamID: 20},
		{ID: 5, Status: enums.IMConversationStatusAIServing, ServiceMode: enums.IMConversationServiceModeAIFirst, CurrentTeamID: 10},
		{ID: 6, Status: enums.IMConversationStatusPending, ServiceMode: enums.IMConversationServiceModeAIOnly, CurrentTeamID: 10},
		{ID: 7, Status: enums.IMConversationStatusClosed, ServiceMode: enums.IMConversationServiceModeAIFirst, CurrentAssigneeID: 101, CurrentTeamID: 10},
		{ID: 42, Status: enums.IMConversationStatusPending, ServiceMode: enums.IMConversationServiceModeAIFirst, CurrentTeamID: 10},
	}
	if err := db.Create(&conversations).Error; err != nil {
		t.Fatal(err)
	}
	sqls.SetDB(db)
	t.Cleanup(func() {
		sqls.SetDB(previousDB)
		if raw, err := db.DB(); err == nil {
			_ = raw.Close()
		}
	})
	principal := &dto.AuthPrincipal{
		UserID: 101, UserType: enums.UserTypeEmployee, Status: enums.StatusOk,
		Roles: []string{constants.RoleCodeCsUser}, Permissions: []string{constants.PermissionConversationView.Code},
	}
	return db, newWsServiceForTest(), principal
}

func realtimeScopePayload(conversationID int64) RealtimeConversationChangedPayload {
	return RealtimeConversationChangedPayload{ConversationID: conversationID, Status: enums.IMConversationStatusPending, ServiceMode: enums.IMConversationServiceModeAIFirst, CurrentTeamID: 10}
}

func TestEmployeeRealtimeConversationSubscriptionUsesCentralScope(t *testing.T) {
	db, svc, principal := employeeRealtimeScopeFixture(t)
	for _, item := range []struct {
		conversationID int64
		want           bool
	}{{1, true}, {2, false}, {3, true}, {4, false}, {5, false}, {6, false}, {7, true}, {999, false}} {
		topic := svc.conversationTopic(item.conversationID)
		allowed := svc.CanSubscribeTopic(&ClientSession{Role: realtimeRoleAdmin, EmployeeID: principal.UserID, Principal: principal}, topic)
		if allowed != item.want {
			t.Errorf("conversation %d subscription allowed=%v, want=%v", item.conversationID, allowed, item.want)
		}
	}
	if err := db.Model(&models.AgentProfile{}).Where("user_id = ?", principal.UserID).Update("status", enums.StatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if svc.CanSubscribeTopic(&ClientSession{Role: realtimeRoleAdmin, EmployeeID: principal.UserID, Principal: principal}, svc.conversationTopic(1)) {
		t.Fatal("disabled employee profile admitted a direct subscription")
	}
}

func TestEmployeeRealtimeStaleConversationSubscriptionRechecksDatabase(t *testing.T) {
	for _, item := range []struct {
		name           string
		conversationID int64
		mutate         func(*testing.T, *gorm.DB)
	}{
		{"assignment", 1, func(t *testing.T, db *gorm.DB) {
			if err := db.Model(&models.Conversation{}).Where("id = ?", 1).Update("current_assignee_id", 102).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"team", 3, func(t *testing.T, db *gorm.DB) {
			if err := db.Model(&models.AgentProfile{}).Where("user_id = ?", 101).Update("team_id", 20).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"status", 3, func(t *testing.T, db *gorm.DB) {
			if err := db.Model(&models.Conversation{}).Where("id = ?", 3).Update("status", enums.IMConversationStatusAIServing).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"profile", 1, func(t *testing.T, db *gorm.DB) {
			if err := db.Model(&models.AgentProfile{}).Where("user_id = ?", 101).Update("status", enums.StatusDisabled).Error; err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(item.name, func(t *testing.T) {
			db, svc, principal := employeeRealtimeScopeFixture(t)
			session := captureEmployeeRealtimeSession(t, svc, "stale-"+item.name, principal, "admin:101")
			topic := svc.conversationTopic(item.conversationID)
			if got := svc.subscribeTopics(session, []string{topic}); len(got) != 1 {
				t.Fatalf("initial direct subscription = %v, want %q", got, topic)
			}
			item.mutate(t, db)
			fresh := captureEmployeeRealtimeSession(t, svc, "fresh-"+item.name, principal, "admin:101")
			if got := svc.subscribeTopics(fresh, []string{topic}); len(got) != 0 {
				t.Fatalf("new direct subscription after scope change = %v, want none", got)
			}
			if _, subscribed := fresh.Topics[topic]; subscribed {
				t.Fatal("new session retained conversation membership after losing scope")
			}
			event := RealtimeEvent{Type: enums.IMRealtimeEventConversationUpdated, Topic: topic, Data: realtimeScopePayload(item.conversationID)}
			svc.PublishToTopic(topic, event)
			requireNoCapturedRealtimeEvent(t, session)
		})
	}
}

func TestEmployeeRealtimeQueueFanoutOnlyIncludesAuthorizedHumanWaiting(t *testing.T) {
	_, svc, agent := employeeRealtimeScopeFixture(t)
	teamLeader := *agent
	teamLeader.UserID = 102
	teamLeader.Roles = []string{constants.RoleCodeCsTeamLeader}
	otherTeam := *agent
	otherTeam.UserID = 201
	otherTeam.Roles = []string{constants.RoleCodeCsUser}
	queueAgent := captureEmployeeRealtimeSession(t, svc, "queue-agent", agent, "admin:all")
	queueLeader := captureEmployeeRealtimeSession(t, svc, "queue-leader", &teamLeader, "admin:all")
	queueOtherTeam := captureEmployeeRealtimeSession(t, svc, "queue-other-team", &otherTeam, "admin:all")
	personalAgent := captureEmployeeRealtimeSession(t, svc, "personal-agent", agent, "admin:101")
	personalLeader := captureEmployeeRealtimeSession(t, svc, "personal-leader", &teamLeader, "admin:102")
	personalOtherTeam := captureEmployeeRealtimeSession(t, svc, "personal-other-team", &otherTeam, "admin:201")
	for _, conversationID := range []int64{3, 4, 5, 6} {
		topic := svc.conversationTopic(conversationID)
		event := RealtimeEvent{Type: enums.IMRealtimeEventConversationUpdated, Topic: topic, Data: realtimeScopePayload(conversationID)}
		svc.PublishToTopic("admin:all", event)
		svc.PublishToTopic("admin:101", event)
		svc.PublishToTopic("admin:102", event)
		svc.PublishToTopic("admin:201", event)
		if conversationID == 3 {
			requireCapturedRealtimeEvent(t, queueAgent, enums.IMRealtimeEventConversationUpdated)
			requireCapturedRealtimeEvent(t, queueLeader, enums.IMRealtimeEventConversationUpdated)
			requireNoCapturedRealtimeEvent(t, queueOtherTeam)
			requireCapturedRealtimeEvent(t, personalAgent, enums.IMRealtimeEventConversationUpdated)
			requireCapturedRealtimeEvent(t, personalLeader, enums.IMRealtimeEventConversationUpdated)
			requireNoCapturedRealtimeEvent(t, personalOtherTeam)
		} else if conversationID == 4 {
			requireNoCapturedRealtimeEvent(t, queueAgent)
			requireNoCapturedRealtimeEvent(t, queueLeader)
			requireCapturedRealtimeEvent(t, queueOtherTeam, enums.IMRealtimeEventConversationUpdated)
			requireNoCapturedRealtimeEvent(t, personalAgent)
			requireNoCapturedRealtimeEvent(t, personalLeader)
			requireCapturedRealtimeEvent(t, personalOtherTeam, enums.IMRealtimeEventConversationUpdated)
		} else {
			requireNoCapturedRealtimeEvent(t, queueAgent)
			requireNoCapturedRealtimeEvent(t, queueLeader)
			requireNoCapturedRealtimeEvent(t, queueOtherTeam)
			requireNoCapturedRealtimeEvent(t, personalAgent)
			requireNoCapturedRealtimeEvent(t, personalLeader)
			requireNoCapturedRealtimeEvent(t, personalOtherTeam)
		}
	}
}

func TestEmployeeRealtimeMalformedIdentityFailsClosedAtFanout(t *testing.T) {
	for _, item := range []struct {
		name   string
		mutate func(*testing.T, *gorm.DB)
	}{
		{"missing-user", func(t *testing.T, db *gorm.DB) {
			if err := db.Delete(&models.User{}, 101).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"missing-profile", func(t *testing.T, db *gorm.DB) {
			if err := db.Where("user_id = ?", 101).Delete(&models.AgentProfile{}).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"missing-team", func(t *testing.T, db *gorm.DB) {
			if err := db.Delete(&models.AgentTeam{}, 10).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"zero-team", func(t *testing.T, db *gorm.DB) {
			if err := db.Model(&models.AgentProfile{}).Where("user_id = ?", 101).Update("team_id", 0).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"disabled-team", func(t *testing.T, db *gorm.DB) {
			if err := db.Model(&models.AgentTeam{}).Where("id = ?", 10).Update("status", enums.StatusDisabled).Error; err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(item.name, func(t *testing.T) {
			db, svc, principal := employeeRealtimeScopeFixture(t)
			session := captureEmployeeRealtimeSession(t, svc, "bad-identity-"+item.name, principal, "admin:all", "conversation:1")
			item.mutate(t, db)
			event := RealtimeEvent{Type: enums.IMRealtimeEventMessageCreated, Topic: "conversation:1", Data: RealtimeMessageCreatedPayload{ConversationID: 1, MessageID: 12}}
			svc.PublishToTopics([]string{"admin:all", "conversation:1"}, event)
			requireNoCapturedRealtimeEvent(t, session)
		})
	}
}

func TestEmployeeRealtimeResyncRechecksCurrentIdentityScope(t *testing.T) {
	for _, identity := range []struct {
		name   string
		mutate func(*testing.T, *gorm.DB)
	}{
		{"user", func(t *testing.T, db *gorm.DB) {
			if err := db.Model(&models.User{}).Where("id = ?", 101).Update("status", enums.StatusDisabled).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"profile", func(t *testing.T, db *gorm.DB) {
			if err := db.Model(&models.AgentProfile{}).Where("user_id = ?", 101).Update("status", enums.StatusDisabled).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{"team", func(t *testing.T, db *gorm.DB) {
			if err := db.Model(&models.AgentTeam{}).Where("id = ?", 10).Update("status", enums.StatusDisabled).Error; err != nil {
				t.Fatal(err)
			}
		}},
	} {
		for _, topic := range []string{"admin:all", "admin:101"} {
			t.Run(identity.name+"/"+topic, func(t *testing.T) {
				db, svc, principal := employeeRealtimeScopeFixture(t)
				session := captureEmployeeRealtimeSession(t, svc, "stale-resync-"+identity.name+"-"+topic, principal, topic)
				identity.mutate(t, db)
				svc.PublishResyncRequired([]string{topic}, "synthetic stale-scope marker")
				requireNoCapturedRealtimeEvent(t, session)
			})
		}
	}
}
