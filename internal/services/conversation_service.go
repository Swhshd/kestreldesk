package services

// This file has been modified for the KestrelDesk project.

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Swhshd/kestreldesk/internal/events"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/request"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/pkg/eventbus"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"github.com/Swhshd/kestreldesk/internal/pkg/utils"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mlogclub/simple/common/strs"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ConversationService = newConversationService()

func newConversationService() *conversationService {
	return &conversationService{}
}

type conversationService struct {
}

func (s *conversationService) Get(id int64) *models.Conversation {
	if id <= 0 {
		return nil
	}
	return repositories.ConversationRepository.Get(sqls.DB(), id)
}

func (s *conversationService) Find(cnd *sqls.Cnd) []models.Conversation {
	return repositories.ConversationRepository.Find(sqls.DB(), cnd)
}

func (s *conversationService) FindOne(cnd *sqls.Cnd) *models.Conversation {
	return repositories.ConversationRepository.FindOne(sqls.DB(), cnd)
}

func (s *conversationService) FindPageByCnd(cnd *sqls.Cnd) (list []models.Conversation, paging *sqls.Paging) {
	return repositories.ConversationRepository.FindPageByCnd(sqls.DB(), cnd)
}

func (s *conversationService) ListConversations(operator *dto.AuthPrincipal, filter request.AgentConversationFilter, keyword string, paging *sqls.Paging, cnd *sqls.Cnd) ([]models.Conversation, *sqls.Paging, error) {
	// Resolve before examining filters so invalid employee/team relations fail closed.
	scope, err := ConversationScopeService.Resolve(sqls.DB(), operator)
	if err != nil {
		return nil, nil, err
	}
	if cnd == nil {
		cnd = sqls.NewCnd()
	}
	cnd.Page(paging.Page, paging.Limit)

	if strs.IsNotBlank(keyword) {
		keyword = strings.TrimSpace(keyword)
		keywordLike := "%" + keyword + "%"
		cnd.Where("(customer_name LIKE ? OR last_message_summary LIKE ?)", keywordLike, keywordLike)
	}

	switch filter {
	case request.AgentConversationFilterAIServing:
		cnd.Eq("current_assignee_id", 0).Eq("status", enums.IMConversationStatusAIServing).Desc("last_active_at").Desc("id")
	case request.AgentConversationFilterMine:
		cnd.Eq("current_assignee_id", scope.UserID()).Desc("last_active_at").Desc("id")
	case request.AgentConversationFilterActive:
		cnd.Eq("status", enums.IMConversationStatusActive).Desc("last_active_at").Desc("id")
	case request.AgentConversationFilterPending:
		cnd.Eq("current_assignee_id", 0).Eq("status", enums.IMConversationStatusPending).NotEq("service_mode", enums.IMConversationServiceModeAIOnly).Asc("last_active_at").Desc("id")
	case request.AgentConversationFilterClosed:
		cnd.Eq("status", enums.IMConversationStatusClosed).Desc("last_active_at").Desc("id")
	default:
		return nil, nil, errorsx.InvalidParamI18n("error.e0121")
	}

	list, paging := repositories.ConversationRepository.FindPageByCnd(sqls.DB(), scope.Restrict(cnd))
	return list, paging, nil
}

func (s *conversationService) Updates(id int64, columns map[string]interface{}) error {
	return repositories.ConversationRepository.Updates(sqls.DB(), id, columns)
}

func (s *conversationService) getLatestNotFinishedByCustomerID(db *gorm.DB, customerID int64) *models.Conversation {
	if customerID <= 0 {
		return nil
	}
	cnd := sqls.NewCnd()
	cnd.Eq("customer_id", customerID)
	cnd.In("status", []enums.IMConversationStatus{
		enums.IMConversationStatusAIServing,
		enums.IMConversationStatusPending,
		enums.IMConversationStatusActive,
	})
	cnd.Desc("id")
	return repositories.ConversationRepository.FindOne(db, cnd)
}

func (s *conversationService) Create(externalUser openidentity.ExternalUser, channelID, aiAgentID int64) (*models.Conversation, error) {
	return s.createWithCustomerResolver(externalUser, channelID, aiAgentID, func(ctx *sqls.TxContext) (int64, error) {
		return CustomerService.EnsureVerifiedExternalCustomer(ctx, externalUser)
	})
}

// CreateForCustomer keeps the authenticated customer authoritative through matching.
func (s *conversationService) CreateForCustomer(customerID int64, externalUser openidentity.ExternalUser, channelID, aiAgentID int64) (*models.Conversation, error) {
	return s.createWithCustomerResolver(externalUser, channelID, aiAgentID, func(ctx *sqls.TxContext) (int64, error) {
		if err := CustomerService.TouchVerifiedCustomer(ctx, customerID, externalUser); err != nil {
			return 0, err
		}
		return customerID, nil
	})
}

