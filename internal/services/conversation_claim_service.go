package services

import (
	"context"
	"slices"
	"time"

	"github.com/Swhshd/kestreldesk/internal/events"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/pkg/eventbus"
	"github.com/Swhshd/kestreldesk/internal/pkg/i18nx"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/mlogclub/simple/sqls"
)

func (s *conversationService) ClaimConversation(conversationID int64, operator *dto.AuthPrincipal) error {
	if operator == nil || !slices.Contains(operator.Permissions, constants.PermissionConversationClaim.Code) {
		return errorsx.ForbiddenI18n("error.e0225")
	}
	err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		scope, err := ConversationScopeService.ResolveClaim(ctx.Tx, operator)
		if err != nil {
			return err
		}
		conversation, err := repositories.EmployeeConversationScopeRepository.ConversationForUpdate(ctx.Tx, conversationID)
		if err != nil || !scope.Allows(conversation, ConversationScopeClaim) {
			return errorsx.ForbiddenI18n("error.e0222")
		}
		now := time.Now()
		claimed, err := repositories.EmployeeConversationScopeRepository.ClaimWaiting(ctx.Tx, conversationID, scope.UserID(), scope.TeamID(), operator.Username, now)
		if err != nil {
			return errorsx.BusinessErrorI18n(1, "error.conversation.claimFailed")
		}
		if !claimed {
			return errorsx.BusinessErrorI18n(1, "error.conversation.claimUnavailable")
		}
		if err := ConversationAssignmentService.FinishActiveAssignments(ctx, conversationID, now); err != nil {
			return errorsx.BusinessErrorI18n(1, "error.conversation.claimFailed")
		}
		if err := ConversationAssignmentService.CreateAssignment(ctx, conversationID, 0, scope.UserID(), enums.IMAssignmentTypeAssign, "", operator, now); err != nil {
			return errorsx.BusinessErrorI18n(1, "error.conversation.claimFailed")
		}
		if err := ConversationEventLogService.CreateEvent(ctx, conversationID, enums.IMEventTypeAssign, enums.IMSenderTypeAgent, operator.UserID, i18nx.Get("conversation.claimed"), s.buildEventPayload(map[string]any{"action": "claim", "fromStatus": enums.IMConversationStatusPending, "toStatus": enums.IMConversationStatusActive, "fromAssigneeId": 0, "toAssigneeId": scope.UserID()})); err != nil {
			return errorsx.BusinessErrorI18n(1, "error.conversation.claimFailed")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if conversation := s.Get(conversationID); conversation != nil {
		WsService.PublishConversationChanged(conversation, enums.IMRealtimeEventConversationAssigned)
	}
	eventbus.PublishAsync(context.Background(), events.ConversationAssignedEvent{ConversationID: conversationID, ToUserID: operator.UserID, OperatorID: operator.UserID, AssignType: events.ConversationAssignTypeAssign})
	return nil
}
