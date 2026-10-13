package services

// This file has been modified for the KestrelDesk project.

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
	"github.com/Swhshd/kestreldesk/internal/pkg/i18nx"
	"github.com/Swhshd/kestreldesk/internal/pkg/utils"
	"github.com/Swhshd/kestreldesk/internal/repositories"

	"github.com/Swhshd/kestreldesk/internal/pkg/httpx/params"

	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

var TicketService = newTicketService()

func newTicketService() *ticketService {
	return &ticketService{}
}

type TicketDetailAggregate struct {
	Ticket     *models.Ticket
	Tags       []models.Tag
	Customer   *models.Customer
	Progresses []models.TicketProgress
	Users      map[int64]*models.User
}

type TicketSummaryAggregate struct {
	All        int64
	Pending    int64
	InProgress int64
	Done       int64
	Unassigned int64
	Mine       int64
	Stale      int64
}

type TicketListAggregate struct {
	List           []models.Ticket
	Paging         *sqls.Paging
	TagsByTicketID map[int64][]models.Tag
	Users          map[int64]*models.User
	Customers      map[int64]*models.Customer
}

type ticketService struct {
}

func normalizeTicketStaleHours(staleHours int) int {
	switch staleHours {
	case 24, 48, 168:
		return staleHours
	default:
		return 24
	}
}

func buildTicketAssignmentProgressContent(fromUser *models.User, toUser *models.User, reason string) string {
	fromName := ticketAssignmentUserDisplayName(fromUser)
	if fromName == "" {
		fromName = "未分配"
	}
	toName := ticketAssignmentUserDisplayName(toUser)
	if toName == "" && toUser != nil {
		toName = toUser.Username
	}
	content := "指派处理人：" + fromName + " -> " + toName
	if trimmedReason := strings.TrimSpace(reason); trimmedReason != "" {
		content += "，原因：" + trimmedReason
	}
	return content
}

func ticketAssignmentUserDisplayName(user *models.User) string {
	if user == nil {
		return ""
	}
	if strings.TrimSpace(user.Nickname) != "" {
		return strings.TrimSpace(user.Nickname)
	}
	return strings.TrimSpace(user.Username)
}

func (s *ticketService) Get(id int64) *models.Ticket {
	return repositories.TicketRepository.Get(sqls.DB(), id)
}

func (s *ticketService) Take(where ...any) *models.Ticket {
	return repositories.TicketRepository.Take(sqls.DB(), where...)
}

func (s *ticketService) Find(cnd *sqls.Cnd) []models.Ticket {
	return repositories.TicketRepository.Find(sqls.DB(), cnd)
}

func (s *ticketService) FindOne(cnd *sqls.Cnd) *models.Ticket {
	return repositories.TicketRepository.FindOne(sqls.DB(), cnd)
}

func (s *ticketService) FindPageByParams(params *params.QueryParams) (list []models.Ticket, paging *sqls.Paging) {
	return repositories.TicketRepository.FindPageByParams(sqls.DB(), params)
}

func (s *ticketService) FindPageByCnd(cnd *sqls.Cnd) (list []models.Ticket, paging *sqls.Paging) {
	return repositories.TicketRepository.FindPageByCnd(sqls.DB(), cnd)
}

func (s *ticketService) FindPageAggregateByCnd(cnd *sqls.Cnd, operator *dto.AuthPrincipal) (*TicketListAggregate, error) {
	db := sqls.DB()
	scope, err := TicketAccessPolicyService.Resolve(db, operator)
	if err != nil {
		return nil, err
	}
	cnd = scope.Restrict(db, cnd)
	list, paging := repositories.TicketRepository.FindPageByCnd(db, cnd)
	return s.buildTicketListAggregate(db, list, paging), nil
}

func (s *ticketService) GetCreateAssigneeOptions(operator *dto.AuthPrincipal) ([]models.AgentProfile, error) {
	scope, err := TicketAccessPolicyService.Resolve(sqls.DB(), operator)
	if err != nil {
		return nil, err
	}
	if scope.IsGlobal() {
		return AgentProfileService.Find(sqls.NewCnd().Desc("id")), nil
	}
	if !scope.IsTeamLeader() {
		return nil, errorsx.ForbiddenI18n("error.e0225")
	}
	return repositories.AgentProfileRepository.FindActiveTicketAssigneesByTeam(sqls.DB(), scope.TeamID())
}