func (s *conversationService) createWithCustomerResolver(externalUser openidentity.ExternalUser, channelID, aiAgentID int64, resolveCustomerID func(*sqls.TxContext) (int64, error)) (*models.Conversation, error) {
	aiAgent := AIAgentService.Get(aiAgentID)
	if aiAgent == nil || aiAgent.Status != enums.StatusOk {
		return nil, errorsx.InvalidParamI18n("error.e0002")
	}

	var conversation *models.Conversation
	var welcomeMessage *models.Message
	created := false
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		customerID, err := resolveCustomerID(ctx)
		if err != nil {
			return err
		}
		customerName := s.getCustomerName(ctx.Tx, customerID)
		if existing := s.getLatestNotFinishedByCustomerID(ctx.Tx, customerID); existing != nil {
			conversation = existing
			if customerName != "" && existing.CustomerName != customerName {
				if err := repositories.ConversationRepository.Updates(ctx.Tx, existing.ID, map[string]any{
					"customer_name": customerName,
					"updated_at":    time.Now(),
				}); err != nil {
					return err
				}
				conversation.CustomerName = customerName
			}
			return nil
		}
		created = true
		now := time.Now()
		conversation = &models.Conversation{
			AIAgentID:         aiAgentID,
			ChannelID:         channelID,
			CustomerID:        customerID,
			CustomerName:      customerName,
			Status:            s.resolveInitialStatus(aiAgent.ServiceMode),
			ServiceMode:       aiAgent.ServiceMode,
			Priority:          0,
			CurrentAssigneeID: 0,
			CurrentTeamID:     0,
			LastMessageAt:     now,
			LastActiveAt:      now,
			AuditFields:       utils.BuildAuditFields(nil),
		}
		if err := ctx.Tx.Create(conversation).Error; err != nil {
			return err
		}
		if err := ConversationParticipantService.CreateCustomerParticipant(ctx, conversation.ID, externalUser); err != nil {
			return err
		}
		if err := ConversationEventLogService.CreateEvent(ctx, conversation.ID, enums.IMEventTypeCreate, enums.IMSenderTypeCustomer, 0, "用户创建会话", ""); err != nil {
			return err
		}
		welcomeMessage, err = MessageService.createAIWelcomeMessage(ctx, conversation, aiAgent, now)
		return err
	}); err != nil {
		return nil, err
	}
	if conversation == nil {
		return nil, errorsx.BusinessErrorI18n(1, "error.conversation.createFailed")
	}
	if !created {
		return conversation, nil
	}

	// 推送会话创建事件
	WsService.PublishConversationChanged(conversation, enums.IMRealtimeEventConversationCreated)
	if welcomeMessage != nil {
		if updatedConversation := s.Get(conversation.ID); updatedConversation != nil {
			WsService.PublishMessageCreated(updatedConversation, welcomeMessage)
			WsService.PublishConversationChanged(updatedConversation, enums.IMRealtimeEventConversationUpdated)
		}
	}

	if aiAgent.ServiceMode == enums.IMConversationServiceModeHumanOnly {
		if _, err := ConversationHumanDispatchService.ApplyHumanOnlyCreate(conversation.ID, *aiAgent); err != nil {
			return nil, err
		}
	}
	return s.Get(conversation.ID), nil
}

func (s *conversationService) AssignConversation(req request.AssignConversationRequest, operator *dto.AuthPrincipal) error {
	if operator == nil {
		return errorsx.UnauthorizedI18n("error.auth.expired")
	}
	var assignedEvent events.ConversationAssignedEvent
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		scope, err := ConversationScopeService.Resolve(ctx.Tx, operator)
		if err != nil {
			return err
		}
		if !scope.IsGlobal() && slices.Contains(operator.Roles, constants.RoleCodeCsUser) {
			return errorsx.ForbiddenI18n("error.e0222")
		}
		conversation, err := repositories.EmployeeConversationScopeRepository.ConversationForUpdate(ctx.Tx, req.ConversationID)
		if err != nil {
			return errorsx.InvalidParamI18n("error.e0116")
		}
		if !scope.Allows(conversation, ConversationScopeRead) || (!scope.IsGlobal() && conversation.CurrentTeamID != scope.TeamID()) {
			return errorsx.ForbiddenI18n("error.e0222")
		}
		if conversation.Status != enums.IMConversationStatusPending {
			return errorsx.InvalidParamI18n("error.e0135")
		}
		if conversation.ServiceMode == enums.IMConversationServiceModeAIOnly {
			return errorsx.ForbiddenI18n("error.e0222")
		}
		if _, err := s.resolveAssignmentTarget(ctx.Tx, req.AssigneeID, scope); err != nil {
			return err
		}
		now := time.Now()
		if err := ConversationAssignmentService.FinishActiveAssignments(ctx, req.ConversationID, now); err != nil {
			return err
		}
		if err := ConversationAssignmentService.CreateAssignment(ctx, req.ConversationID, conversation.CurrentAssigneeID, req.AssigneeID, enums.IMAssignmentTypeAssign, req.Reason, operator, now); err != nil {
			return err
		}
		if err := repositories.ConversationRepository.Updates(ctx.Tx, req.ConversationID, map[string]any{
			"current_assignee_id": req.AssigneeID,
			"status":              enums.IMConversationStatusActive,
			"update_user_id":      operator.UserID,
			"update_user_name":    operator.Username,
			"updated_at":          now,
		}); err != nil {
			return err
		}
		if err := ConversationEventLogService.CreateEvent(ctx, req.ConversationID, enums.IMEventTypeAssign, enums.IMSenderTypeAgent, operator.UserID, "会话已分配", s.buildEventPayload(map[string]any{
			"fromStatus":     conversation.Status,
			"toStatus":       enums.IMConversationStatusActive,
			"fromAssigneeId": conversation.CurrentAssigneeID,
			"toAssigneeId":   req.AssigneeID,
			"reason":         strings.TrimSpace(req.Reason),
		})); err != nil {
			return err
		}
		assignedEvent = events.ConversationAssignedEvent{
			ConversationID: req.ConversationID,
			FromUserID:     conversation.CurrentAssigneeID,
			ToUserID:       req.AssigneeID,
			OperatorID:     operator.UserID,
			Reason:         strings.TrimSpace(req.Reason),
			AssignType:     events.ConversationAssignTypeAssign,
		}
		return nil
	}); err != nil {
		return err
	}
	if conversation := s.Get(req.ConversationID); conversation != nil {
		WsService.PublishConversationChanged(conversation, enums.IMRealtimeEventConversationAssigned)
	}
	eventbus.PublishAsync(context.Background(), assignedEvent)
	return nil
}

