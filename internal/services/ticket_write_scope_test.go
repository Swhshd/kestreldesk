package services_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/request"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/Swhshd/kestreldesk/internal/services"
	"github.com/mlogclub/simple/sqls"
)

func TestTicketStatusAndProgressWritesRequireCurrentTicketScope(t *testing.T) {
	setupTicketTestDB(t)
	teamID := createTicketScopeTeam(t, "ticket-write-team")
	otherTeamID := createTicketScopeTeam(t, "ticket-write-outside")
	agentID := createTicketScopeAgent(t, "ticket-write-agent", teamID, enums.StatusOk)
	peerID := createTicketScopeAgent(t, "ticket-write-peer", teamID, enums.StatusOk)
	otherID := createTicketScopeAgent(t, "ticket-write-other", otherTeamID, enums.StatusOk)
	agent := ticketWritePrincipal(agentID, constants.RoleCodeCsUser)

	owned := createTicketForScopeTest(t, "owned", agentID, enums.TicketStatusPending)
	peer := createTicketForScopeTest(t, "peer", peerID, enums.TicketStatusPending)
	other := createTicketForScopeTest(t, "other team", otherID, enums.TicketStatusPending)
	unassigned := createTicketForScopeTest(t, "unassigned", 0, enums.TicketStatusPending)

	if err := services.TicketService.ChangeStatus(request.ChangeTicketStatusRequest{TicketID: owned.ID, Status: string(enums.TicketStatusInProgress)}, agent); err != nil {
		t.Fatalf("agent should change status on own ticket: %v", err)
	}
	if _, err := services.TicketService.AddProgress(request.CreateTicketProgressRequest{TicketID: owned.ID, Content: "own progress"}, agent); err != nil {
		t.Fatalf("agent should add progress on own ticket: %v", err)
	}

	for _, item := range []*models.Ticket{peer, other, unassigned} {
		t.Run(item.Title, func(t *testing.T) {
			if err := services.TicketService.ChangeStatus(request.ChangeTicketStatusRequest{TicketID: item.ID, Status: string(enums.TicketStatusDone)}, agent); err == nil {
				t.Fatalf("agent changed status outside current Ticket scope: %s", item.Title)
			}
			if _, err := services.TicketService.AddProgress(request.CreateTicketProgressRequest{TicketID: item.ID, Content: "out of scope"}, agent); err == nil {
				t.Fatalf("agent added progress outside current Ticket scope: %s", item.Title)
			}
		})
	}
}

