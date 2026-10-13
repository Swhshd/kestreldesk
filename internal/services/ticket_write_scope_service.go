package services

import (
	"context"
	"strings"
	"time"

	"github.com/Swhshd/kestreldesk/internal/events"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/request"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/pkg/eventbus"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

// FindTicketForUpdate returns a currently in-scope Ticket while holding its
// row lock through the caller's write transaction.
func (s *TicketAccessScope) FindTicketForUpdate(db *gorm.DB, ticketID int64) (*models.Ticket, error) {
	if db == nil || s == nil || ticketID <= 0 {
		return nil, errorsx.InvalidParamI18n("error.e0178")
	}
	cnd := s.Restrict(db, sqls.NewCnd().Eq("id", ticketID))
	item := repositories.TicketRepository.FindOneForUpdate(db, cnd)
	if item == nil {
		return nil, errorsx.InvalidParamI18n("error.e0178")
	}
	return item, nil
}

// ValidateAssignmentTarget keeps team-leader destinations within their active
// authoritative team. Global administrators retain the existing active-user
// target rule, which the caller validates separately.
func (s *ticketAccessPolicyService) ValidateAssignmentTarget(db *gorm.DB, scope *TicketAccessScope, targetUserID int64) error {
	deny := errorsx.ForbiddenI18n("error.e0225")
	if db == nil || scope == nil || targetUserID <= 0 {
		return deny
	}
	if scope.IsGlobal() {
		return nil
	}
	if !scope.IsTeamLeader() || scope.TeamID() <= 0 {
		return deny
	}

	user, err := repositories.EmployeeConversationScopeRepository.User(db, targetUserID)
	if err != nil || user.Status != enums.StatusOk || user.UserType != enums.UserTypeEmployee || user.DeletedAt != nil {
		return deny
	}
	profile, err := repositories.EmployeeConversationScopeRepository.Profile(db, user.ID)
	if err != nil || profile.Status != enums.StatusOk || profile.TeamID != scope.TeamID() {
		return deny
	}
	team, err := repositories.EmployeeConversationScopeRepository.Team(db, profile.TeamID)
	if err != nil || team.Status != enums.StatusOk || team.ID != scope.TeamID() {
		return deny
	}
	return nil
}

func (s *ticketService) requireWriteScope(db *gorm.DB, operator *dto.AuthPrincipal) (*TicketAccessScope, error) {
	if operator == nil {
		return nil, errorsx.UnauthorizedI18n("error.auth.expired")
	}
	return TicketAccessPolicyService.Resolve(db, operator)
}

// resolveCreateScope permits the existing trusted internal creation path to
// continue using user ID zero. Dashboard callers always have a persisted
// employee identity and must resolve the Ticket policy.
func (s *ticketService) resolveCreateScope(db *gorm.DB, operator *dto.AuthPrincipal) (*TicketAccessScope, error) {
	if operator == nil {
		return nil, errorsx.UnauthorizedI18n("error.auth.expired")
	}
	if operator.UserID == 0 {
		return nil, nil
	}
	if operator.UserID < 0 {
		return nil, errorsx.UnauthorizedI18n("error.auth.expired")
	}
	return TicketAccessPolicyService.Resolve(db, operator)
}

func (s *ticketService) findTicketForWrite(db *gorm.DB, operator *dto.AuthPrincipal, ticketID int64) (*TicketAccessScope, *models.Ticket, error) {
	scope, err := s.requireWriteScope(db, operator)
	if err != nil {
		return nil, nil, err
	}
	ticket, err := scope.FindTicketForUpdate(db, ticketID)
	if err != nil {
		return nil, nil, err
	}
	return scope, ticket, nil
}

func (s *ticketService) validateCreateTicketReferences(db *gorm.DB, req request.CreateTicketRequest, operator *dto.AuthPrincipal, scope *TicketAccessScope) error {
	if req.CustomerID > 0 && repositories.CustomerRepository.Get(db, req.CustomerID) == nil {
		return errorsx.InvalidParamI18n("error.e0155")
	}
	if req.ConversationID > 0 {
		var conversation *models.Conversation
		if operator.UserID == 0 {
			// User ID zero is reserved for trusted internal AI creation. Employee
			// Dashboard creation has already passed C1 ActiveWrite in its scoped
			// entry point and every other employee create must authorize here.
			conversation = repositories.ConversationRepository.Get(db, req.ConversationID)
		} else {
			var err error
			conversation, err = ConversationScopeService.AuthorizeForUpdate(db, operator, req.ConversationID, ConversationScopeActiveWrite)
			if err != nil {
				return err
			}
		}
		if conversation == nil {
			return errorsx.InvalidParamI18n("error.e0116")
		}
		if req.CustomerID > 0 && conversation.CustomerID != req.CustomerID {
			return errorsx.InvalidParamI18n("error.e0118")
		}
	}
	if req.CurrentAssigneeID > 0 {
		if err := s.validateAssigneeTx(db, req.CurrentAssigneeID); err != nil {
			return err
		}
	}
	return nil
}

func (s *ticketService) validateAssigneeTx(db *gorm.DB, userID int64) error {
	if userID <= 0 {
		return nil
	}
	user := repositories.UserRepository.Get(db, userID)
	if user == nil || user.Status != enums.StatusOk {
		return errorsx.InvalidParamI18n("error.e0334")
	}
	return nil
}

func (s *ticketService) buildTicket(req request.CreateTicketRequest, operator *dto.AuthPrincipal, auditFields models.AuditFields) *models.Ticket {
	source := enums.TicketSource(strings.TrimSpace(req.Source))
	if source == "" {
		source = enums.TicketSourceManual
	}
	return &models.Ticket{
		Title:             strings.TrimSpace(req.Title),
		Description:       strings.TrimSpace(req.Description),
		Source:            source,
		Channel:           strings.TrimSpace(req.Channel),
		CustomerID:        req.CustomerID,
		ConversationID:    req.ConversationID,
		Status:            enums.TicketStatusPending,
		CurrentAssigneeID: req.CurrentAssigneeID,
		AuditFields:       auditFields,
	}
}

func (s *ticketService) persistTicketTx(db *gorm.DB, ticket *models.Ticket, tagIDs []int64, operator *dto.AuthPrincipal) error {
	if err := repositories.TicketRepository.Create(db, ticket); err != nil {
		return err
	}
	if err := TicketTagService.ReplaceTicketTags(db, ticket.ID, tagIDs, operator); err != nil {
		return err
	}
	return repositories.TicketProgressRepository.Create(db, &models.TicketProgress{
		TicketID:  ticket.ID,
		Content:   "Created ticket",
		AuthorID:  operator.UserID,
		CreatedAt: time.Now(),
	})
}

func (s *ticketService) publishTicketCreated(ticketID int64, operator *dto.AuthPrincipal) {
	eventbus.PublishAsync(context.Background(), events.TicketCreatedEvent{
		TicketID:   ticketID,
		OperatorID: operator.UserID,
	})
}