func (s *conversationService) AutoAssignConversation(conversationID int64, operator *dto.AuthPrincipal) error {
	if operator == nil {
		return errorsx.UnauthorizedI18n("error.auth.expired")
	}

	var aiAgent *models.AIAgent
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		scope, err := ConversationScopeService.Resolve(ctx.Tx, operator)
		if err != nil {
			return err
		}
		if !scope.IsGlobal() && slices.Contains(operator.Roles, constants.RoleCodeCsUser) {
			return errorsx.ForbiddenI18n("error.e0222")
		}
		conversation, err := repositories.EmployeeConversationScopeRepository.ConversationForUpdate(ctx.Tx, conversationID)
		if err != nil {
			return errorsx.InvalidParamI18n("error.e0116")
		}
		if conversation.Status != enums.IMConversationStatusPending {
			return errorsx.InvalidParamI18n("error.e0136")
		}
		if conversation.CurrentAssigneeID > 0 {
			return errorsx.InvalidParamI18n("error.e0190")
		}
		if conversation.ServiceMode == enums.IMConversationServiceModeAIOnly {
			return errorsx.ForbiddenI18n("error.e0222")
		}
		if !scope.Allows(conversation, ConversationScopeRead) || (!scope.IsGlobal() && conversation.CurrentTeamID != scope.TeamID()) {
			return errorsx.ForbiddenI18n("error.e0222")
		}
		aiAgent = repositories.AIAgentRepository.Get(ctx.Tx, conversation.AIAgentID)
		if aiAgent == nil || aiAgent.Status != enums.StatusOk {
			return errorsx.InvalidParamI18n("error.e0003")
		}
		if !scope.IsGlobal() {
			if !slices.Contains(orderedPositiveIDs(aiAgent.TeamIDs), scope.TeamID()) {
				return errorsx.ForbiddenI18n("error.e0222")
			}
			aiAgent.TeamIDs = strconv.FormatInt(scope.TeamID(), 10)
		}
		return nil
	}); err != nil {
		return err
	}
	result, err := ConversationHumanDispatchService.DispatchPendingConversation(conversationID, *aiAgent, operator)
	if err != nil {
		return err
	}
	if result == nil || result.Decision == HandoffDecisionOffHours {
		return errorsx.InvalidParamI18n("error.e0194")
	}
	return nil
}