func TestTicketAssignmentAndMixedUpdateRespectRoleBoundaries(t *testing.T) {
	setupTicketTestDB(t)
	teamID := createTicketScopeTeam(t, "ticket-assignment-team")
	otherTeamID := createTicketScopeTeam(t, "ticket-assignment-outside")
	leaderID := createTicketScopeAgent(t, "ticket-assignment-leader", teamID, enums.StatusOk)
	memberID := createTicketScopeAgent(t, "ticket-assignment-member", teamID, enums.StatusOk)
	nextMemberID := createTicketScopeAgent(t, "ticket-assignment-next", teamID, enums.StatusOk)
	otherID := createTicketScopeAgent(t, "ticket-assignment-other", otherTeamID, enums.StatusOk)
	disabledMemberID := createTicketScopeAgent(t, "ticket-assignment-disabled", teamID, enums.StatusDisabled)
	leader := ticketWritePrincipal(leaderID, constants.RoleCodeCsTeamLeader)
	agent := ticketWritePrincipal(memberID, constants.RoleCodeCsUser)
	agent.Permissions = append(agent.Permissions, constants.PermissionTicketAssign.Code)

	agentAssignedTicket := createTicketForScopeTest(t, "agent assigned", memberID, enums.TicketStatusPending)
	agentUpdateTicket := createTicketForScopeTest(t, "agent update", memberID, enums.TicketStatusPending)
	teamTicket := createTicketForScopeTest(t, "team assigned", memberID, enums.TicketStatusPending)
	otherTicket := createTicketForScopeTest(t, "other team assigned", otherID, enums.TicketStatusPending)
	unassignedTicket := createTicketForScopeTest(t, "leader unassigned", 0, enums.TicketStatusPending)

	for name, assignment := range map[string]request.AssignTicketRequest{
		"self target":          {TicketID: agentAssignedTicket.ID, ToUserID: memberID},
		"same-team target":     {TicketID: agentAssignedTicket.ID, ToUserID: nextMemberID},
		"other-team target":    {TicketID: agentAssignedTicket.ID, ToUserID: otherID},
		"unassign null target": {TicketID: agentAssignedTicket.ID, ToUserID: 0},
	} {
		t.Run("cs_user "+name, func(t *testing.T) {
			if err := services.TicketService.AssignTicket(assignment, agent); err == nil {
				t.Error("cs_user reassigned or unassigned a Ticket despite holding ticket.assign")
			}
		})
	}
	if err := services.TicketService.AssignTicket(request.AssignTicketRequest{TicketID: teamTicket.ID, ToUserID: nextMemberID}, leader); err != nil {
		t.Fatalf("team leader could not reassign an in-scope Ticket to an active teammate: %v", err)
	}
	for name, assignment := range map[string]request.AssignTicketRequest{
		"cross-team target":   {TicketID: teamTicket.ID, ToUserID: otherID},
		"disabled target":     {TicketID: teamTicket.ID, ToUserID: disabledMemberID},
		"null target":         {TicketID: teamTicket.ID, ToUserID: 0},
		"out-of-scope Ticket": {TicketID: otherTicket.ID, ToUserID: memberID},
		"unassigned Ticket":   {TicketID: unassignedTicket.ID, ToUserID: memberID},
	} {
		t.Run(name, func(t *testing.T) {
			if err := services.TicketService.AssignTicket(assignment, leader); err == nil {
				t.Errorf("team leader assignment succeeded for %s", name)
			}
		})
	}

	for _, roleCode := range []string{constants.RoleCodeAdmin, constants.RoleCodeSuperAdmin} {
		t.Run(roleCode+" global assignment", func(t *testing.T) {
			admin := ticketWritePrincipal(leaderID, roleCode)
			adminGlobalTicket := createTicketForScopeTest(t, roleCode+" global", otherID, enums.TicketStatusPending)
			if err := services.TicketService.AssignTicket(request.AssignTicketRequest{TicketID: adminGlobalTicket.ID, ToUserID: memberID}, admin); err != nil {
				t.Fatalf("%s global assignment behavior regressed: %v", roleCode, err)
			}
		})
	}

	if err := services.TicketService.UpdateTicket(request.UpdateTicketRequest{
		TicketID:          teamTicket.ID,
		Title:             "metadata updated",
		Description:       "safe in-scope edit",
		CurrentAssigneeID: nextMemberID,
	}, leader); err != nil {
		t.Fatalf("team leader safe update with unchanged assignee failed: %v", err)
	}
	if err := services.TicketService.UpdateTicket(request.UpdateTicketRequest{
		TicketID:          teamTicket.ID,
		Title:             "assignment bypass",
		Description:       "unsafe assignee change",
		CurrentAssigneeID: otherID,
	}, leader); err == nil {
		t.Fatal("team leader changed assignee through mixed UpdateTicket DTO")
	}
	if err := services.TicketService.UpdateTicket(request.UpdateTicketRequest{
		TicketID:          agentUpdateTicket.ID,
		Title:             "agent safe metadata",
		Description:       "assignee unchanged",
		CurrentAssigneeID: memberID,
	}, agent); err != nil {
		t.Fatalf("cs_user safe in-scope metadata update failed: %v", err)
	}
	if err := services.TicketService.UpdateTicket(request.UpdateTicketRequest{
		TicketID:          agentUpdateTicket.ID,
		Title:             "agent assignment bypass",
		Description:       "unsafe assignee change",
		CurrentAssigneeID: nextMemberID,
	}, agent); err == nil {
		t.Error("cs_user changed assignee through mixed UpdateTicket DTO")
	}
}