func (s *ticketService) ApplyStaleFilter(cnd *sqls.Cnd, staleHours int) *sqls.Cnd {
	if cnd == nil {
		cnd = sqls.NewCnd()
	}
	staleHour := normalizeTicketStaleHours(staleHours)
	return cnd.
		NotEq("status", enums.TicketStatusDone).
		Where("updated_at < ?", time.Now().Add(-time.Duration(staleHour)*time.Hour))
}

func (s *ticketService) Count(cnd *sqls.Cnd) int64 {
	return repositories.TicketRepository.Count(sqls.DB(), cnd)
}

func (s *ticketService) Create(t *models.Ticket) error {
	return repositories.TicketRepository.Create(sqls.DB(), t)
}

func (s *ticketService) Update(t *models.Ticket) error {
	return repositories.TicketRepository.Update(sqls.DB(), t)
}

func (s *ticketService) Updates(id int64, columns map[string]any) error {
	return repositories.TicketRepository.Updates(sqls.DB(), id, columns)
}

func (s *ticketService) UpdateColumn(id int64, name string, value any) error {
	return repositories.TicketRepository.UpdateColumn(sqls.DB(), id, name, value)
}

func (s *ticketService) Delete(id int64) {
	repositories.TicketRepository.Delete(sqls.DB(), id)
}

func (s *ticketService) GetTags(ticketID int64) []models.Tag {
	if ticketID <= 0 {
		return nil
	}
	relations := TicketTagService.Find(sqls.NewCnd().Eq("ticket_id", ticketID).Asc("id"))
	if len(relations) == 0 {
		return nil
	}
	tagIDs := make([]int64, 0, len(relations))
	for i := range relations {
		tagIDs = append(tagIDs, relations[i].TagID)
	}
	tags := repositories.TagRepository.Find(sqls.DB(), sqls.NewCnd().In("id", tagIDs))
	if len(tags) <= 1 {
		return tags
	}
	tagMap := make(map[int64]models.Tag, len(tags))
	for i := range tags {
		tagMap[tags[i].ID] = tags[i]
	}
	ordered := make([]models.Tag, 0, len(relations))
	for _, tagID := range tagIDs {
		if tag, ok := tagMap[tagID]; ok {
			ordered = append(ordered, tag)
		}
	}
	return ordered
}

func (s *ticketService) CreateTicket(req request.CreateTicketRequest, operator *dto.AuthPrincipal) (*models.Ticket, error) {
	if operator == nil {
		return nil, errorsx.UnauthorizedI18n("error.auth.expired")
	}
	title := strings.TrimSpace(req.Title)
	description := strings.TrimSpace(req.Description)
	if title == "" {
		return nil, errorsx.InvalidParamI18n("error.e0181")
	}
	if description == "" {
		return nil, errorsx.InvalidParamI18n("error.e0179")
	}
	source := enums.TicketSource(strings.TrimSpace(req.Source))
	if source == "" {
		source = enums.TicketSourceManual
	}
	if !enums.IsValidTicketSource(string(source)) {
		return nil, errorsx.InvalidParamI18n("error.e0180")
	}
	tagIDs, err := TicketTagService.ValidateTagIDs(req.TagIDs)
	if err != nil {
		return nil, err
	}

	auditFields := utils.BuildAuditFields(operator)
	ticketNo, err := TicketNoSequenceService.Next(auditFields.CreatedAt)
	if err != nil {
		return nil, err
	}
	ticket := &models.Ticket{}

	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		scope, err := s.resolveCreateScope(ctx.Tx, operator)
		if err != nil {
			return err
		}
		if scope != nil && !scope.IsGlobal() {
			if !scope.IsTeamLeader() {
				return errorsx.ForbiddenI18n("error.e0225")
			}
			if err := TicketAccessPolicyService.ValidateAssignmentTarget(ctx.Tx, scope, req.CurrentAssigneeID); err != nil {
				return err
			}
		}
		if err := s.validateCreateTicketReferences(ctx.Tx, req, operator, scope); err != nil {
			return err
		}
		ticket = s.buildTicket(req, operator, auditFields)
		ticket.TicketNo = ticketNo
		return s.persistTicketTx(ctx.Tx, ticket, tagIDs, operator)
	}); err != nil {
		return nil, err
	}

	eventbus.PublishAsync(context.Background(), events.TicketCreatedEvent{
		TicketID:   ticket.ID,
		OperatorID: operator.UserID,
	})
	return s.Get(ticket.ID), nil
}