func (s *conversationService) TransferConversation(conversationID, toUserID int64, reason string, operator *dto.AuthPrincipal) error {
	if operator == nil {
		return errorsx.UnauthorizedI18n("error.auth.expired")
	}
	if toUserID <= 0 {
		return errorsx.InvalidParamI18n("error.e0278")
	}
	var assignedEvent events.ConversationAssignedEvent
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		scope, err := ConversationScopeService.Resolve(ctx.Tx, operator)
		if err != nil {
			return err
		}
		if !scope.IsGlobal() && slices.Contains(operator.Roles, constants.RoleCodeCsUser) {
			return errorsx.ForbiddenI18n("error.e0222")
		}
		conversation, err := repositories.EmployeeConversationScopeRepository.ConversationForUpdate(ctx.Tx, conversationID)
		if err != nil {
			return errorsx.InvalidParamI18n("error.e0116")
		}
		if !scope.Allows(conversation, ConversationScopeActiveWrite) || (!scope.IsGlobal() && conversation.CurrentTeamID != scope.TeamID()) {
			return errorsx.ForbiddenI18n("error.e0223")
		}
		if conversation.Status != enums.IMConversationStatusActive {
			return errorsx.InvalidParamI18n("error.e0134")
		}
		if conversation.CurrentAssigneeID <= 0 {
			return errorsx.InvalidParamI18n("error.e0193")
		}
		if conversation.CurrentAssigneeID == toUserID {
			return errorsx.InvalidParamI18n("error.e0277")
		}
		if _, err := s.resolveAssignmentTarget(ctx.Tx, toUserID, scope); err != nil {
			return err
		}
		now := time.Now()
		if err := ConversationAssignmentService.FinishActiveAssignments(ctx, conversationID, now); err != nil {
			return err
		}
		if err := ConversationAssignmentService.CreateAssignment(ctx, conversationID, conversation.CurrentAssigneeID, toUserID, enums.IMAssignmentTypeTransfer, reason, operator, now); err != nil {
			return err
		}
		if err := repositories.ConversationRepository.Updates(ctx.Tx, conversationID, map[string]any{
			"current_assignee_id": toUserID,
			"status":              enums.IMConversationStatusActive,
			"update_user_id":      operator.UserID,
			"update_user_name":    operator.Username,
			"updated_at":          now,
		}); err != nil {
			return err
		}
		if err := ConversationEventLogService.CreateEvent(ctx, conversationID, enums.IMEventTypeTransfer, enums.IMSenderTypeAgent, operator.UserID, "会话已转接", s.buildEventPayload(map[string]any{
			"fromStatus":     conversation.Status,
			"toStatus":       enums.IMConversationStatusActive,
			"fromAssigneeId": conversation.CurrentAssigneeID,
			"toAssigneeId":   toUserID,
			"reason":         strings.TrimSpace(reason),
		})); err != nil {
			return err
		}
		assignedEvent = events.ConversationAssignedEvent{
			ConversationID: conversationID,
			FromUserID:     conversation.CurrentAssigneeID,
			ToUserID:       toUserID,
			OperatorID:     operator.UserID,
			Reason:         strings.TrimSpace(reason),
			AssignType:     events.ConversationAssignTypeTransfer,
		}
		return nil
	}); err != nil {
		return err
	}
	if conversation := s.Get(conversationID); conversation != nil {
		WsService.PublishConversationChanged(conversation, enums.IMRealtimeEventConversationTransferred)
	}
	eventbus.PublishAsync(context.Background(), assignedEvent)
	return nil
}

func (s *conversationService) HandoffByAI(conversationID int64, aiAgent models.AIAgent, reason string) error {
	return s.HandoffByAIWithRequestID(conversationID, aiAgent, reason, "")
}

func (s *conversationService) HandoffByAIWithRequestID(conversationID int64, aiAgent models.AIAgent, reason string, requestID string) error {
	if conversationID <= 0 {
		return errorsx.InvalidParamI18n("error.e0116")
	}
	_, err := ConversationHumanDispatchService.HandoffByAIWithRequestID(conversationID, aiAgent, reason, requestID)
	if err != nil {
		slog.Warn("schedule-aware ai handoff failed",
			"requestId", requestID,
			"conversation_id", conversationID,
			"ai_agent_id", aiAgent.ID,
			"error", err)
	}
	return err
}

func (s *conversationService) TryOffHoursHandoffByAI(conversationID int64, aiAgent models.AIAgent, reason string) (bool, error) {
	return s.TryOffHoursHandoffByAIWithRequestID(conversationID, aiAgent, reason, "")
}

func (s *conversationService) TryOffHoursHandoffByAIWithRequestID(conversationID int64, aiAgent models.AIAgent, reason string, requestID string) (bool, error) {
	if conversationID <= 0 {
		return false, errorsx.InvalidParamI18n("error.e0116")
	}
	handled, err := ConversationHumanDispatchService.TryOffHoursHandoffByAIWithRequestID(conversationID, aiAgent, reason, requestID)
	if err != nil {
		slog.Warn("off-hours ai handoff failed",
			"requestId", requestID,
			"conversation_id", conversationID,
			"ai_agent_id", aiAgent.ID,
			"error", err)
	}
	return handled, err
}

func (s *conversationService) CloseConversation(conversationID int64, closeReason string, operator *dto.AuthPrincipal) error {
	if operator == nil {
		return errorsx.UnauthorizedI18n("error.auth.expired")
	}
	return s.closeConversation(conversationID, enums.IMSenderTypeAgent, closeReason, operator)
}

func (s *conversationService) CloseCustomerConversation(conversationID int64, externalUser openidentity.ExternalUser) error {
	conversation := s.Get(conversationID)
	if conversation == nil {
		return errorsx.InvalidParamI18n("error.e0116")
	}
	if !s.IsCustomerConversationOwner(conversation, externalUser) {
		return errorsx.ForbiddenI18n("error.e0222")
	}
	return s.closeConversation(conversationID, enums.IMSenderTypeCustomer, "", nil)
}

