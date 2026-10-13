package services

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/request"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func setupConversationWriteScopeReviewDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{TablePrefix: "t_", SingularTable: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	previousDB := sqls.DB()
	t.Cleanup(func() {
		sqls.SetDB(previousDB)
		raw, err := db.DB()
		if err == nil {
			_ = raw.Close()
		}
	})
	if err := db.AutoMigrate(
		&models.User{}, &models.AgentTeam{}, &models.AgentProfile{}, &models.AIAgent{},
		&models.AgentTeamSchedule{}, &models.Conversation{}, &models.ConversationAssignment{},
		&models.ConversationEventLog{}, &models.Message{}, &models.ConversationReadState{}, &models.Channel{}, &models.Customer{},
		&models.ChannelMessageOutbox{}, &models.Notification{},
	); err != nil {
		t.Fatal(err)
	}
	sqls.SetDB(db)
	previousWS := WsService
	WsService = newWsServiceForTest()
	t.Cleanup(func() { WsService = previousWS })

	for _, teamID := range []int64{10, 20} {
		if err := db.Create(&models.AgentTeam{ID: teamID, Name: fmt.Sprintf("synthetic-team-%d", teamID), Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ userID, teamID int64 }{{101, 10}, {102, 10}, {201, 20}} {
		if err := db.Create(&models.User{ID: item.userID, Username: fmt.Sprintf("synthetic-employee-%d", item.userID), UserType: enums.UserTypeEmployee, Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.AgentProfile{UserID: item.userID, TeamID: item.teamID, AgentCode: fmt.Sprintf("synthetic-agent-%d", item.userID), Status: enums.StatusOk, ServiceStatus: enums.ServiceStatusIdle, AutoAssignEnabled: true, MaxConcurrentCount: 4}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func reviewOperator(userID int64, role string) *dto.AuthPrincipal {
	return &dto.AuthPrincipal{UserID: userID, Username: fmt.Sprintf("synthetic-employee-%d", userID), UserType: enums.UserTypeEmployee, Status: enums.StatusOk, Roles: []string{role}, Permissions: []string{constants.PermissionConversationAssign.Code, constants.PermissionConversationTransfer.Code}}
}

func createReviewConversation(t *testing.T, db *gorm.DB, status enums.IMConversationStatus, assigneeID, teamID int64, serviceMode enums.IMConversationServiceMode) *models.Conversation {
	t.Helper()
	item := &models.Conversation{Status: status, CurrentAssigneeID: assigneeID, CurrentTeamID: teamID, ServiceMode: serviceMode}
	if err := db.Create(item).Error; err != nil {
		t.Fatal(err)
	}
	return item
}

func waitForReviewAssignmentNotification(t *testing.T, db *gorm.DB, conversationID int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var count int64
		if err := db.Model(&models.Notification{}).Where("biz_id = ?", conversationID).Count(&count).Error; err == nil && count > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("assignment event for conversation %d was not consumed", conversationID)
}

func TestAssignConversationRejectsCsUserWithDirectPermissionOverride(t *testing.T) {
	db := setupConversationWriteScopeReviewDB(t)
	conversation := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 10, enums.IMConversationServiceModeHumanOnly)
	err := ConversationService.AssignConversation(request.AssignConversationRequest{ConversationID: conversation.ID, AssigneeID: 102, Reason: "synthetic"}, reviewOperator(101, constants.RoleCodeCsUser))
	if err == nil {
		waitForReviewAssignmentNotification(t, db, conversation.ID)
		t.Fatal("cs_user with a direct permission override assigned a waiting conversation")
	}
	var current models.Conversation
	if err := db.First(&current, conversation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.Status != enums.IMConversationStatusPending || current.CurrentAssigneeID != 0 {
		t.Fatalf("denied assignment mutated conversation: %+v", current)
	}
	if got := ConversationAssignmentService.Count(sqls.NewCnd().Eq("conversation_id", conversation.ID)); got != 0 {
		t.Fatalf("denied assignment wrote %d audit rows", got)
	}
}

func TestAssignConversationRejectsCrossTeamConversationForTeamLeader(t *testing.T) {
	db := setupConversationWriteScopeReviewDB(t)
	conversation := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 20, enums.IMConversationServiceModeHumanOnly)
	if err := ConversationService.AssignConversation(request.AssignConversationRequest{ConversationID: conversation.ID, AssigneeID: 201, Reason: "synthetic"}, reviewOperator(101, constants.RoleCodeCsTeamLeader)); err == nil {
		waitForReviewAssignmentNotification(t, db, conversation.ID)
		t.Fatal("team leader assigned a conversation outside their authoritative team")
	}
	var current models.Conversation
	if err := db.First(&current, conversation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.Status != enums.IMConversationStatusPending || current.CurrentAssigneeID != 0 {
		t.Fatalf("denied cross-team assignment mutated conversation: %+v", current)
	}
}

func TestLinkConversationCustomerRejectsCrossTeamConversationForTeamLeader(t *testing.T) {
	db := setupConversationWriteScopeReviewDB(t)
	conversation := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 20, enums.IMConversationServiceModeHumanOnly)
	if err := db.Create(&models.Customer{ID: 4242, Name: "synthetic-customer", Status: enums.StatusOk}).Error; err != nil {
		t.Fatal(err)
	}
	if err := ConversationService.LinkConversationCustomer(conversation.ID, 4242, reviewOperator(101, constants.RoleCodeCsTeamLeader)); err == nil {
		t.Fatal("team leader linked a customer to a conversation outside their authoritative team")
	}
	var current models.Conversation
	if err := db.First(&current, conversation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.CustomerID != 0 {
		t.Fatalf("denied cross-team customer link changed customer_id to %d", current.CustomerID)
	}
}

func TestLinkConversationCustomerDoesNotDiscloseCustomerExistenceOutsideScope(t *testing.T) {
	db := setupConversationWriteScopeReviewDB(t)
	conversation := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 20, enums.IMConversationServiceModeHumanOnly)
	if err := db.Create(&models.Customer{ID: 4242, Name: "synthetic-customer", Status: enums.StatusOk}).Error; err != nil {
		t.Fatal(err)
	}
	operator := reviewOperator(101, constants.RoleCodeCsTeamLeader)
	validCustomerErr := ConversationService.LinkConversationCustomer(conversation.ID, 4242, operator)
	missingCustomerErr := ConversationService.LinkConversationCustomer(conversation.ID, 4243, operator)
	if validCustomerErr == nil || missingCustomerErr == nil {
		t.Fatal("out-of-scope conversation link must be denied for both existing and missing customer IDs")
	}
	if validCustomerErr.Error() != missingCustomerErr.Error() {
		t.Fatalf("out-of-scope customer existence leaked through different errors: existing=%q missing=%q", validCustomerErr, missingCustomerErr)
	}
}

func TestLinkConversationCustomerDoesNotDiscloseOutOfScopeConversationState(t *testing.T) {
	db := setupConversationWriteScopeReviewDB(t)
	closed := createReviewConversation(t, db, enums.IMConversationStatusClosed, 0, 20, enums.IMConversationServiceModeHumanOnly)
	pending := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 20, enums.IMConversationServiceModeHumanOnly)
	if err := db.Create(&models.Customer{ID: 4242, Name: "synthetic-customer", Status: enums.StatusOk}).Error; err != nil {
		t.Fatal(err)
	}
	operator := reviewOperator(101, constants.RoleCodeCsTeamLeader)
	errs := []error{
		ConversationService.LinkConversationCustomer(closed.ID, 4242, operator),
		ConversationService.LinkConversationCustomer(pending.ID, 4242, operator),
		ConversationService.LinkConversationCustomer(9999, 4242, operator),
	}
	for i, err := range errs {
		if err == nil {
			t.Fatalf("out-of-scope conversation request %d unexpectedly succeeded", i)
		}
	}
	for i := 1; i < len(errs); i++ {
		if errs[i].Error() != errs[0].Error() {
			t.Fatalf("out-of-scope conversation state/existence leaked: closed=%q pending=%q missing=%q", errs[0], errs[1], errs[2])
		}
	}
}

func TestAdminCannotLinkConversationCustomerOutsideItsTeam(t *testing.T) {
	db := setupConversationWriteScopeReviewDB(t)
	conversation := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 20, enums.IMConversationServiceModeHumanOnly)
	if err := db.Create(&models.Customer{ID: 4242, Name: "synthetic-customer", Status: enums.StatusOk}).Error; err != nil {
		t.Fatal(err)
	}
	if err := ConversationService.LinkConversationCustomer(conversation.ID, 4242, reviewOperator(101, constants.RoleCodeAdmin)); err == nil {
		t.Fatal("admin linked a customer to a conversation outside its authoritative team")
	}
	var current models.Conversation
	if err := db.First(&current, conversation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.CustomerID != 0 {
		t.Fatalf("denied admin cross-team customer link changed customer_id to %d", current.CustomerID)
	}
}

func TestLinkConversationCustomerRechecksTeamScopeInWriteTransaction(t *testing.T) {
	db := setupConversationWriteScopeReviewDB(t)
	conversation := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 10, enums.IMConversationServiceModeHumanOnly)
	if err := db.Create(&models.Customer{ID: 4242, Name: "synthetic-customer", Status: enums.StatusOk}).Error; err != nil {
		t.Fatal(err)
	}
	interleaved := false
	callbackName := "test:move_linked_conversation_out_of_scope"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(query *gorm.DB) {
		if query.Statement.Table != "t_conversation" || interleaved {
			return
		}
		_, inTransaction := query.Statement.ConnPool.(gorm.TxCommitter)
		if !inTransaction {
			return
		}
		interleaved = true
		if _, err := query.Statement.ConnPool.ExecContext(context.Background(), "UPDATE t_conversation SET current_team_id = ? WHERE id = ?", 20, conversation.ID); err != nil {
			t.Errorf("move conversation outside the operator's team: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	if err := ConversationService.LinkConversationCustomer(conversation.ID, 4242, reviewOperator(101, constants.RoleCodeCsTeamLeader)); err == nil {
		t.Fatal("customer link succeeded after the conversation moved outside the operator's team")
	}
	if !interleaved {
		t.Fatal("deterministic team change did not happen between lookup and write transaction")
	}
	var current models.Conversation
	if err := db.First(&current, conversation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.CurrentTeamID != 10 || current.CustomerID != 0 {
		t.Fatalf("rejected transaction did not roll back the interleaved scope change: %+v", current)
	}
}

func TestAssignConversationRejectsAIOnlyPendingConversation(t *testing.T) {
	db := setupConversationWriteScopeReviewDB(t)
	conversation := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 20, enums.IMConversationServiceModeAIOnly)
	operator := reviewOperator(101, constants.RoleCodeSuperAdmin)
	if err := ConversationService.AssignConversation(request.AssignConversationRequest{ConversationID: conversation.ID, AssigneeID: 201, Reason: "synthetic"}, operator); err == nil {
		waitForReviewAssignmentNotification(t, db, conversation.ID)
		t.Fatal("AI-only pending conversation entered the human assignment path")
	}
	var current models.Conversation
	if err := db.First(&current, conversation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.Status != enums.IMConversationStatusPending || current.CurrentAssigneeID != 0 {
		t.Fatalf("rejected AI-only assignment mutated conversation: %+v", current)
	}
}

func TestTransferConversationRejectsCsUserWithDirectPermissionOverride(t *testing.T) {
	db := setupConversationWriteScopeReviewDB(t)
	conversation := createReviewConversation(t, db, enums.IMConversationStatusActive, 101, 10, enums.IMConversationServiceModeHumanOnly)
	err := ConversationService.TransferConversation(conversation.ID, 102, "synthetic", reviewOperator(101, constants.RoleCodeCsUser))
	if err == nil {
		waitForReviewAssignmentNotification(t, db, conversation.ID)
		t.Fatal("cs_user with a direct permission override transferred an assigned conversation")
	}
	var current models.Conversation
	if err := db.First(&current, conversation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.CurrentAssigneeID != 101 || current.CurrentTeamID != 10 {
		t.Fatalf("denied transfer mutated conversation: %+v", current)
	}
}

func TestTransferConversationRejectsCrossTeamConversationForTeamLeader(t *testing.T) {
	db := setupConversationWriteScopeReviewDB(t)
	conversation := createReviewConversation(t, db, enums.IMConversationStatusActive, 101, 20, enums.IMConversationServiceModeHumanOnly)
	if err := ConversationService.TransferConversation(conversation.ID, 201, "synthetic", reviewOperator(101, constants.RoleCodeCsTeamLeader)); err == nil {
		waitForReviewAssignmentNotification(t, db, conversation.ID)
		t.Fatal("team leader transferred a conversation outside their authoritative team")
	}
	var current models.Conversation
	if err := db.First(&current, conversation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.CurrentAssigneeID != 101 || current.CurrentTeamID != 20 {
		t.Fatalf("denied cross-team transfer mutated conversation: %+v", current)
	}
}

func TestSuperAdminCanAssignAcrossTeamsAndValidateTargetIdentity(t *testing.T) {
	db := setupConversationWriteScopeReviewDB(t)
	conversation := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 20, enums.IMConversationServiceModeHumanOnly)
	if err := ConversationService.AssignConversation(request.AssignConversationRequest{ConversationID: conversation.ID, AssigneeID: 201, Reason: "synthetic"}, reviewOperator(101, constants.RoleCodeSuperAdmin)); err != nil {
		t.Fatalf("global super_admin assignment failed: %v", err)
	}
	waitForReviewAssignmentNotification(t, db, conversation.ID)
	var current models.Conversation
	if err := db.First(&current, conversation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.CurrentAssigneeID != 201 || current.CurrentTeamID != 20 || current.Status != enums.IMConversationStatusActive {
		t.Fatalf("global super_admin assignment changed semantics: %+v", current)
	}

	if err := db.Create(&models.User{ID: 301, Username: "synthetic-customer-user", UserType: enums.UserTypeUser, Status: enums.StatusOk}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.AgentProfile{UserID: 301, TeamID: 20, AgentCode: "synthetic-customer-profile", Status: enums.StatusOk}).Error; err != nil {
		t.Fatal(err)
	}
	other := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 20, enums.IMConversationServiceModeHumanOnly)
	if err := ConversationService.AssignConversation(request.AssignConversationRequest{ConversationID: other.ID, AssigneeID: 301, Reason: "synthetic"}, reviewOperator(101, constants.RoleCodeSuperAdmin)); err == nil {
		waitForReviewAssignmentNotification(t, db, other.ID)
		t.Fatal("assignment accepted a profile whose account is not an employee")
	}
}

func TestAssignConversationRejectsInactiveAndWrongTeamTargets(t *testing.T) {
	for _, scenario := range []string{"inactive-user", "wrong-team"} {
		t.Run(scenario, func(t *testing.T) {
			db := setupConversationWriteScopeReviewDB(t)
			conversation := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 10, enums.IMConversationServiceModeHumanOnly)
			targetID := int64(102)
			if scenario == "inactive-user" {
				if err := db.Model(&models.User{}).Where("id = ?", targetID).Update("status", enums.StatusDisabled).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				targetID = 201
			}
			if err := ConversationService.AssignConversation(request.AssignConversationRequest{ConversationID: conversation.ID, AssigneeID: targetID, Reason: "synthetic"}, reviewOperator(101, constants.RoleCodeCsTeamLeader)); err == nil {
				waitForReviewAssignmentNotification(t, db, conversation.ID)
				t.Fatalf("assignment accepted %s target", scenario)
			}
			var current models.Conversation
			if err := db.First(&current, conversation.ID).Error; err != nil {
				t.Fatal(err)
			}
			if current.Status != enums.IMConversationStatusPending || current.CurrentAssigneeID != 0 {
				t.Fatalf("rejected target assignment mutated conversation: %+v", current)
			}
		})
	}
}

func TestTransferConversationRejectsInactiveNonEmployeeAndWrongTeamTargets(t *testing.T) {
	for _, scenario := range []string{"inactive-user", "non-employee", "wrong-team"} {
		t.Run(scenario, func(t *testing.T) {
			db := setupConversationWriteScopeReviewDB(t)
			conversation := createReviewConversation(t, db, enums.IMConversationStatusActive, 101, 10, enums.IMConversationServiceModeHumanOnly)
			targetID := int64(102)
			switch scenario {
			case "inactive-user":
				if err := db.Model(&models.User{}).Where("id = ?", targetID).Update("status", enums.StatusDisabled).Error; err != nil {
					t.Fatal(err)
				}
			case "non-employee":
				targetID = 301
				if err := db.Create(&models.User{ID: targetID, Username: "synthetic-customer-user", UserType: enums.UserTypeUser, Status: enums.StatusOk}).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&models.AgentProfile{UserID: targetID, TeamID: 10, AgentCode: "synthetic-customer-profile", Status: enums.StatusOk}).Error; err != nil {
					t.Fatal(err)
				}
			case "wrong-team":
				targetID = 201
			}
			if err := ConversationService.TransferConversation(conversation.ID, targetID, "synthetic", reviewOperator(101, constants.RoleCodeCsTeamLeader)); err == nil {
				waitForReviewAssignmentNotification(t, db, conversation.ID)
				t.Fatalf("transfer accepted %s target", scenario)
			}
			var current models.Conversation
			if err := db.First(&current, conversation.ID).Error; err != nil {
				t.Fatal(err)
			}
			if current.CurrentAssigneeID != 101 || current.CurrentTeamID != 10 {
				t.Fatalf("rejected target transfer mutated conversation: %+v", current)
			}
		})
	}
}

func TestAutoAssignConversationRestrictsDispatchToCallerTeam(t *testing.T) {
	db := setupConversationWriteScopeReviewDB(t)
	for _, teamID := range []int64{10, 20} {
		now := time.Now()
		if err := db.Create(&models.AgentTeamSchedule{TeamID: teamID, StartAt: now.Add(-time.Hour), EndAt: now.Add(time.Hour), Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&models.AgentProfile{}).Where("user_id = ?", 201).Update("priority_level", 100).Error; err != nil {
		t.Fatal(err)
	}
	aiAgent := &models.AIAgent{Name: "synthetic-ai", TeamIDs: "20,10", Status: enums.StatusOk, ServiceMode: enums.IMConversationServiceModeAIFirst}
	if err := db.Create(aiAgent).Error; err != nil {
		t.Fatal(err)
	}
	conversation := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 10, enums.IMConversationServiceModeAIFirst)
	conversation.AIAgentID = aiAgent.ID
	if err := db.Model(conversation).Update("ai_agent_id", aiAgent.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := ConversationService.AutoAssignConversation(conversation.ID, reviewOperator(101, constants.RoleCodeCsTeamLeader)); err != nil {
		t.Fatalf("team scoped dispatch failed: %v", err)
	}
	current := ConversationService.Get(conversation.ID)
	if current.CurrentAssigneeID <= 0 || current.CurrentAssigneeID == 201 || current.CurrentTeamID != 10 {
		t.Fatalf("dispatch chose an employee outside caller team: assignee=%d team=%d", current.CurrentAssigneeID, current.CurrentTeamID)
	}
}

func TestAutoAssignConversationRejectsCsUserPermissionOverride(t *testing.T) {
	db := setupConversationWriteScopeReviewDB(t)
	now := time.Now()
	if err := db.Create(&models.AgentTeamSchedule{TeamID: 10, StartAt: now.Add(-time.Hour), EndAt: now.Add(time.Hour), Status: enums.StatusOk}).Error; err != nil {
		t.Fatal(err)
	}
	aiAgent := &models.AIAgent{Name: "synthetic-ai", TeamIDs: "10", Status: enums.StatusOk, ServiceMode: enums.IMConversationServiceModeAIFirst}
	if err := db.Create(aiAgent).Error; err != nil {
		t.Fatal(err)
	}
	conversation := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 10, enums.IMConversationServiceModeAIFirst)
	if err := db.Model(conversation).Update("ai_agent_id", aiAgent.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := ConversationService.AutoAssignConversation(conversation.ID, reviewOperator(101, constants.RoleCodeCsUser)); err == nil {
		t.Fatal("cs_user with a direct permission override triggered arbitrary auto-dispatch")
	}
	current := ConversationService.Get(conversation.ID)
	if current.Status != enums.IMConversationStatusPending || current.CurrentAssigneeID != 0 {
		t.Fatalf("denied auto-dispatch mutated conversation: %+v", current)
	}
}

func TestAutoAssignConversationRejectsAIOnlyPendingConversation(t *testing.T) {
	db := setupConversationWriteScopeReviewDB(t)
	conversation := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 20, enums.IMConversationServiceModeAIOnly)
	if err := ConversationService.AutoAssignConversation(conversation.ID, reviewOperator(101, constants.RoleCodeSuperAdmin)); err == nil {
		t.Fatal("AI-only pending conversation entered the human auto-dispatch path")
	}
	current := ConversationService.Get(conversation.ID)
	if current.Status != enums.IMConversationStatusPending || current.CurrentAssigneeID != 0 {
		t.Fatalf("rejected AI-only dispatch mutated conversation: %+v", current)
	}
}

func TestAutoAssignRechecksCallerTeamInFinalDispatchTransaction(t *testing.T) {
	db := setupConversationWriteScopeReviewDB(t)
	if err := db.Create(&models.AgentTeamSchedule{TeamID: 10, StartAt: time.Now().Add(-time.Hour), EndAt: time.Now().Add(time.Hour), Status: enums.StatusOk}).Error; err != nil {
		t.Fatal(err)
	}
	aiAgent := &models.AIAgent{Name: "synthetic-ai", TeamIDs: "10", Status: enums.StatusOk, ServiceMode: enums.IMConversationServiceModeAIFirst}
	if err := db.Create(aiAgent).Error; err != nil {
		t.Fatal(err)
	}
	conversation := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 10, enums.IMConversationServiceModeAIFirst)
	if err := db.Model(conversation).Update("ai_agent_id", aiAgent.ID).Error; err != nil {
		t.Fatal(err)
	}
	interleaved := interleaveAfterPreflightConversationRead(t, db, func(conn gorm.ConnPool) error {
		_, err := conn.ExecContext(context.Background(), "UPDATE t_conversation SET current_team_id = ? WHERE id = ?", 20, conversation.ID)
		return err
	})
	err := ConversationService.AutoAssignConversation(conversation.ID, reviewOperator(101, constants.RoleCodeCsTeamLeader))
	if !interleaved() {
		t.Fatal("conversation team was not changed before the final dispatch read")
	}
	if err == nil {
		t.Fatal("auto-dispatch succeeded after the conversation moved outside the caller's team")
	}
	var current models.Conversation
	if err := db.First(&current, conversation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.Status != enums.IMConversationStatusPending || current.CurrentAssigneeID != 0 || current.CurrentTeamID != 20 {
		t.Fatalf("stale-scope dispatch changed the moved conversation: %+v", current)
	}
}

func TestAutoAssignRevalidatesCandidateEmployeeInFinalDispatchTransaction(t *testing.T) {
	db := setupConversationWriteScopeReviewDB(t)
	if err := db.Create(&models.AgentTeamSchedule{TeamID: 10, StartAt: time.Now().Add(-time.Hour), EndAt: time.Now().Add(time.Hour), Status: enums.StatusOk}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.AgentProfile{}).Where("user_id = ?", 101).Update("auto_assign_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	aiAgent := &models.AIAgent{Name: "synthetic-ai", TeamIDs: "10", Status: enums.StatusOk, ServiceMode: enums.IMConversationServiceModeAIFirst}
	if err := db.Create(aiAgent).Error; err != nil {
		t.Fatal(err)
	}
	conversation := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 10, enums.IMConversationServiceModeAIFirst)
	if err := db.Model(conversation).Update("ai_agent_id", aiAgent.ID).Error; err != nil {
		t.Fatal(err)
	}
	interleaved := interleaveInFinalDispatchTransaction(t, db, func(conn gorm.ConnPool) error {
		_, err := conn.ExecContext(context.Background(), "UPDATE t_user SET user_type = ? WHERE id = ?", enums.UserTypeUser, 102)
		return err
	})
	err := ConversationService.AutoAssignConversation(conversation.ID, reviewOperator(101, constants.RoleCodeCsTeamLeader))
	if !interleaved() {
		t.Fatal("candidate identity was not changed before the final dispatch read")
	}
	if err == nil {
		t.Fatal("auto-dispatch assigned a candidate whose account stopped being an employee")
	}
	var current models.Conversation
	if err := db.First(&current, conversation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if current.Status != enums.IMConversationStatusPending || current.CurrentAssigneeID != 0 {
		t.Fatalf("stale candidate identity was assigned: %+v", current)
	}
}

func TestAutoAssignRevalidatesCandidateEligibilityInFinalDispatchTransaction(t *testing.T) {
	for _, scenario := range []string{"auto-assign-disabled", "service-status-busy"} {
		t.Run(scenario, func(t *testing.T) {
			db := setupConversationWriteScopeReviewDB(t)
			if err := db.Create(&models.AgentTeamSchedule{TeamID: 10, StartAt: time.Now().Add(-time.Hour), EndAt: time.Now().Add(time.Hour), Status: enums.StatusOk}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&models.AgentProfile{}).Where("user_id = ?", 101).Update("auto_assign_enabled", false).Error; err != nil {
				t.Fatal(err)
			}
			aiAgent := &models.AIAgent{Name: "synthetic-ai", TeamIDs: "10", Status: enums.StatusOk, ServiceMode: enums.IMConversationServiceModeAIFirst}
			if err := db.Create(aiAgent).Error; err != nil {
				t.Fatal(err)
			}
			conversation := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 10, enums.IMConversationServiceModeAIFirst)
			if err := db.Model(conversation).Update("ai_agent_id", aiAgent.ID).Error; err != nil {
				t.Fatal(err)
			}
			interleaved := interleaveInFinalDispatchTransaction(t, db, func(conn gorm.ConnPool) error {
				switch scenario {
				case "auto-assign-disabled":
					_, err := conn.ExecContext(context.Background(), "UPDATE t_agent_profile SET auto_assign_enabled = ? WHERE user_id = ?", false, 102)
					return err
				case "service-status-busy":
					_, err := conn.ExecContext(context.Background(), "UPDATE t_agent_profile SET service_status = ? WHERE user_id = ?", enums.ServiceStatusBusy, 102)
					return err
				default:
					return fmt.Errorf("unknown test scenario %q", scenario)
				}
			})
			err := ConversationService.AutoAssignConversation(conversation.ID, reviewOperator(101, constants.RoleCodeCsTeamLeader))
			if !interleaved() {
				t.Fatal("candidate eligibility was not changed before the final dispatch read")
			}
			if err == nil {
				t.Fatalf("auto-dispatch assigned candidate after %s", scenario)
			}
			var current models.Conversation
			if err := db.First(&current, conversation.ID).Error; err != nil {
				t.Fatal(err)
			}
			if current.Status != enums.IMConversationStatusPending || current.CurrentAssigneeID != 0 {
				t.Fatalf("ineligible candidate was assigned after %s: %+v", scenario, current)
			}
		})
	}
}

func TestAutoAssignTeamPoolFallbackRevalidatesCurrentScopeAndState(t *testing.T) {
	for _, scenario := range []string{"operator-team-changed", "conversation-team-changed", "conversation-claimed", "conversation-closed"} {
		t.Run(scenario, func(t *testing.T) {
			db, conversation := setupAutoAssignTeamPoolReview(t)
			interleaved := interleaveAfterDispatchCandidateSelection(t, db, func(conn gorm.ConnPool) error {
				switch scenario {
				case "operator-team-changed":
					_, err := conn.ExecContext(context.Background(), "UPDATE t_agent_profile SET team_id = ? WHERE user_id = ?", 20, 101)
					return err
				case "conversation-team-changed":
					_, err := conn.ExecContext(context.Background(), "UPDATE t_conversation SET current_team_id = ? WHERE id = ?", 20, conversation.ID)
					return err
				case "conversation-claimed":
					_, err := conn.ExecContext(context.Background(), "UPDATE t_conversation SET status = ?, current_assignee_id = ? WHERE id = ?", enums.IMConversationStatusActive, 102, conversation.ID)
					return err
				case "conversation-closed":
					_, err := conn.ExecContext(context.Background(), "UPDATE t_conversation SET status = ? WHERE id = ?", enums.IMConversationStatusClosed, conversation.ID)
					return err
				default:
					return fmt.Errorf("unknown test scenario %q", scenario)
				}
			})
			err := ConversationService.AutoAssignConversation(conversation.ID, reviewOperator(101, constants.RoleCodeCsTeamLeader))
			if !interleaved() {
				t.Fatal("scope or conversation state was not changed after candidate selection")
			}
			if err == nil {
				t.Fatalf("team-pool fallback overwrote concurrent change in scenario %s", scenario)
			}
			var current models.Conversation
			if err := db.First(&current, conversation.ID).Error; err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "operator-team-changed":
				if current.Status != enums.IMConversationStatusPending || current.CurrentTeamID != 10 || current.CurrentAssigneeID != 0 {
					t.Fatalf("operator team change was overwritten by team-pool fallback: %+v", current)
				}
			case "conversation-team-changed":
				if current.Status != enums.IMConversationStatusPending || current.CurrentTeamID != 20 || current.CurrentAssigneeID != 0 {
					t.Fatalf("conversation team change was overwritten by team-pool fallback: %+v", current)
				}
			case "conversation-claimed":
				if current.Status != enums.IMConversationStatusActive || current.CurrentAssigneeID != 102 {
					t.Fatalf("claim was overwritten by team-pool fallback: %+v", current)
				}
			case "conversation-closed":
				if current.Status != enums.IMConversationStatusClosed {
					t.Fatalf("close was overwritten by team-pool fallback: %+v", current)
				}
			}
			var eventCount int64
			if err := db.Model(&models.ConversationEventLog{}).Where("conversation_id = ?", conversation.ID).Count(&eventCount).Error; err != nil {
				t.Fatal(err)
			}
			if eventCount != 0 {
				t.Fatalf("rejected team-pool fallback wrote %d conversation events", eventCount)
			}
		})
	}
}

func setupAutoAssignTeamPoolReview(t *testing.T) (*gorm.DB, *models.Conversation) {
	t.Helper()
	db := setupConversationWriteScopeReviewDB(t)
	if err := db.Create(&models.AgentTeamSchedule{TeamID: 10, StartAt: time.Now().Add(-time.Hour), EndAt: time.Now().Add(time.Hour), Status: enums.StatusOk}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.AgentProfile{}).Where("user_id IN ?", []int64{101, 102}).Update("auto_assign_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	aiAgent := &models.AIAgent{Name: "synthetic-ai", TeamIDs: "10", Status: enums.StatusOk, ServiceMode: enums.IMConversationServiceModeAIFirst}
	if err := db.Create(aiAgent).Error; err != nil {
		t.Fatal(err)
	}
	conversation := createReviewConversation(t, db, enums.IMConversationStatusPending, 0, 10, enums.IMConversationServiceModeAIFirst)
	if err := db.Model(conversation).Update("ai_agent_id", aiAgent.ID).Error; err != nil {
		t.Fatal(err)
	}
	return db, conversation
}

func interleaveAfterDispatchCandidateSelection(t *testing.T, db *gorm.DB, mutate func(gorm.ConnPool) error) func() bool {
	t.Helper()
	interleaved := false
	callbackErr := error(nil)
	callbackName := "test:interleave_dispatch_pool_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(query *gorm.DB) {
		if query.Statement.Table != "t_agent_profile" || interleaved {
			return
		}
		if _, inTransaction := query.Statement.ConnPool.(gorm.TxCommitter); inTransaction {
			return
		}
		interleaved = true
		callbackErr = mutate(query.Statement.ConnPool)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })
	return func() bool {
		if callbackErr != nil {
			t.Errorf("interleave team-pool dispatch state: %v", callbackErr)
		}
		return interleaved
	}
}

func interleaveAfterPreflightConversationRead(t *testing.T, db *gorm.DB, mutate func(gorm.ConnPool) error) func() bool {
	t.Helper()
	transactionConversationReads := 0
	interleaved := false
	callbackErr := error(nil)
	callbackName := "test:interleave_final_dispatch_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(query *gorm.DB) {
		if query.Statement.Table != "t_conversation" {
			return
		}
		if _, inTransaction := query.Statement.ConnPool.(gorm.TxCommitter); !inTransaction {
			if transactionConversationReads == 0 || interleaved {
				return
			}
			interleaved = true
			callbackErr = mutate(query.Statement.ConnPool)
			return
		}
		transactionConversationReads++
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })
	return func() bool {
		if callbackErr != nil {
			t.Errorf("interleave final dispatch state: %v", callbackErr)
		}
		return interleaved
	}
}

func interleaveInFinalDispatchTransaction(t *testing.T, db *gorm.DB, mutate func(gorm.ConnPool) error) func() bool {
	t.Helper()
	transactionConversationReads := 0
	interleaved := false
	callbackErr := error(nil)
	callbackName := "test:interleave_final_dispatch_tx_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(query *gorm.DB) {
		if query.Statement.Table != "t_conversation" {
			return
		}
		if _, inTransaction := query.Statement.ConnPool.(gorm.TxCommitter); !inTransaction {
			return
		}
		transactionConversationReads++
		if transactionConversationReads != 2 || interleaved {
			return
		}
		interleaved = true
		callbackErr = mutate(query.Statement.ConnPool)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })
	return func() bool {
		if callbackErr != nil {
			t.Errorf("interleave final dispatch state: %v", callbackErr)
		}
		return interleaved
	}
}