func (s *ticketService) CreateFromConversation(req request.CreateTicketFromConversationRequest, operator *dto.AuthPrincipal) (*models.Ticket, error) {
	if operator == nil {
		return nil, errorsx.UnauthorizedI18n("error.auth.expired")
	}
	if operator.UserID > 0 {
		return s.CreateFromConversationForDashboard(req, operator)
	}
	return s.createFromConversationTrusted(req, operator)
}

// CreateFromConversationForDashboard creates from a Conversation only after
// authorizing the current employee against C1's active-write policy. Ticket
// ownership is derived from the authorized Conversation and employee scope.
func (s *ticketService) CreateFromConversationForDashboard(req request.CreateTicketFromConversationRequest, operator *dto.AuthPrincipal) (*models.Ticket, error) {
	if operator == nil {
		return nil, errorsx.UnauthorizedI18n("error.auth.expired")
	}
	tagIDs, err := TicketTagService.ValidateTagIDs(req.TagIDs)
	if err != nil {
		return nil, err
	}
	createdAt := time.Now()
	ticketNo, err := TicketNoSequenceService.Next(createdAt)
	if err != nil {
		return nil, err
	}
	auditFields := utils.BuildAuditFields(operator)
	ticket := &models.Ticket{}
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		scope, err := s.requireWriteScope(ctx.Tx, operator)
		if err != nil {
			return err
		}
		conversation, err := ConversationScopeService.AuthorizeForUpdate(ctx.Tx, operator, req.ConversationID, ConversationScopeActiveWrite)
		if err != nil {
			return err
		}
		if req.CustomerID != 0 && req.CustomerID != conversation.CustomerID {
			return errorsx.InvalidParamI18n("error.e0118")
		}
		if req.CurrentAssigneeID < 0 {
			return errorsx.InvalidParamI18n("error.e0334")
		}

		assigneeID := req.CurrentAssigneeID
		switch {
		case scope.IsGlobal():
			if assigneeID > 0 {
				if err := s.validateAssigneeTx(ctx.Tx, assigneeID); err != nil {
					return err
				}
			}
		case scope.IsTeamLeader():
			if assigneeID <= 0 {
				assigneeID = conversation.CurrentAssigneeID
			}
			if err := TicketAccessPolicyService.ValidateAssignmentTarget(ctx.Tx, scope, assigneeID); err != nil {
				return err
			}
		default:
			if assigneeID != 0 && assigneeID != scope.UserID() {
				return errorsx.ForbiddenI18n("error.e0225")
			}
			assigneeID = scope.UserID()
		}

		customerID := conversation.CustomerID
		if customerID > 0 && repositories.CustomerRepository.Get(ctx.Tx, customerID) == nil {
			return errorsx.InvalidParamI18n("error.e0155")
		}
		title := strings.TrimSpace(req.Title)
		if title == "" {
			title = strings.TrimSpace(ConversationService.BuildConversationSummary(conversation))
		}
		if title == "" {
			title = i18nx.Get("ticket.defaultConversationTitle")
		}
		description := strings.TrimSpace(req.Description)
		if description == "" {
			description = strings.TrimSpace(conversation.LastMessageSummary)
		}
		if description == "" {
			description = title
		}
		createReq := request.CreateTicketRequest{
			Title:             title,
			Description:       description,
			Source:            string(enums.TicketSourceConversation),
			Channel:           s.resolveConversationChannel(conversation),
			CustomerID:        conversation.CustomerID,
			ConversationID:    conversation.ID,
			TagIDs:            req.TagIDs,
			CurrentAssigneeID: assigneeID,
		}
		ticket = s.buildTicket(createReq, operator, auditFields)
		ticket.TicketNo = ticketNo
		return s.persistTicketTx(ctx.Tx, ticket, tagIDs, operator)
	}); err != nil {
		return nil, err
	}
	s.publishTicketCreated(ticket.ID, operator)
	return s.Get(ticket.ID), nil
}