func (s *conversationService) CloseVerifiedCustomerConversation(conversationID, customerID int64) error {
	conversation := s.Get(conversationID)
	if conversation == nil {
		return errorsx.InvalidParamI18n("error.e0116")
	}
	if !s.IsVerifiedCustomerConversationOwner(conversation, customerID) {
		return errorsx.ForbiddenI18n("error.e0222")
	}
	return s.closeConversation(conversationID, enums.IMSenderTypeCustomer, "", nil)
}

func (s *conversationService) closeConversation(conversationID int64, senderType enums.IMSenderType, closeReason string, operator *dto.AuthPrincipal) error {
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		var conversation *models.Conversation
		if senderType == enums.IMSenderTypeAgent {
			lockedConversation, err := repositories.EmployeeConversationScopeRepository.ConversationForUpdate(ctx.Tx, conversationID)
			if err == nil {
				conversation = lockedConversation
			}
		} else {
			conversation = repositories.ConversationRepository.Get(ctx.Tx, conversationID)
		}
		if conversation == nil {
			return errorsx.InvalidParamI18n("error.e0116")
		}
		if conversation.Status == enums.IMConversationStatusClosed {
			if senderType == enums.IMSenderTypeAgent {
				scope, err := ConversationScopeService.Resolve(ctx.Tx, operator)
				if err != nil {
					return err
				}
				if !scope.IsGlobal() {
					return errorsx.ForbiddenI18n("error.e0221")
				}
			}
			return nil
		}
		if conversation.Status != enums.IMConversationStatusAIServing &&
			conversation.Status != enums.IMConversationStatusPending &&
			conversation.Status != enums.IMConversationStatusActive {
			return errorsx.InvalidParamI18n("error.e0197")
		}
		var (
			now          = time.Now()
			eventDesc    = "会话已关闭"
			operatorID   int64
			operatorName string
		)
		closeReason = strings.TrimSpace(closeReason)
		if senderType == enums.IMSenderTypeCustomer {
			eventDesc = "客户关闭会话"
		} else {
			if operator == nil {
				return errorsx.InvalidParamI18n("error.e0226")
			}
			if closeReason == "" {
				return errorsx.InvalidParamI18n("error.e0128")
			}
			scope, err := ConversationScopeService.Resolve(ctx.Tx, operator)
			if err != nil {
				return err
			}
			if !scope.Allows(conversation, ConversationScopeClose) {
				return errorsx.ForbiddenI18n("error.e0222")
			}
			operatorID = operator.UserID
			operatorName = operator.Nickname
		}
		if err := ConversationAssignmentService.FinishActiveAssignments(ctx, conversationID, now); err != nil {
			return err
		}
		if err := repositories.ConversationRepository.Updates(ctx.Tx, conversationID, map[string]any{
			"status":           enums.IMConversationStatusClosed,
			"closed_at":        now,
			"closed_by":        operatorID,
			"close_reason":     closeReason,
			"update_user_id":   operatorID,
			"update_user_name": operatorName,
			"updated_at":       now,
		}); err != nil {
			return err
		}
		return ConversationEventLogService.CreateEvent(ctx, conversationID, enums.IMEventTypeClose, senderType, operatorID, eventDesc, s.buildEventPayload(map[string]any{
			"fromStatus":     conversation.Status,
			"toStatus":       enums.IMConversationStatusClosed,
			"fromAssigneeId": conversation.CurrentAssigneeID,
			"toAssigneeId":   conversation.CurrentAssigneeID,
			"closeReason":    closeReason,
		}))
	}); err != nil {
		return err
	}
	if conversation := s.Get(conversationID); conversation != nil {
		WsService.PublishConversationChanged(conversation, enums.IMRealtimeEventConversationClosed)
	}
	return nil
}

// MarkAgentConversationReadToMessage 控制台客服将会话已读推进到指定消息。
func (s *conversationService) MarkAgentConversationReadToMessage(conversationID, messageID int64, operator *dto.AuthPrincipal) error {
	if operator == nil {
		return errorsx.UnauthorizedI18n("error.auth.expired")
	}
	conversation, err := s.GetForEmployee(operator, conversationID)
	if err != nil {
		return err
	}
	changed, err := s.markConversationReadWithActor(conversation, messageID, agentConversationReadActor{operator: operator})
	if err != nil {
		return err
	}
	if changed {
		if updated := s.Get(conversationID); updated != nil {
			WsService.PublishConversationChanged(updated, enums.IMRealtimeEventConversationRead)
		}
	}
	return nil
}

// MarkCustomerConversationReadToMessage IM 客户将会话已读推进到指定消息（需为会话归属外部身份）。
func (s *conversationService) MarkCustomerConversationReadToMessage(conversationID, messageID int64, external *openidentity.ExternalUser) error {
	if external == nil || strings.TrimSpace(external.ExternalID) == "" {
		return errorsx.UnauthorizedI18n("error.e0149")
	}
	conversation := s.Get(conversationID)
	if conversation == nil {
		return errorsx.InvalidParamI18n("error.e0116")
	}
	if !s.IsCustomerConversationOwner(conversation, *external) {
		return errorsx.ForbiddenI18n("error.e0222")
	}
	return s.markCustomerConversationRead(conversation, messageID, external)
}

