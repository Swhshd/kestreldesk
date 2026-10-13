package services

import (
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

// FindEmployeePageByCnd intersects caller search filters with the authoritative
// employee read scope. Pagination and counts use the same restricted query.
func (s *conversationService) FindEmployeePageByCnd(operator *dto.AuthPrincipal, cnd *sqls.Cnd) ([]models.Conversation, *sqls.Paging, error) {
	scope, err := ConversationScopeService.Resolve(sqls.DB(), operator)
	if err != nil {
		return nil, nil, err
	}
	list, paging := repositories.ConversationRepository.FindPageByCnd(sqls.DB(), scope.Restrict(cnd))
	return list, paging, nil
}

func (s *conversationService) GetForEmployee(operator *dto.AuthPrincipal, conversationID int64) (*models.Conversation, error) {
	return ConversationScopeService.Authorize(sqls.DB(), operator, conversationID, ConversationScopeRead)
}

func (a agentConversationReadActor) currentConversationForWrite(db *gorm.DB, conversationID int64) (*models.Conversation, error) {
	conversation, err := repositories.EmployeeConversationScopeRepository.ConversationForUpdate(db, conversationID)
	if err != nil {
		return nil, errorsx.ForbiddenI18n("error.e0222")
	}
	scope, err := ConversationScopeService.Resolve(db, a.operator)
	if err != nil {
		return nil, err
	}
	if !scope.Allows(conversation, ConversationScopeRead) {
		return nil, errorsx.ForbiddenI18n("error.e0222")
	}
	return conversation, nil
}

func (s *conversationService) markAgentUnreadWithoutTarget(conversationID int64, actor agentConversationReadActor) (bool, error) {
	changed := false
	err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		current, err := actor.currentConversationForWrite(ctx.Tx, conversationID)
		if err != nil {
			return err
		}
		if current.AgentUnreadCount == 0 {
			return nil
		}
		userID, userName := actor.conversationUpdateAudit()
		if err := repositories.ConversationRepository.Updates(ctx.Tx, conversationID, map[string]any{
			"agent_unread_count": 0,
			"update_user_id":     userID,
			"update_user_name":   userName,
			"updated_at":         time.Now(),
		}); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}
