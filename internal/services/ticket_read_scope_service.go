package services

import (
	"slices"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

// TicketAccessScope is resolved from the authenticated employee's persisted
// account and AgentProfile. Resolve it again for each read operation.
type TicketAccessScope struct {
	userID   int64
	teamID   int64
	global   bool
	teamWide bool
}

func (s *TicketAccessScope) UserID() int64 {
	if s == nil {
		return 0
	}
	return s.userID
}

func (s *TicketAccessScope) TeamID() int64 {
	if s == nil {
		return 0
	}
	return s.teamID
}

func (s *TicketAccessScope) IsGlobal() bool {
	return s != nil && s.global
}

func (s *TicketAccessScope) IsTeamLeader() bool {
	return s != nil && s.teamWide
}

// Restrict adds the resolved object scope to an existing Ticket query.
// Callers may then add ordinary client filters, ordering, or pagination;
// those filters can only narrow this SQL predicate.
func (s *TicketAccessScope) Restrict(db *gorm.DB, cnd *sqls.Cnd) *sqls.Cnd {
	if s == nil {
		return repositories.TicketRepository.RestrictToReadScope(db, cnd, 0, 0, false, false)
	}
	return repositories.TicketRepository.RestrictToReadScope(db, cnd, s.userID, s.teamID, s.global, s.teamWide)
}

// FindTicket returns a Ticket only when its current assignee is inside this
// scope. Out-of-scope and missing IDs share the same not-found response.
func (s *TicketAccessScope) FindTicket(db *gorm.DB, ticketID int64) (*models.Ticket, error) {
	if db == nil || s == nil || ticketID <= 0 {
		return nil, errorsx.InvalidParamI18n("error.e0178")
	}
	cnd := s.Restrict(db, sqls.NewCnd().Eq("id", ticketID))
	item := repositories.TicketRepository.FindOne(db, cnd)
	if item == nil {
		return nil, errorsx.InvalidParamI18n("error.e0178")
	}
	return item, nil
}

var TicketAccessPolicyService = &ticketAccessPolicyService{}

type ticketAccessPolicyService struct{}

func (s *ticketAccessPolicyService) Resolve(db *gorm.DB, principal *dto.AuthPrincipal) (*TicketAccessScope, error) {
	deny := errorsx.ForbiddenI18n("error.e0225")
	if db == nil || principal == nil || principal.UserID <= 0 || principal.Status != enums.StatusOk || principal.UserType != enums.UserTypeEmployee {
		return nil, deny
	}
	user, err := repositories.EmployeeConversationScopeRepository.User(db, principal.UserID)
	if err != nil || user.Status != enums.StatusOk || user.UserType != enums.UserTypeEmployee || user.DeletedAt != nil {
		return nil, deny
	}

	scope := &TicketAccessScope{userID: user.ID}
	if slices.Contains(principal.Roles, constants.RoleCodeSuperAdmin) || slices.Contains(principal.Roles, constants.RoleCodeAdmin) {
		scope.global = true
		return scope, nil
	}
	if !slices.Contains(principal.Roles, constants.RoleCodeCsUser) && !slices.Contains(principal.Roles, constants.RoleCodeCsTeamLeader) {
		return nil, deny
	}

	profile, err := repositories.EmployeeConversationScopeRepository.Profile(db, user.ID)
	if err != nil || profile.Status != enums.StatusOk || profile.TeamID <= 0 {
		return nil, deny
	}
	team, err := repositories.EmployeeConversationScopeRepository.Team(db, profile.TeamID)
	if err != nil || team.Status != enums.StatusOk {
		return nil, deny
	}
	scope.teamID = team.ID
	scope.teamWide = slices.Contains(principal.Roles, constants.RoleCodeCsTeamLeader)
	return scope, nil
}