func (s *conversationService) MarkVerifiedCustomerConversationReadToMessage(conversationID, messageID, customerID int64, external *openidentity.ExternalUser) error {
	if customerID <= 0 || external == nil || strings.TrimSpace(external.ExternalID) == "" || strings.TrimSpace(string(external.ExternalSource)) == "" {
		return errorsx.UnauthorizedI18n("error.e0149")
	}
	conversation := s.Get(conversationID)
	if conversation == nil {
		return errorsx.InvalidParamI18n("error.e0116")
	}
	if !s.IsVerifiedCustomerConversationOwner(conversation, customerID) {
		return errorsx.ForbiddenI18n("error.e0222")
	}
	return s.markCustomerConversationRead(conversation, messageID, external)
}

func (s *conversationService) markCustomerConversationRead(conversation *models.Conversation, messageID int64, external *openidentity.ExternalUser) error {
	changed, err := s.markConversationReadWithActor(conversation, messageID, customerConversationReadActor{external: external})
	if err != nil {
		return err
	}
	if changed {
		if updated := s.Get(conversation.ID); updated != nil {
			WsService.PublishConversationChanged(updated, enums.IMRealtimeEventConversationRead)
		}
	}
	return nil
}

func displayExternalName(ext *openidentity.ExternalUser) string {
	if ext == nil {
		return ""
	}
	if n := strings.TrimSpace(ext.ExternalName); n != "" {
		return n
	}
	return strings.TrimSpace(ext.ExternalID)
}

// conversationReadActor 抽象「读者身份」，供 markConversationReadWithActor 共用（包内私有）。
type conversationReadActor interface {
	isAgentSide() bool
	getReadState(conversationID int64) *models.ConversationReadState
	markRead(ctx *sqls.TxContext, conversation *models.Conversation, targetMessage *models.Message) error
	conversationUpdateAudit() (userID int64, userName string)
}

type agentConversationReadActor struct {
	operator *dto.AuthPrincipal
}

func (a agentConversationReadActor) isAgentSide() bool { return true }

func (a agentConversationReadActor) getReadState(conversationID int64) *models.ConversationReadState {
	return ConversationReadStateService.GetByAgentReader(conversationID, a.operator)
}

func (a agentConversationReadActor) markRead(ctx *sqls.TxContext, conversation *models.Conversation, targetMessage *models.Message) error {
	_, err := ConversationReadStateService.MarkAgentRead(ctx, conversation, a.operator, targetMessage)
	return err
}

func (a agentConversationReadActor) conversationUpdateAudit() (int64, string) {
	if a.operator == nil {
		return 0, ""
	}
	return a.operator.UserID, a.operator.Username
}

type customerConversationReadActor struct {
	external *openidentity.ExternalUser
}

func (a customerConversationReadActor) isAgentSide() bool { return false }

func (a customerConversationReadActor) getReadState(conversationID int64) *models.ConversationReadState {
	return ConversationReadStateService.GetByCustomerReader(conversationID, a.external)
}

func (a customerConversationReadActor) markRead(ctx *sqls.TxContext, conversation *models.Conversation, targetMessage *models.Message) error {
	_, err := ConversationReadStateService.MarkCustomerRead(ctx, conversation, a.external, targetMessage)
	return err
}

func (a customerConversationReadActor) conversationUpdateAudit() (int64, string) {
	return 0, displayExternalName(a.external)
}