func (s *ticketService) createFromConversationTrusted(req request.CreateTicketFromConversationRequest, operator *dto.AuthPrincipal) (*models.Ticket, error) {
	conversation := ConversationService.Get(req.ConversationID)
	if conversation == nil {
		return nil, errorsx.InvalidParamI18n("error.e0116")
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSpace(ConversationService.BuildConversationSummary(conversation))
	}
	if title == "" {
		title = i18nx.Get("ticket.defaultConversationTitle")
	}
	description := strings.TrimSpace(req.Description)
	if description == "" {
		description = strings.TrimSpace(conversation.LastMessageSummary)
	}
	if description == "" {
		description = title
	}
	return s.CreateTicket(request.CreateTicketRequest{
		Title:             title,
		Description:       description,
		Source:            string(enums.TicketSourceConversation),
		Channel:           s.resolveConversationChannel(conversation),
		CustomerID:        conversation.CustomerID,
		ConversationID:    conversation.ID,
		TagIDs:            req.TagIDs,
		CurrentAssigneeID: req.CurrentAssigneeID,
	}, operator)
}

func (s *ticketService) UpdateTicket(req request.UpdateTicketRequest, operator *dto.AuthPrincipal) error {
	if operator == nil {
		return errorsx.UnauthorizedI18n("error.auth.expired")
	}
	title := strings.TrimSpace(req.Title)
	description := strings.TrimSpace(req.Description)
	if title == "" {
		return errorsx.InvalidParamI18n("error.e0181")
	}
	if description == "" {
		return errorsx.InvalidParamI18n("error.e0179")
	}
	tagIDs, err := TicketTagService.ValidateTagIDs(req.TagIDs)
	if err != nil {
		return err
	}
	now := time.Now()
	return sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		scope, ticket, err := s.findTicketForWrite(ctx.Tx, operator, req.TicketID)
		if err != nil {
			return err
		}
		assigneeChanged := req.CurrentAssigneeID != ticket.CurrentAssigneeID
		if assigneeChanged {
			if !scope.IsGlobal() {
				return errorsx.ForbiddenI18n("error.e0225")
			}
			if err := s.validateAssigneeTx(ctx.Tx, req.CurrentAssigneeID); err != nil {
				return err
			}
		}
		columns := map[string]any{
			"title":            title,
			"description":      description,
			"updated_at":       now,
			"update_user_id":   operator.UserID,
			"update_user_name": operator.Username,
		}
		if scope.IsGlobal() {
			columns["current_assignee_id"] = req.CurrentAssigneeID
		}
		if err := repositories.TicketRepository.Updates(ctx.Tx, ticket.ID, columns); err != nil {
			return err
		}
		return TicketTagService.ReplaceTicketTags(ctx.Tx, ticket.ID, tagIDs, operator)
	})
}

func (s *ticketService) LinkCustomer(ticketID int64, customerID int64, operator *dto.AuthPrincipal) error {
	if operator == nil {
		return errorsx.UnauthorizedI18n("error.auth.expired")
	}
	if customerID <= 0 {
		return errorsx.InvalidParamI18n("error.e0155")
	}
	now := time.Now()
	return sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		scope, ticket, err := s.findTicketForWrite(ctx.Tx, operator, ticketID)
		if err != nil {
			return err
		}
		if repositories.CustomerRepository.Get(ctx.Tx, customerID) == nil {
			return errorsx.InvalidParamI18n("error.e0155")
		}

		var conversation *models.Conversation
		if ticket.ConversationID > 0 {
			if scope.IsGlobal() {
				conversation, err = repositories.EmployeeConversationScopeRepository.Conversation(ctx.Tx, ticket.ConversationID)
				if err != nil {
					return errorsx.InvalidParamI18n("error.e0116")
				}
			} else {
				conversation, err = ConversationScopeService.AuthorizeForUpdate(ctx.Tx, operator, ticket.ConversationID, ConversationScopeRead)
				if err != nil {
					return err
				}
			}
			if conversation.CustomerID > 0 && conversation.CustomerID != customerID {
				return errorsx.InvalidParamI18n("error.e0118")
			}
			if !scope.IsGlobal() && !scope.IsTeamLeader() && conversation.CustomerID != customerID {
				return errorsx.ForbiddenI18n("error.e0225")
			}
		} else if !scope.IsGlobal() && !scope.IsTeamLeader() {
			return errorsx.ForbiddenI18n("error.e0225")
		}
		return repositories.TicketRepository.Updates(ctx.Tx, ticket.ID, map[string]any{
			"customer_id":      customerID,
			"updated_at":       now,
			"update_user_id":   operator.UserID,
			"update_user_name": operator.Username,
		})
	})
}