func TestTicketCreationFromConversationDerivesOwnershipAndRequiresActiveOwnConversation(t *testing.T) {
	setupTicketTestDB(t)
	teamID := createTicketScopeTeam(t, "ticket-create-team")
	otherTeamID := createTicketScopeTeam(t, "ticket-create-outside")
	agentID := createTicketScopeAgent(t, "ticket-create-agent", teamID, enums.StatusOk)
	peerID := createTicketScopeAgent(t, "ticket-create-peer", teamID, enums.StatusOk)
	otherID := createTicketScopeAgent(t, "ticket-create-other", otherTeamID, enums.StatusOk)
	agent := ticketWritePrincipal(agentID, constants.RoleCodeCsUser)
	customerID := createTestCustomer(t, "ticket-create-customer")
	otherCustomerID := createTestCustomer(t, "ticket-create-other-customer")

	ownedConversation := createTicketWriteConversation(t, customerID, enums.IMConversationStatusActive, agentID, teamID)
	created, err := services.TicketService.CreateFromConversation(request.CreateTicketFromConversationRequest{ConversationID: ownedConversation, Title: "own conversation"}, agent)
	if err != nil {
		t.Fatalf("agent could not create from active own-assigned conversation: %v", err)
	}
	if created.CustomerID != customerID || created.ConversationID != ownedConversation || created.CurrentAssigneeID != agentID {
		t.Errorf("Ticket ownership was not derived from authorized Conversation: %+v", created)
	}

	peerConversation := createTicketWriteConversation(t, customerID, enums.IMConversationStatusActive, peerID, teamID)
	otherConversation := createTicketWriteConversation(t, customerID, enums.IMConversationStatusActive, otherID, otherTeamID)
	waitingConversation := createTicketWriteConversation(t, customerID, enums.IMConversationStatusPending, 0, teamID)
	closedConversation := createTicketWriteConversation(t, customerID, enums.IMConversationStatusClosed, agentID, teamID)
	for name, conversationID := range map[string]int64{
		"same-team peer conversation": peerConversation,
		"other-team conversation":     otherConversation,
		"waiting conversation":        waitingConversation,
		"closed conversation":         closedConversation,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := services.TicketService.CreateFromConversation(request.CreateTicketFromConversationRequest{ConversationID: conversationID, Title: name}, agent)
			if err == nil {
				t.Errorf("created Ticket from unauthorized %s", name)
			}
		})
	}
	for name, assigneeID := range map[string]int64{
		"same-team assignee":  peerID,
		"other-team assignee": otherID,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := services.TicketService.CreateFromConversation(request.CreateTicketFromConversationRequest{
				ConversationID:    ownedConversation,
				Title:             name,
				CurrentAssigneeID: assigneeID,
			}, agent); err == nil {
				t.Errorf("client-supplied %s overrode authorized conversation ownership", name)
			}
		})
	}

	createdWithoutAssignee, err := services.TicketService.CreateFromConversation(request.CreateTicketFromConversationRequest{
		ConversationID:    ownedConversation,
		Title:             "explicit unassigned request",
		CurrentAssigneeID: 0,
	}, agent)
	if err != nil {
		t.Fatalf("zero assignee request should derive the authenticated agent: %v", err)
	}
	if createdWithoutAssignee.CurrentAssigneeID != agentID {
		t.Errorf("zero assignee request produced an unassigned or wrong-owner Ticket: %+v", createdWithoutAssignee)
	}

	manual := request.CreateTicketRequest{Title: "manual agent ticket", Description: "must not bypass Conversation scope", CurrentAssigneeID: agentID}
	if _, err := services.TicketService.CreateTicket(manual, agent); err == nil {
		t.Error("cs_user used generic manual creation to bypass conversation-scoped creation")
	}
	if otherCustomerID == customerID {
		t.Fatal("test customers must differ")
	}
}

