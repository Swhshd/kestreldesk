package services_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/Swhshd/kestreldesk/internal/services"
	"github.com/mlogclub/simple/sqls"
)

func TestTicketListAndSummaryUseAuthenticatedAgentScope(t *testing.T) {
	setupTicketTestDB(t)

	teamID := createTicketScopeTeam(t, "support-a")
	otherTeamID := createTicketScopeTeam(t, "support-b")
	callerID := createTicketScopeAgent(t, "caller", teamID, enums.StatusOk)
	teammateID := createTicketScopeAgent(t, "teammate", teamID, enums.StatusOk)
	disabledAgentID := createTicketScopeAgent(t, "disabled", teamID, enums.StatusDisabled)
	otherTeamAgentID := createTicketScopeAgent(t, "other-team", otherTeamID, enums.StatusOk)
	caller := ticketScopePrincipal(callerID, constants.RoleCodeCsUser)

	createTicketForScopeTest(t, "caller open", callerID, enums.TicketStatusPending)
	createTicketForScopeTest(t, "caller done", callerID, enums.TicketStatusDone)
	createTicketForScopeTest(t, "teammate", teammateID, enums.TicketStatusInProgress)
	createTicketForScopeTest(t, "disabled agent", disabledAgentID, enums.TicketStatusPending)
	createTicketForScopeTest(t, "other team", otherTeamAgentID, enums.TicketStatusPending)
	createTicketForScopeTest(t, "unassigned", 0, enums.TicketStatusPending)

	list, err := services.TicketService.FindPageAggregateByCnd(sqls.NewCnd().Page(1, 1), caller)
	if err != nil {
		t.Fatalf("FindPageAggregateByCnd() error = %v", err)
	}
	if got := len(list.List); got != 1 {
		t.Fatalf("expected one row on page, got %d", got)
	}
	if list.Paging.Total != 2 {
		t.Fatalf("expected page total to count only the caller's two tickets, got %d", list.Paging.Total)
	}
	for _, item := range list.List {
		if item.CurrentAssigneeID != callerID {
			t.Fatalf("list exposed ticket assigned to %d, caller is %d", item.CurrentAssigneeID, callerID)
		}
	}

	filtered, err := services.TicketService.FindPageAggregateByCnd(sqls.NewCnd().Eq("current_assignee_id", teammateID).Page(1, 10), caller)
	if err != nil {
		t.Fatalf("filtered FindPageAggregateByCnd() error = %v", err)
	}
	if len(filtered.List) != 0 || filtered.Paging.Total != 0 {
		t.Fatalf("client assignee filter widened caller scope: rows=%d total=%d", len(filtered.List), filtered.Paging.Total)
	}
	keyword := "%teammate%"
	searched, err := services.TicketService.FindPageAggregateByCnd(sqls.NewCnd().Where(
		"ticket_no LIKE ? OR title LIKE ? OR description LIKE ?", keyword, keyword, keyword,
	).Page(1, 10), caller)
	if err != nil {
		t.Fatalf("search FindPageAggregateByCnd() error = %v", err)
	}
	if len(searched.List) != 0 || searched.Paging.Total != 0 {
		t.Fatalf("keyword OR filter widened caller scope: rows=%d total=%d", len(searched.List), searched.Paging.Total)
	}

	summary, err := services.TicketService.GetSummary(caller)
	if err != nil {
		t.Fatalf("GetSummary() error = %v", err)
	}
	if summary.All != 2 || summary.Pending != 1 || summary.Done != 1 || summary.Mine != 2 || summary.Unassigned != 0 {
		t.Fatalf("summary included tickets outside caller scope: %+v", summary)
	}
}

func TestTicketTeamLeaderReadsOnlyActiveProfilesInAuthoritativeTeam(t *testing.T) {
	setupTicketTestDB(t)

	teamID := createTicketScopeTeam(t, "leader-team")
	otherTeamID := createTicketScopeTeam(t, "outside-team")
	leaderID := createTicketScopeAgent(t, "leader", teamID, enums.StatusOk)
	teammateID := createTicketScopeAgent(t, "leader-teammate", teamID, enums.StatusOk)
	disabledAgentID := createTicketScopeAgent(t, "leader-disabled", teamID, enums.StatusDisabled)
	otherTeamAgentID := createTicketScopeAgent(t, "leader-other-team", otherTeamID, enums.StatusOk)
	leader := ticketScopePrincipal(leaderID, constants.RoleCodeCsTeamLeader)

	createTicketForScopeTest(t, "leader ticket", leaderID, enums.TicketStatusPending)
	createTicketForScopeTest(t, "teammate ticket", teammateID, enums.TicketStatusInProgress)
	createTicketForScopeTest(t, "disabled profile ticket", disabledAgentID, enums.TicketStatusPending)
	createTicketForScopeTest(t, "other team ticket", otherTeamAgentID, enums.TicketStatusDone)
	createTicketForScopeTest(t, "unassigned ticket", 0, enums.TicketStatusPending)

	list, err := services.TicketService.FindPageAggregateByCnd(sqls.NewCnd().Page(1, 1), leader)
	if err != nil {
		t.Fatalf("FindPageAggregateByCnd() error = %v", err)
	}
	if len(list.List) != 1 || list.Paging.Total != 2 {
		t.Fatalf("leader page should contain one of two team tickets, rows=%d total=%d", len(list.List), list.Paging.Total)
	}

	summary, err := services.TicketService.GetSummary(leader)
	if err != nil {
		t.Fatalf("GetSummary() error = %v", err)
	}
	if summary.All != 2 || summary.Pending != 1 || summary.InProgress != 1 || summary.Done != 0 || summary.Unassigned != 0 {
		t.Fatalf("leader summary included tickets outside active team scope: %+v", summary)
	}
}