func (s *ticketService) AssignTicket(req request.AssignTicketRequest, operator *dto.AuthPrincipal) error {
	if operator == nil {
		return errorsx.UnauthorizedI18n("error.auth.expired")
	}
	var assignedEvent *events.TicketAssignedEvent
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		event, err := s.assignTicketTx(ctx.Tx, req, operator)
		if err != nil {
			return err
		}
		assignedEvent = event
		return nil
	}); err != nil {
		return err
	}
	if assignedEvent != nil {
		eventbus.PublishAsync(context.Background(), *assignedEvent)
	}
	return nil
}

func (s *ticketService) ChangeStatus(req request.ChangeTicketStatusRequest, operator *dto.AuthPrincipal) error {
	if operator == nil {
		return errorsx.UnauthorizedI18n("error.auth.expired")
	}
	status := strings.TrimSpace(req.Status)
	if !enums.IsValidTicketStatus(status) {
		return errorsx.InvalidParamI18n("error.e0182")
	}
	now := time.Now()
	var handledAt *time.Time
	if enums.TicketStatus(status) == enums.TicketStatusDone {
		handledAt = &now
	}
	return sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		_, ticket, err := s.findTicketForWrite(ctx.Tx, operator, req.TicketID)
		if err != nil {
			return err
		}
		return repositories.TicketRepository.Updates(ctx.Tx, ticket.ID, map[string]any{
			"status":           enums.TicketStatus(status),
			"handled_at":       handledAt,
			"updated_at":       now,
			"update_user_id":   operator.UserID,
			"update_user_name": operator.Username,
		})
	})
}

func (s *ticketService) AddProgress(req request.CreateTicketProgressRequest, operator *dto.AuthPrincipal) (*models.TicketProgress, error) {
	if operator == nil {
		return nil, errorsx.UnauthorizedI18n("error.auth.expired")
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return nil, errorsx.InvalidParamI18n("error.e0148")
	}
	now := time.Now()
	var progress *models.TicketProgress
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		_, ticket, err := s.findTicketForWrite(ctx.Tx, operator, req.TicketID)
		if err != nil {
			return err
		}
		progress = &models.TicketProgress{
			TicketID:  ticket.ID,
			Content:   content,
			AuthorID:  operator.UserID,
			CreatedAt: now,
		}
		if err := repositories.TicketProgressRepository.Create(ctx.Tx, progress); err != nil {
			return err
		}
		return repositories.TicketRepository.Updates(ctx.Tx, ticket.ID, map[string]any{
			"updated_at":       now,
			"update_user_id":   operator.UserID,
			"update_user_name": operator.Username,
		})
	}); err != nil {
		return nil, err
	}
	return progress, nil
}

func (s *ticketService) GetDetail(id int64, operator *dto.AuthPrincipal) (*TicketDetailAggregate, error) {
	ticket, err := s.ticketForRead(id, operator)
	if err != nil {
		return nil, err
	}
	aggregate := &TicketDetailAggregate{
		Ticket:     ticket,
		Tags:       s.GetTags(id),
		Progresses: repositories.TicketProgressRepository.Find(sqls.DB(), sqls.NewCnd().Eq("ticket_id", id).Asc("id")),
		Users:      make(map[int64]*models.User),
	}
	if ticket.CustomerID > 0 {
		aggregate.Customer = CustomerService.Get(ticket.CustomerID)
	}
	userIDs := make([]int64, 0)
	seen := make(map[int64]struct{})
	addUserID := func(userID int64) {
		if userID <= 0 {
			return
		}
		if _, ok := seen[userID]; ok {
			return
		}
		seen[userID] = struct{}{}
		userIDs = append(userIDs, userID)
	}
	addUserID(ticket.CurrentAssigneeID)
	for i := range aggregate.Progresses {
		addUserID(aggregate.Progresses[i].AuthorID)
	}
	if len(userIDs) > 0 {
		users := repositories.UserRepository.FindByIds(sqls.DB(), userIDs)
		for i := range users {
			item := users[i]
			aggregate.Users[item.ID] = &item
		}
	}
	return aggregate, nil
}