func TestTicketCreateFromConversationRejectsConflictingCustomerIDFromJSON(t *testing.T) {
	setupTicketTestDB(t)
	teamID := createTicketScopeTeam(t, "ticket-create-customer-team")
	agentID := createTicketScopeAgent(t, "ticket-create-customer-agent", teamID, enums.StatusOk)
	agent := ticketWritePrincipal(agentID, constants.RoleCodeCsUser)
	customerID := createTestCustomer(t, "ticket-create-authoritative-customer")
	otherCustomerID := createTestCustomer(t, "ticket-create-client-customer")
	conversationID := createTicketWriteConversation(t, customerID, enums.IMConversationStatusActive, agentID, teamID)

	body, err := json.Marshal(map[string]any{
		"conversationId": conversationID,
		"customerId":     otherCustomerID,
		"title":          "conflicting customer",
	})
	if err != nil {
		t.Fatal(err)
	}
	var req request.CreateTicketFromConversationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if _, err := services.TicketService.CreateFromConversation(req, agent); err == nil {
		t.Fatal("conflicting client-supplied customerId must be rejected")
	}

	body, err = json.Marshal(map[string]any{
		"conversationId":    conversationID,
		"currentAssigneeId": nil,
		"title":             "null assignee derives owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	var nullAssigneeReq request.CreateTicketFromConversationRequest
	if err := json.Unmarshal(body, &nullAssigneeReq); err != nil {
		t.Fatal(err)
	}
	created, err := services.TicketService.CreateFromConversation(nullAssigneeReq, agent)
	if err != nil {
		t.Fatalf("null assignee request should derive the authenticated agent: %v", err)
	}
	if created.CurrentAssigneeID != agentID {
		t.Fatalf("null assignee request produced an unassigned or wrong-owner Ticket: %+v", created)
	}
}

func TestTicketCreateAssigneeOptionsRespectTeamAndGlobalScopes(t *testing.T) {
	setupTicketTestDB(t)
	teamID := createTicketScopeTeam(t, "ticket-options-team")
	otherTeamID := createTicketScopeTeam(t, "ticket-options-outside")
	leaderID := createTicketScopeAgent(t, "ticket-options-leader", teamID, enums.StatusOk)
	memberID := createTicketScopeAgent(t, "ticket-options-member", teamID, enums.StatusOk)
	disabledProfileID := createTicketScopeAgent(t, "ticket-options-disabled-profile", teamID, enums.StatusDisabled)
	inactiveUserID := createTicketScopeAgent(t, "ticket-options-inactive-user", teamID, enums.StatusOk)
	if err := sqls.DB().Model(&models.User{}).Where("id = ?", inactiveUserID).Update("status", enums.StatusDisabled).Error; err != nil {
		t.Fatalf("disable assignee account: %v", err)
	}
	otherTeamAgentID := createTicketScopeAgent(t, "ticket-options-other-team", otherTeamID, enums.StatusOk)

	teamOptions, err := services.TicketService.GetCreateAssigneeOptions(ticketWritePrincipal(leaderID, constants.RoleCodeCsTeamLeader))
	if err != nil {
		t.Fatalf("load team leader assignee options: %v", err)
	}
	teamOptionIDs := make(map[int64]bool, len(teamOptions))
	for _, profile := range teamOptions {
		teamOptionIDs[profile.UserID] = true
	}
	if len(teamOptions) != 2 || !teamOptionIDs[leaderID] || !teamOptionIDs[memberID] {
		t.Fatalf("team options should include only active same-team agents: %+v", teamOptions)
	}
	if teamOptionIDs[disabledProfileID] || teamOptionIDs[inactiveUserID] || teamOptionIDs[otherTeamAgentID] {
		t.Fatalf("team options included disabled or out-of-team agents: %+v", teamOptions)
	}

	if _, err := services.TicketService.GetCreateAssigneeOptions(ticketWritePrincipal(memberID, constants.RoleCodeCsUser)); err == nil {
		t.Fatal("ordinary cs_user should not load an assignee selector list")
	}

	globalOptions, err := services.TicketService.GetCreateAssigneeOptions(ticketWritePrincipal(leaderID, constants.RoleCodeSuperAdmin))
	if err != nil {
		t.Fatalf("load global administrator assignee options: %v", err)
	}
	globalOptionIDs := make(map[int64]bool, len(globalOptions))
	for _, profile := range globalOptions {
		globalOptionIDs[profile.UserID] = true
	}
	if len(globalOptions) != 5 || !globalOptionIDs[disabledProfileID] || !globalOptionIDs[inactiveUserID] || !globalOptionIDs[otherTeamAgentID] {
		t.Fatalf("global administrator behavior did not preserve the existing all-profile list: %+v", globalOptions)
	}
}

func TestTicketManualCreateAndConversationCreateRespectLeaderTeamScope(t *testing.T) {
	setupTicketTestDB(t)
	teamID := createTicketScopeTeam(t, "ticket-leader-create-team")
	otherTeamID := createTicketScopeTeam(t, "ticket-leader-create-outside")
	leaderID := createTicketScopeAgent(t, "ticket-leader-create-leader", teamID, enums.StatusOk)
	memberID := createTicketScopeAgent(t, "ticket-leader-create-member", teamID, enums.StatusOk)
	otherID := createTicketScopeAgent(t, "ticket-leader-create-other", otherTeamID, enums.StatusOk)
	leader := ticketWritePrincipal(leaderID, constants.RoleCodeCsTeamLeader)
	customerID := createTestCustomer(t, "ticket-leader-create-customer")

	created, err := services.TicketService.CreateTicket(request.CreateTicketRequest{
		Title:             "leader manual",
		Description:       "same-team assignee",
		CustomerID:        customerID,
		CurrentAssigneeID: memberID,
	}, leader)
	if err != nil || created.CurrentAssigneeID != memberID {
		t.Fatalf("leader manual create failed for active same-team assignee: Ticket=%+v err=%v", created, err)
	}
	for name, assigneeID := range map[string]int64{"cross-team assignee": otherID, "unassigned": 0} {
		t.Run(name, func(t *testing.T) {
			_, err := services.TicketService.CreateTicket(request.CreateTicketRequest{Title: name, Description: name, CurrentAssigneeID: assigneeID}, leader)
			if err == nil {
				t.Errorf("leader created manual Ticket with %s", name)
			}
		})
	}

	activeOwnConversation := createTicketWriteConversation(t, customerID, enums.IMConversationStatusActive, memberID, teamID)
	createdFromTeamConversation, err := services.TicketService.CreateFromConversation(request.CreateTicketFromConversationRequest{
		ConversationID: activeOwnConversation,
		Title:          "leader team conversation",
	}, leader)
	if err != nil || createdFromTeamConversation.CurrentAssigneeID != memberID {
		t.Errorf("leader could not create from active team Conversation with derived assignee: Ticket=%+v err=%v", createdFromTeamConversation, err)
	}

	otherConversation := createTicketWriteConversation(t, customerID, enums.IMConversationStatusActive, otherID, otherTeamID)
	if _, err := services.TicketService.CreateFromConversation(request.CreateTicketFromConversationRequest{ConversationID: otherConversation}, leader); err == nil {
		t.Error("leader created Ticket from another team's Conversation")
	}
}

func TestTicketCreateFromConversationPreservesTrustedInternalCaller(t *testing.T) {
	setupTicketTestDB(t)
	customerID := createTestCustomer(t, "ticket-create-internal-customer")
	conversationID := createTicketWriteConversation(t, customerID, enums.IMConversationStatusClosed, 0, 0)
	internalAI := &dto.AuthPrincipal{Username: "AI"}

	created, err := services.TicketService.CreateFromConversation(request.CreateTicketFromConversationRequest{
		ConversationID: conversationID,
		Title:          "trusted internal creation",
	}, internalAI)
	if err != nil {
		t.Fatalf("trusted internal CreateFromConversation path failed: %v", err)
	}
	if created.ConversationID != conversationID || created.CustomerID != customerID {
		t.Fatalf("trusted internal path did not preserve Conversation data: %+v", created)
	}
}

func TestDashboardTicketCreationUsesC1ConversationScopeForTicketAdministrators(t *testing.T) {
	setupTicketTestDB(t)
	adminTeamID := createTicketScopeTeam(t, "ticket-admin-conversation-team")
	otherTeamID := createTicketScopeTeam(t, "ticket-admin-conversation-other-team")
	adminID := createTicketScopeAgent(t, "ticket-admin-conversation-admin", adminTeamID, enums.StatusOk)
	otherAgentID := createTicketScopeAgent(t, "ticket-admin-conversation-other-agent", otherTeamID, enums.StatusOk)
	admin := ticketWritePrincipal(adminID, constants.RoleCodeAdmin)
	customerID := createTestCustomer(t, "ticket-admin-conversation-customer")
	otherConversation := createTicketWriteConversation(t, customerID, enums.IMConversationStatusActive, otherAgentID, otherTeamID)

	if _, err := services.TicketService.CreateFromConversation(request.CreateTicketFromConversationRequest{
		ConversationID: otherConversation,
		Title:          "admin must still pass C1 scope",
	}, admin); err == nil {
		t.Fatal("Ticket administrator created from a Conversation outside C1 ActiveWrite scope")
	}
	if _, err := services.TicketService.CreateTicket(request.CreateTicketRequest{
		Title:             "admin manual Ticket with linked Conversation",
		Description:       "must still pass C1 scope",
		CustomerID:        customerID,
		ConversationID:    otherConversation,
		CurrentAssigneeID: adminID,
	}, admin); err == nil {
		t.Fatal("Ticket administrator manually linked a Conversation outside C1 ActiveWrite scope")
	}
}

func TestTicketCustomerRelinkRequiresScopeAndDoesNotAllowAgentArbitraryRelink(t *testing.T) {
	setupTicketTestDB(t)
	teamID := createTicketScopeTeam(t, "ticket-link-team")
	otherTeamID := createTicketScopeTeam(t, "ticket-link-outside")
	agentID := createTicketScopeAgent(t, "ticket-link-agent", teamID, enums.StatusOk)
	peerID := createTicketScopeAgent(t, "ticket-link-peer", teamID, enums.StatusOk)
	agent := ticketWritePrincipal(agentID, constants.RoleCodeCsUser)
	customerID := createTestCustomer(t, "ticket-link-customer")
	otherCustomerID := createTestCustomer(t, "ticket-link-other-customer")

	ownedConversation := createTicketWriteConversation(t, customerID, enums.IMConversationStatusActive, agentID, teamID)
	owned := createTicketForScopeTest(t, "owned link", agentID, enums.TicketStatusPending)
	if err := sqls.DB().Model(&models.Ticket{}).Where("id = ?", owned.ID).Update("conversation_id", ownedConversation).Error; err != nil {
		t.Fatalf("attach owned Conversation to Ticket: %v", err)
	}
	if err := services.TicketService.LinkCustomer(owned.ID, customerID, agent); err != nil {
		t.Fatalf("agent could not link the customer already bound to their Conversation: %v", err)
	}
	if err := services.TicketService.LinkCustomer(owned.ID, otherCustomerID, agent); err == nil {
		t.Fatal("agent changed customer away from the linked Conversation customer")
	}

	peerConversation := createTicketWriteConversation(t, customerID, enums.IMConversationStatusActive, peerID, teamID)
	outOfScope := createTicketForScopeTest(t, "peer link", peerID, enums.TicketStatusPending)
	if err := sqls.DB().Model(&models.Ticket{}).Where("id = ?", outOfScope.ID).Update("conversation_id", peerConversation).Error; err != nil {
		t.Fatalf("attach peer Conversation to Ticket: %v", err)
	}
	if err := services.TicketService.LinkCustomer(outOfScope.ID, customerID, agent); err == nil {
		t.Error("agent linked a customer on a peer Ticket outside current scope")
	}

	unlinked := createTicketForScopeTest(t, "unlinked customer link", agentID, enums.TicketStatusPending)
	if err := services.TicketService.LinkCustomer(unlinked.ID, customerID, agent); err == nil {
		t.Error("cs_user arbitrarily linked a customer without an authorized linked Conversation")
	}

	admin := createTestTicketAdminOperator(t, "ticket-link-admin")
	otherTeamTicket := createTicketForScopeTest(t, "admin global link", 0, enums.TicketStatusPending)
	if err := sqls.DB().Model(&models.Ticket{}).Where("id = ?", otherTeamTicket.ID).Update("conversation_id", createTicketWriteConversation(t, customerID, enums.IMConversationStatusActive, 0, otherTeamID)).Error; err != nil {
		t.Fatalf("attach admin test Conversation: %v", err)
	}
	if err := services.TicketService.LinkCustomer(otherTeamTicket.ID, customerID, admin); err != nil {
		t.Fatalf("admin global customer link failed: %v", err)
	}
}

func TestTicketAdminRetainsGlobalWriteBehaviorAndInvalidProfileFailsClosed(t *testing.T) {
	setupTicketTestDB(t)
	admin := createTestTicketAdminOperator(t, "ticket-write-admin")
	teamID := createTicketScopeTeam(t, "ticket-invalid-profile-team")
	agentID := createTicketScopeAgent(t, "ticket-invalid-profile-agent", teamID, enums.StatusDisabled)
	invalidAgent := ticketWritePrincipal(agentID, constants.RoleCodeCsUser)
	foreignTicket := createTicketForScopeTest(t, "global admin ticket", 0, enums.TicketStatusPending)

	if err := services.TicketService.ChangeStatus(request.ChangeTicketStatusRequest{TicketID: foreignTicket.ID, Status: string(enums.TicketStatusDone)}, admin); err != nil {
		t.Fatalf("admin lost global status write: %v", err)
	}
	profileTicket := createTicketForScopeTest(t, "invalid profile scope", agentID, enums.TicketStatusPending)
	if err := services.TicketService.ChangeStatus(request.ChangeTicketStatusRequest{TicketID: profileTicket.ID, Status: string(enums.TicketStatusDone)}, invalidAgent); err == nil {
		t.Fatal("agent with disabled profile wrote to a Ticket")
	}
}

func ticketWritePrincipal(userID int64, role string) *dto.AuthPrincipal {
	principal := ticketScopePrincipal(userID, role)
	principal.Username = fmt.Sprintf("ticket-write-%d", userID)
	principal.Permissions = []string{
		constants.PermissionTicketView.Code,
		constants.PermissionTicketCreate.Code,
		constants.PermissionTicketUpdate.Code,
		constants.PermissionTicketAssign.Code,
		constants.PermissionTicketChangeStatus.Code,
		constants.PermissionTicketProgress.Code,
	}
	return principal
}

func createTicketWriteConversation(t *testing.T, customerID int64, status enums.IMConversationStatus, assigneeID, teamID int64) int64 {
	t.Helper()
	now := time.Now()
	conversation := &models.Conversation{
		CustomerID:         customerID,
		CustomerName:       "Ticket write test customer",
		Status:             status,
		ServiceMode:        enums.IMConversationServiceModeHumanOnly,
		CurrentAssigneeID:  assigneeID,
		CurrentTeamID:      teamID,
		LastMessageAt:      now,
		LastActiveAt:       now,
		LastMessageSummary: "Conversation summary",
		AuditFields:        models.AuditFields{CreatedAt: now, UpdatedAt: now},
	}
	if err := repositories.ConversationRepository.Create(sqls.DB(), conversation); err != nil {
		t.Fatalf("create Conversation: %v", err)
	}
	return conversation.ID
}
