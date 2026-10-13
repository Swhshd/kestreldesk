package repositories

import (
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var EmployeeConversationScopeRepository = &employeeConversationScopeRepository{}

type employeeConversationScopeRepository struct{}

func (r *employeeConversationScopeRepository) User(db *gorm.DB, userID int64) (*models.User, error) {
	var item models.User
	if err := db.Session(&gorm.Session{}).First(&item, "id = ?", userID).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *employeeConversationScopeRepository) Profile(db *gorm.DB, userID int64) (*models.AgentProfile, error) {
	var item models.AgentProfile
	if err := db.Session(&gorm.Session{}).Take(&item, "user_id = ?", userID).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *employeeConversationScopeRepository) Team(db *gorm.DB, teamID int64) (*models.AgentTeam, error) {
	var item models.AgentTeam
	if err := db.Session(&gorm.Session{}).First(&item, "id = ?", teamID).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *employeeConversationScopeRepository) Conversation(db *gorm.DB, conversationID int64) (*models.Conversation, error) {
	var item models.Conversation
	if err := db.First(&item, "id = ?", conversationID).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

// ConversationForUpdate keeps the row used for employee write authorization
// locked through persistence. GORM omits FOR UPDATE for SQLite, whose write
// transactions serialize competing writers, and emits it for MySQL.
func (r *employeeConversationScopeRepository) ConversationForUpdate(db *gorm.DB, conversationID int64) (*models.Conversation, error) {
	var item models.Conversation
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&item, "id = ?", conversationID).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

// Restrict adds the read predicate to the caller's existing filters. Identity
// values must come from the service's resolved employee scope.
func (r *employeeConversationScopeRepository) Restrict(cnd *sqls.Cnd, userID, teamID int64, global, teamWide bool) *sqls.Cnd {
	if cnd == nil {
		cnd = sqls.NewCnd()
	}
	if global {
		return cnd
	}
	if userID <= 0 || teamID <= 0 {
		return cnd.Where("1 = 0")
	}
	if teamWide {
		return cnd.Eq("current_team_id", teamID)
	}
	return cnd.Where("(current_assignee_id = ? OR (current_team_id = ? AND current_assignee_id = 0 AND status = ? AND service_mode <> ?))", userID, teamID, enums.IMConversationStatusPending, enums.IMConversationServiceModeAIOnly)
}