func (s *ticketService) GetProgressHistory(ticketID int64, operator *dto.AuthPrincipal) ([]models.TicketProgress, error) {
	if _, err := s.ticketForRead(ticketID, operator); err != nil {
		return nil, err
	}
	return repositories.TicketProgressRepository.Find(sqls.DB(), sqls.NewCnd().Eq("ticket_id", ticketID).Asc("id")), nil
}

func (s *ticketService) ticketForRead(ticketID int64, operator *dto.AuthPrincipal) (*models.Ticket, error) {
	db := sqls.DB()
	scope, err := TicketAccessPolicyService.Resolve(db, operator)
	if err != nil {
		return nil, err
	}
	return scope.FindTicket(db, ticketID)
}

func (s *ticketService) GetSummary(operator *dto.AuthPrincipal, staleHours ...int) (*TicketSummaryAggregate, error) {
	db := sqls.DB()
	scope, err := TicketAccessPolicyService.Resolve(db, operator)
	if err != nil {
		return nil, err
	}
	count := func(cnd *sqls.Cnd) int64 {
		return repositories.TicketRepository.Count(db, scope.Restrict(db, cnd))
	}
	staleHour := 0
	if len(staleHours) > 0 {
		staleHour = staleHours[0]
	}
	summary := &TicketSummaryAggregate{
		All:        count(sqls.NewCnd()),
		Pending:    count(sqls.NewCnd().Eq("status", enums.TicketStatusPending)),
		InProgress: count(sqls.NewCnd().Eq("status", enums.TicketStatusInProgress)),
		Done:       count(sqls.NewCnd().Eq("status", enums.TicketStatusDone)),
		Unassigned: count(sqls.NewCnd().Eq("current_assignee_id", 0)),
		Stale:      count(s.ApplyStaleFilter(sqls.NewCnd(), staleHour)),
	}
	summary.Mine = count(sqls.NewCnd().Eq("current_assignee_id", operator.UserID))
	return summary, nil
}

func (s *ticketService) assignTicketTx(tx *gorm.DB, req request.AssignTicketRequest, operator *dto.AuthPrincipal) (*events.TicketAssignedEvent, error) {
	scope, ticket, err := s.findTicketForWrite(tx, operator, req.TicketID)
	if err != nil {
		return nil, err
	}
	if !scope.IsGlobal() && !scope.IsTeamLeader() {
		return nil, errorsx.ForbiddenI18n("error.e0225")
	}
	if err := TicketAccessPolicyService.ValidateAssignmentTarget(tx, scope, req.ToUserID); err != nil {
		return nil, err
	}
	if err := s.validateAssigneeTx(tx, req.ToUserID); err != nil {
		return nil, err
	}
	toUser := repositories.UserRepository.Get(tx, req.ToUserID)
	if toUser == nil || toUser.Status != enums.StatusOk {
		return nil, errorsx.InvalidParamI18n("error.e0334")
	}
	var fromUser *models.User
	if ticket.CurrentAssigneeID > 0 {
		fromUser = repositories.UserRepository.Get(tx, ticket.CurrentAssigneeID)
	}
	now := time.Now()
	if err := repositories.TicketRepository.Updates(tx, ticket.ID, map[string]any{
		"current_assignee_id": req.ToUserID,
		"updated_at":          now,
		"update_user_id":      operator.UserID,
		"update_user_name":    operator.Username,
	}); err != nil {
		return nil, err
	}
	if err := repositories.TicketProgressRepository.Create(tx, &models.TicketProgress{
		TicketID:  ticket.ID,
		Content:   buildTicketAssignmentProgressContent(fromUser, toUser, req.Reason),
		AuthorID:  operator.UserID,
		CreatedAt: now,
	}); err != nil {
		return nil, err
	}
	return &events.TicketAssignedEvent{
		TicketID:   ticket.ID,
		FromUserID: ticket.CurrentAssigneeID,
		ToUserID:   req.ToUserID,
		OperatorID: operator.UserID,
		Reason:     strings.TrimSpace(req.Reason),
	}, nil
}