func TestTicketDetailAndProgressHistoryRequireCurrentReadScope(t *testing.T) {
	setupTicketTestDB(t)

	teamID := createTicketScopeTeam(t, "detail-team")
	callerID := createTicketScopeAgent(t, "detail-caller", teamID, enums.StatusOk)
	peerID := createTicketScopeAgent(t, "detail-peer", teamID, enums.StatusOk)
	caller := ticketScopePrincipal(callerID, constants.RoleCodeCsUser)
	owned := createTicketForScopeTest(t, "owned detail", callerID, enums.TicketStatusPending)
	other := createTicketForScopeTest(t, "peer detail", peerID, enums.TicketStatusPending)
	if err := sqls.DB().Create(&models.TicketProgress{TicketID: owned.ID, Content: "owned history", AuthorID: callerID}).Error; err != nil {
		t.Fatalf("create owned progress: %v", err)
	}
	if err := sqls.DB().Create(&models.TicketProgress{TicketID: other.ID, Content: "peer history", AuthorID: peerID}).Error; err != nil {
		t.Fatalf("create peer progress: %v", err)
	}

	detail, err := services.TicketService.GetDetail(owned.ID, caller)
	if err != nil {
		t.Fatalf("GetDetail(owned) error = %v", err)
	}
	if detail == nil || detail.Ticket == nil || detail.Ticket.ID != owned.ID || len(detail.Progresses) != 1 || detail.Progresses[0].Content != "owned history" {
		t.Fatalf("unexpected authorized detail: %+v", detail)
	}
	if _, err := services.TicketService.GetDetail(other.ID, caller); err == nil {
		t.Fatal("GetDetail() exposed a peer ticket")
	}

	history, err := services.TicketService.GetProgressHistory(owned.ID, caller)
	if err != nil {
		t.Fatalf("GetProgressHistory(owned) error = %v", err)
	}
	if len(history) != 1 || history[0].Content != "owned history" {
		t.Fatalf("unexpected owned history: %+v", history)
	}
	if _, err := services.TicketService.GetProgressHistory(other.ID, caller); err == nil {
		t.Fatal("GetProgressHistory() exposed a peer ticket")
	}
}

func TestTicketReadScopeFailsClosedForInvalidEmployeeScope(t *testing.T) {
	setupTicketTestDB(t)

	validTeamID := createTicketScopeTeam(t, "valid-team")
	disabledTeamID := createTicketScopeTeam(t, "disabled-team")
	if err := sqls.DB().Model(&models.AgentTeam{}).Where("id = ?", disabledTeamID).Update("status", enums.StatusDisabled).Error; err != nil {
		t.Fatalf("disable team: %v", err)
	}

	tests := []struct {
		name    string
		userID  int64
		teamID  int64
		profile enums.Status
		missing bool
	}{
		{name: "missing profile", missing: true},
		{name: "disabled profile", teamID: validTeamID, profile: enums.StatusDisabled},
		{name: "profile without team", teamID: 0, profile: enums.StatusOk},
		{name: "disabled team", teamID: disabledTeamID, profile: enums.StatusOk},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			userID := test.userID
			if userID <= 0 {
				if test.missing {
					userID = createTicketScopeEmployeeWithoutProfile(t, test.name)
				} else {
					userID = createTicketScopeAgent(t, test.name, test.teamID, test.profile)
				}
			}
			createTicketForScopeTest(t, test.name+" ticket", userID, enums.TicketStatusPending)
			principal := ticketScopePrincipal(userID, constants.RoleCodeCsUser)
			if _, err := services.TicketService.FindPageAggregateByCnd(sqls.NewCnd().Page(1, 10), principal); err == nil {
				t.Fatal("read scope accepted a missing, disabled, or invalid employee profile/team")
			}
			if _, err := services.TicketService.GetSummary(principal); err == nil {
				t.Fatal("summary accepted a missing, disabled, or invalid employee profile/team")
			}
		})
	}
}