func (s *conversationService) markConversationReadWithActor(conversation *models.Conversation, messageID int64, actor conversationReadActor) (bool, error) {
	if conversation == nil {
		return false, errorsx.InvalidParamI18n("error.e0116")
	}
	targetMessage, err := MessageService.GetConversationReadTarget(conversation.ID, messageID)
	if err != nil {
		return false, err
	}
	if targetMessage == nil {
		if agent, ok := actor.(agentConversationReadActor); ok {
			return s.markAgentUnreadWithoutTarget(conversation.ID, agent)
		}
		if actor.isAgentSide() && conversation.AgentUnreadCount == 0 {
			return false, nil
		}
		if !actor.isAgentSide() && conversation.CustomerUnreadCount == 0 {
			return false, nil
		}
		now := time.Now()
		updateUserID, updateUserName := actor.conversationUpdateAudit()
		updates := map[string]any{
			"update_user_id":   updateUserID,
			"update_user_name": updateUserName,
			"updated_at":       now,
		}
		if actor.isAgentSide() {
			updates["agent_unread_count"] = 0
		} else {
			updates["customer_unread_count"] = 0
		}
		return true, s.Updates(conversation.ID, updates)
	}

	currentReadState := actor.getReadState(conversation.ID)
	if currentReadState != nil && currentReadState.LastReadMessageID >= targetMessage.ID {
		if actor.isAgentSide() && conversation.AgentUnreadCount == 0 {
			return false, nil
		}
		if !actor.isAgentSide() && conversation.CustomerUnreadCount == 0 {
			return false, nil
		}
	}

	err = sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		var currentConversation *models.Conversation
		if agent, ok := actor.(agentConversationReadActor); ok {
			var err error
			currentConversation, err = agent.currentConversationForWrite(ctx.Tx, conversation.ID)
			if err != nil {
				return err
			}
		} else {
			currentConversation = repositories.ConversationRepository.Get(ctx.Tx, conversation.ID)
		}
		if currentConversation == nil {
			return errorsx.InvalidParamI18n("error.e0116")
		}
		if err := actor.markRead(ctx, currentConversation, targetMessage); err != nil {
			return err
		}
		agentReadState, customerReadState := ConversationReadStateService.getConversationReadStates(ctx.Tx, currentConversation.ID)
		agentUnreadCount, err := s.countUnreadByState(ctx, currentConversation.ID, agentReadState, enums.IMSenderTypeCustomer)
		if err != nil {
			return err
		}
		customerUnreadCount, err := s.countUnreadByState(ctx, currentConversation.ID, customerReadState, enums.IMSenderTypeAgent, enums.IMSenderTypeAI)
		if err != nil {
			return err
		}
		if actor.isAgentSide() && currentConversation.AgentUnreadCount == agentUnreadCount && currentReadState != nil && currentReadState.LastReadMessageID >= targetMessage.ID {
			return nil
		}
		if !actor.isAgentSide() && currentConversation.CustomerUnreadCount == customerUnreadCount && currentReadState != nil && currentReadState.LastReadMessageID >= targetMessage.ID {
			return nil
		}
		updateUserID, updateUserName := actor.conversationUpdateAudit()
		return repositories.ConversationRepository.Updates(ctx.Tx, currentConversation.ID, map[string]any{
			"agent_unread_count":    agentUnreadCount,
			"customer_unread_count": customerUnreadCount,
			"update_user_id":        updateUserID,
			"update_user_name":      updateUserName,
			"updated_at":            time.Now(),
		})
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *conversationService) countUnreadByState(ctx *sqls.TxContext, conversationID int64, state *models.ConversationReadState, senderTypes ...enums.IMSenderType) (int, error) {
	lastReadMessageID := int64(0)
	if state != nil {
		lastReadMessageID = state.LastReadMessageID
	}
	normalizedSenderTypes := make([]enums.IMSenderType, 0, len(senderTypes))
	for _, senderType := range senderTypes {
		normalizedSenderTypes = append(normalizedSenderTypes, senderType)
	}
	count, err := ConversationReadStateService.CountUnreadMessages(ctx, conversationID, lastReadMessageID, normalizedSenderTypes...)
	return int(count), err
}

func (s *conversationService) IsVerifiedCustomerConversationOwner(conversation *models.Conversation, customerID int64) bool {
	return conversation != nil && customerID > 0 && conversation.CustomerID == customerID
}

func (s *conversationService) IsCustomerConversationOwner(conversation *models.Conversation, externalUser openidentity.ExternalUser) bool {
	if conversation == nil || externalUser.ExternalSource == enums.ExternalSourceGuest {
		return false
	}
	extID := strings.TrimSpace(externalUser.ExternalID)
	if extID == "" || strings.TrimSpace(string(externalUser.ExternalSource)) == "" || conversation.CustomerID <= 0 {
		return false
	}
	identity := repositories.CustomerIdentityRepository.GetBy(sqls.DB(), externalUser.ExternalSource, extID)
	if identity == nil {
		return false
	}
	return identity.CustomerID == conversation.CustomerID
}

func (s *conversationService) BuildConversationSummary(conversation *models.Conversation) string {
	if conversation == nil {
		return ""
	}
	if strings.TrimSpace(conversation.LastMessageSummary) != "" {
		return conversation.LastMessageSummary
	}
	return strings.TrimSpace(conversation.CustomerName)
}

func (s *conversationService) getCustomerName(db *gorm.DB, customerID int64) string {
	if customerID <= 0 {
		return ""
	}
	if customer := repositories.CustomerRepository.Get(db, customerID); customer != nil {
		return strings.TrimSpace(customer.Name)
	}
	return ""
}

func (s *conversationService) resolveAssignmentTarget(db *gorm.DB, userID int64, scope *EmployeeConversationScope) (*models.AgentProfile, error) {
	if db == nil || userID <= 0 || scope == nil {
		return nil, errorsx.InvalidParamI18n("error.e0276")
	}
	locked := db.Clauses(clause.Locking{Strength: "UPDATE"})
	user, err := repositories.EmployeeConversationScopeRepository.User(locked, userID)
	if err != nil || user.Status != enums.StatusOk || user.UserType != enums.UserTypeEmployee || user.DeletedAt != nil {
		return nil, errorsx.InvalidParamI18n("error.e0276")
	}
	profile, err := repositories.EmployeeConversationScopeRepository.Profile(locked, userID)
	if err != nil || profile.Status != enums.StatusOk || profile.TeamID <= 0 {
		return nil, errorsx.InvalidParamI18n("error.e0276")
	}
	team, err := repositories.EmployeeConversationScopeRepository.Team(locked, profile.TeamID)
	if err != nil || team.Status != enums.StatusOk {
		return nil, errorsx.InvalidParamI18n("error.e0276")
	}
	if !scope.IsGlobal() && profile.TeamID != scope.TeamID() {
		return nil, errorsx.ForbiddenI18n("error.e0222")
	}
	return profile, nil
}