func (s *ticketService) buildTicketListAggregate(db *gorm.DB, list []models.Ticket, paging *sqls.Paging) *TicketListAggregate {
	aggregate := &TicketListAggregate{
		List:           list,
		Paging:         paging,
		TagsByTicketID: make(map[int64][]models.Tag),
		Users:          make(map[int64]*models.User),
		Customers:      make(map[int64]*models.Customer),
	}
	if len(list) == 0 {
		return aggregate
	}
	ticketIDs := make([]int64, 0, len(list))
	customerIDs := make([]int64, 0)
	userIDs := make([]int64, 0)
	ticketSeen := make(map[int64]struct{})
	customerSeen := make(map[int64]struct{})
	userSeen := make(map[int64]struct{})
	for i := range list {
		item := &list[i]
		if _, ok := ticketSeen[item.ID]; !ok {
			ticketSeen[item.ID] = struct{}{}
			ticketIDs = append(ticketIDs, item.ID)
		}
		if item.CustomerID > 0 {
			if _, ok := customerSeen[item.CustomerID]; !ok {
				customerSeen[item.CustomerID] = struct{}{}
				customerIDs = append(customerIDs, item.CustomerID)
			}
		}
		if item.CurrentAssigneeID > 0 {
			if _, ok := userSeen[item.CurrentAssigneeID]; !ok {
				userSeen[item.CurrentAssigneeID] = struct{}{}
				userIDs = append(userIDs, item.CurrentAssigneeID)
			}
		}
	}
	s.enrichTicketTags(db, aggregate, ticketIDs)
	if len(userIDs) > 0 {
		users := repositories.UserRepository.FindByIds(db, userIDs)
		for i := range users {
			item := users[i]
			aggregate.Users[item.ID] = &item
		}
	}
	if len(customerIDs) > 0 {
		customers := repositories.CustomerRepository.Find(db, sqls.NewCnd().In("id", customerIDs))
		for i := range customers {
			item := customers[i]
			aggregate.Customers[item.ID] = &item
		}
	}
	return aggregate
}

func (s *ticketService) enrichTicketTags(db *gorm.DB, aggregate *TicketListAggregate, ticketIDs []int64) {
	if len(ticketIDs) == 0 {
		return
	}
	ticketTags := repositories.TicketTagRepository.Find(db, sqls.NewCnd().In("ticket_id", ticketIDs).Asc("id"))
	if len(ticketTags) == 0 {
		return
	}
	tagIDs := make([]int64, 0)
	tagSeen := make(map[int64]struct{})
	ticketTagMap := make(map[int64][]int64, len(ticketIDs))
	for i := range ticketTags {
		relation := ticketTags[i]
		ticketTagMap[relation.TicketID] = append(ticketTagMap[relation.TicketID], relation.TagID)
		if _, ok := tagSeen[relation.TagID]; !ok {
			tagSeen[relation.TagID] = struct{}{}
			tagIDs = append(tagIDs, relation.TagID)
		}
	}
	tags := repositories.TagRepository.Find(db, sqls.NewCnd().In("id", tagIDs))
	tagMap := make(map[int64]models.Tag, len(tags))
	for i := range tags {
		tagMap[tags[i].ID] = tags[i]
	}
	for ticketID, orderedTagIDs := range ticketTagMap {
		orderedTags := make([]models.Tag, 0, len(orderedTagIDs))
		for _, tagID := range orderedTagIDs {
			if tag, ok := tagMap[tagID]; ok {
				orderedTags = append(orderedTags, tag)
			}
		}
		aggregate.TagsByTicketID[ticketID] = orderedTags
	}
}

func (s *ticketService) resolveConversationChannel(conversation *models.Conversation) string {
	if conversation == nil || conversation.ChannelID <= 0 {
		return ""
	}
	if channel := ChannelService.Get(conversation.ChannelID); channel != nil {
		return channel.ChannelType
	}
	return ""
}

func normalizeInt64IDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}