func TestTicketReadPermissionCannotBeInferredFromPermissionCode(t *testing.T) {
	setupTicketTestDB(t)
	teamID := createTicketScopeTeam(t, "role-required-team")
	userID := createTicketScopeAgent(t, "permission-only", teamID, enums.StatusOk)
	createTicketForScopeTest(t, "permission-only ticket", userID, enums.TicketStatusPending)
	principal := ticketScopePrincipal(userID, "")

	if _, err := services.TicketService.FindPageAggregateByCnd(sqls.NewCnd().Page(1, 10), principal); err == nil {
		t.Fatal("Ticket view permission without an approved role produced an object scope")
	}
}

func TestTicketAdministratorRetainsGlobalReadScope(t *testing.T) {
	setupTicketTestDB(t)
	adminID := createTicketScopeEmployeeWithoutProfile(t, "admin")
	createTicketForScopeTest(t, "admin own", adminID, enums.TicketStatusPending)
	createTicketForScopeTest(t, "admin other", 9001, enums.TicketStatusDone)
	createTicketForScopeTest(t, "admin unassigned", 0, enums.TicketStatusPending)
	admin := ticketScopePrincipal(adminID, constants.RoleCodeAdmin)

	list, err := services.TicketService.FindPageAggregateByCnd(sqls.NewCnd().Page(1, 10), admin)
	if err != nil {
		t.Fatalf("FindPageAggregateByCnd() error = %v", err)
	}
	if len(list.List) != 3 || list.Paging.Total != 3 {
		t.Fatalf("administrator should retain global Ticket read access, rows=%d total=%d", len(list.List), list.Paging.Total)
	}
	summary, err := services.TicketService.GetSummary(admin)
	if err != nil {
		t.Fatalf("GetSummary() error = %v", err)
	}
	if summary.All != 3 || summary.Unassigned != 1 {
		t.Fatalf("administrator summary should include all Tickets: %+v", summary)
	}
}

func createTicketScopeTeam(t *testing.T, name string) int64 {
	t.Helper()
	team := &models.AgentTeam{Name: name, Status: enums.StatusOk}
	if err := sqls.DB().Create(team).Error; err != nil {
		t.Fatalf("create team %q: %v", name, err)
	}
	return team.ID
}

func createTicketScopeAgent(t *testing.T, name string, teamID int64, profileStatus enums.Status) int64 {
	t.Helper()
	user := &models.User{
		Username: fmt.Sprintf("ticket-scope-%s-%d", name, time.Now().UnixNano()),
		Nickname: name,
		UserType: enums.UserTypeEmployee,
		Status:   enums.StatusOk,
	}
	if err := repositories.UserRepository.Create(sqls.DB(), user); err != nil {
		t.Fatalf("create agent user %q: %v", name, err)
	}
	profile := &models.AgentProfile{
		UserID:      user.ID,
		TeamID:      teamID,
		AgentCode:   fmt.Sprintf("ticket-scope-%s-%d", name, time.Now().UnixNano()),
		DisplayName: name,
		Status:      profileStatus,
	}
	if err := sqls.DB().Create(profile).Error; err != nil {
		t.Fatalf("create agent profile %q: %v", name, err)
	}
	return user.ID
}

func createTicketScopeEmployeeWithoutProfile(t *testing.T, name string) int64 {
	t.Helper()
	user := &models.User{
		Username: fmt.Sprintf("ticket-scope-%s-%d", name, time.Now().UnixNano()),
		Nickname: name,
		UserType: enums.UserTypeEmployee,
		Status:   enums.StatusOk,
	}
	if err := repositories.UserRepository.Create(sqls.DB(), user); err != nil {
		t.Fatalf("create employee %q: %v", name, err)
	}
	return user.ID
}

func ticketScopePrincipal(userID int64, role string) *dto.AuthPrincipal {
	return &dto.AuthPrincipal{
		UserID:      userID,
		UserType:    enums.UserTypeEmployee,
		Status:      enums.StatusOk,
		Roles:       []string{role},
		Permissions: []string{constants.PermissionTicketView.Code},
	}
}

func createTestTicketAdminOperator(t *testing.T, name string) *dto.AuthPrincipal {
	t.Helper()
	principal := ticketScopePrincipal(createTicketScopeEmployeeWithoutProfile(t, name), constants.RoleCodeAdmin)
	principal.Username = name
	return principal
}

func createTicketForScopeTest(t *testing.T, title string, assigneeID int64, status enums.TicketStatus) *models.Ticket {
	t.Helper()
	ticket := &models.Ticket{
		TicketNo:          fmt.Sprintf("scope-%d", time.Now().UnixNano()),
		Title:             title,
		Description:       title,
		Status:            status,
		CurrentAssigneeID: assigneeID,
	}
	if err := repositories.TicketRepository.Create(sqls.DB(), ticket); err != nil {
		t.Fatalf("create Ticket %q: %v", title, err)
	}
	return ticket
}