func (s *conversationService) isAdmin(operator *dto.AuthPrincipal) bool {
	if operator == nil {
		return false
	}
	return slices.Contains(operator.Roles, constants.RoleCodeSuperAdmin) || slices.Contains(operator.Roles, constants.RoleCodeAdmin)
}

func (s *conversationService) buildEventPayload(payload map[string]any) string {
	if len(payload) == 0 {
		return ""
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(data)
}

// LinkConversationCustomer 将会话绑定到指定客户。
func (s *conversationService) LinkConversationCustomer(conversationID, customerID int64, operator *dto.AuthPrincipal) error {
	if operator == nil {
		return errorsx.UnauthorizedI18n("error.auth.expired")
	}
	if conversationID <= 0 || customerID <= 0 {
		return errorsx.InvalidParamI18n("error.e0133")
	}
	err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		scope, err := ConversationScopeService.Resolve(ctx.Tx.Clauses(clause.Locking{Strength: "UPDATE"}), operator)
		if err != nil {
			return err
		}
		current, err := repositories.EmployeeConversationScopeRepository.ConversationForUpdate(ctx.Tx, conversationID)
		if err != nil {
			if !scope.IsGlobal() {
				return errorsx.ForbiddenI18n("error.e0224")
			}
			return errorsx.InvalidParamI18n("error.e0116")
		}
		if !scope.Allows(current, ConversationScopeRead) || (!scope.IsGlobal() && current.CurrentTeamID != scope.TeamID()) {
			return errorsx.ForbiddenI18n("error.e0224")
		}
		if current.Status == enums.IMConversationStatusClosed {
			return errorsx.InvalidParamI18n("error.e0183")
		}
		if !s.canLinkConversationCustomer(current, operator) {
			return errorsx.ForbiddenI18n("error.e0224")
		}
		cust := repositories.CustomerRepository.Get(ctx.Tx.Clauses(clause.Locking{Strength: "UPDATE"}), customerID)
		if cust == nil || cust.Status == enums.StatusDeleted {
			return errorsx.InvalidParamI18n("error.e0155")
		}
		now := time.Now()
		return repositories.ConversationRepository.Updates(ctx.Tx, conversationID, map[string]any{
			"customer_id":      customerID,
			"customer_name":    strings.TrimSpace(cust.Name),
			"update_user_id":   operator.UserID,
			"update_user_name": operator.Username,
			"updated_at":       now,
		})
	})
	if err != nil {
		return err
	}
	if updated := s.Get(conversationID); updated != nil {
		WsService.PublishConversationChanged(updated, enums.IMRealtimeEventConversationUpdated)
	}
	return nil
}

func (s *conversationService) GetConversationExternalIdentity(conversation *models.Conversation) *models.CustomerIdentity {
	if conversation == nil || conversation.CustomerID <= 0 {
		return nil
	}
	identities := repositories.CustomerIdentityRepository.FindByCustomerID(sqls.DB(), conversation.CustomerID)
	if len(identities) == 0 {
		return nil
	}
	if channel := ChannelService.Get(conversation.ChannelID); channel != nil {
		expected := externalSourceForChannelType(channel.ChannelType)
		if strings.TrimSpace(string(expected)) != "" {
			for i := range identities {
				if identities[i].ExternalSource == expected {
					return &identities[i]
				}
			}
		}
	}
	return &identities[0]
}

func externalSourceForChannelType(channelType string) enums.ExternalSource {
	switch strings.TrimSpace(channelType) {
	case enums.ChannelTypeWxWorkKF:
		return enums.ExternalSourceWxWorkKF
	case enums.ChannelTypeWeb:
		return enums.ExternalSourceGuest
	default:
		return ""
	}
}

func (s *conversationService) canLinkConversationCustomer(conv *models.Conversation, operator *dto.AuthPrincipal) bool {
	if conv == nil || operator == nil {
		return false
	}
	if s.isAdmin(operator) {
		return true
	}
	switch conv.Status {
	case enums.IMConversationStatusAIServing:
		return true
	case enums.IMConversationStatusPending:
		return true
	case enums.IMConversationStatusActive:
		return conv.CurrentAssigneeID == 0 || conv.CurrentAssigneeID == operator.UserID
	default:
		return false
	}
}

func (s *conversationService) resolveInitialStatus(serviceMode enums.IMConversationServiceMode) enums.IMConversationStatus {
	switch serviceMode {
	case enums.IMConversationServiceModeHumanOnly:
		return enums.IMConversationStatusPending
	case enums.IMConversationServiceModeAIOnly, enums.IMConversationServiceModeAIFirst:
		return enums.IMConversationStatusAIServing
	default:
		return enums.IMConversationStatusAIServing
	}
}
