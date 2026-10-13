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
	"gorm.io/gorm/clause"
)

type ConversationScopeAction uint8

const (
	ConversationScopeRead ConversationScopeAction = iota
	ConversationScopeActiveWrite
	ConversationScopeClaim
	ConversationScopeClose
)

// EmployeeConversationScope is resolved exclusively from authenticated identity
// and persisted employee/team records. It must be resolved again for each
// request or protected realtime publication, never cached across mutations.
type EmployeeConversationScope struct {
	userID   int64
	teamID   int64
	global   bool
	teamWide bool
}

func (s *EmployeeConversationScope) UserID() int64 {
	if s == nil {
		return 0
	}
	return s.userID
}
func (s *EmployeeConversationScope) TeamID() int64 {
	if s == nil {
		return 0
	}
	return s.teamID
}
func (s *EmployeeConversationScope) IsGlobal() bool     { return s != nil && s.global }
func (s *EmployeeConversationScope) IsTeamLeader() bool { return s != nil && s.teamWide }

var ConversationScopeService = &conversationScopeService{}

type conversationScopeService struct{}

// ResolveClaim keeps self-assignment tied to a current active agent profile,
// including for globally visible administrators. Call within the write transaction.
func (s *conversationScopeService) ResolveClaim(db *gorm.DB, principal *dto.AuthPrincipal) (*EmployeeConversationScope, error) {
	if db == nil {
		return nil, errorsx.ForbiddenI18n("error.e0222")
	}
	locked := db.Clauses(clause.Locking{Strength: "UPDATE"})
	scope, err := s.Resolve(locked, principal)
	if err != nil {
		return nil, err
	}
	profile, err := repositories.EmployeeConversationScopeRepository.Profile(locked, scope.UserID())
	if err != nil || profile.Status != enums.StatusOk || profile.TeamID <= 0 {
		return nil, errorsx.ForbiddenI18n("error.e0222")
	}
	team, err := repositories.EmployeeConversationScopeRepository.Team(locked, profile.TeamID)
	if err != nil || team.Status != enums.StatusOk {
		return nil, errorsx.ForbiddenI18n("error.e0222")
	}
	scope.teamID = team.ID
	return scope, nil
}

func (s *conversationScopeService) Resolve(db *gorm.DB, principal *dto.AuthPrincipal) (*EmployeeConversationScope, error) {
	deny := errorsx.ForbiddenI18n("error.e0222")
	if db == nil || principal == nil || principal.UserID <= 0 || principal.Status != enums.StatusOk || principal.UserType != enums.UserTypeEmployee {
		return nil, deny
	}
	user, err := repositories.EmployeeConversationScopeRepository.User(db, principal.UserID)
	if err != nil || user.Status != enums.StatusOk || user.UserType != enums.UserTypeEmployee {
		return nil, deny
	}
	scope := &EmployeeConversationScope{userID: principal.UserID}
	if slices.Contains(principal.Roles, constants.RoleCodeSuperAdmin) {
		scope.global = true
		return scope, nil
	}
	profile, err := repositories.EmployeeConversationScopeRepository.Profile(db, principal.UserID)
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

func (s *conversationScopeService) Authorize(db *gorm.DB, principal *dto.AuthPrincipal, conversationID int64, action ConversationScopeAction) (*models.Conversation, error) {
	scope, err := s.Resolve(db, principal)
	if err != nil {
		return nil, err
	}
	if conversationID <= 0 {
		return nil, errorsx.ForbiddenI18n("error.e0222")
	}
	conversation, err := repositories.EmployeeConversationScopeRepository.Conversation(db, conversationID)
	if err != nil || !scope.Allows(conversation, action) {
		return nil, errorsx.ForbiddenI18n("error.e0222")
	}
	return conversation, nil
}

// AuthorizeForUpdate re-resolves the employee scope and locks the current
// conversation row before checking a mutation policy. Call it from the same
// transaction that will persist the mutation.
func (s *conversationScopeService) AuthorizeForUpdate(db *gorm.DB, principal *dto.AuthPrincipal, conversationID int64, action ConversationScopeAction) (*models.Conversation, error) {
	scope, err := s.Resolve(db, principal)
	if err != nil {
		return nil, err
	}
	if conversationID <= 0 {
		return nil, errorsx.ForbiddenI18n("error.e0222")
	}
	conversation, err := repositories.EmployeeConversationScopeRepository.ConversationForUpdate(db, conversationID)
	if err != nil || !scope.Allows(conversation, action) {
		return nil, errorsx.ForbiddenI18n("error.e0222")
	}
	return conversation, nil
}

func (s *EmployeeConversationScope) Allows(conversation *models.Conversation, action ConversationScopeAction) bool {
	if s == nil || s.userID <= 0 || conversation == nil || conversation.ID <= 0 {
		return false
	}
	waiting := conversation.Status == enums.IMConversationStatusPending && conversation.CurrentAssigneeID == 0 && conversation.ServiceMode != enums.IMConversationServiceModeAIOnly
	inTeam := s.teamID > 0 && conversation.CurrentTeamID == s.teamID
	owned := conversation.CurrentAssigneeID == s.userID
	switch action {
	case ConversationScopeRead:
		return s.global || (s.teamWide && inTeam) || (!s.teamWide && (owned || (inTeam && waiting)))
	case ConversationScopeActiveWrite:
		return conversation.Status == enums.IMConversationStatusActive && (s.global || (s.teamWide && inTeam) || (!s.teamWide && owned))
	case ConversationScopeClose:
		if s.global {
			return conversation.Status == enums.IMConversationStatusActive ||
				conversation.Status == enums.IMConversationStatusPending ||
				conversation.Status == enums.IMConversationStatusAIServing
		}
		return s.Allows(conversation, ConversationScopeActiveWrite)
	case ConversationScopeClaim:
		return waiting && inTeam
	default:
		return false
	}
}

func (s *EmployeeConversationScope) Restrict(cnd *sqls.Cnd) *sqls.Cnd {
	if s == nil {
		return repositories.EmployeeConversationScopeRepository.Restrict(cnd, 0, 0, false, false)
	}
	return repositories.EmployeeConversationScopeRepository.Restrict(cnd, s.userID, s.teamID, s.global, s.teamWide)
}
