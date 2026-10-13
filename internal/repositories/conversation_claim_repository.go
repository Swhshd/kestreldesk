package repositories

import (
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"gorm.io/gorm"
)

// ClaimWaiting is a compare-and-set transition. The current employee/profile/team
// predicates are also enforced in the write, never from client target identifiers.
func (r *employeeConversationScopeRepository) ClaimWaiting(db *gorm.DB, conversationID, userID, teamID int64, username string, now time.Time) (bool, error) {
	activeTeams := db.Model(&models.AgentTeam{}).Select("id").Where("status = ?", enums.StatusOk)
	activeProfiles := db.Model(&models.AgentProfile{}).Select("team_id").Where("user_id = ? AND status = ? AND team_id IN (?)", userID, enums.StatusOk, activeTeams)
	activeEmployees := db.Model(&models.User{}).Select("id").Where("status = ? AND user_type = ?", enums.StatusOk, enums.UserTypeEmployee)
	result := db.Model(&models.Conversation{}).
		Where("id = ? AND status = ? AND current_assignee_id = 0 AND current_team_id = ? AND service_mode <> ?", conversationID, enums.IMConversationStatusPending, teamID, enums.IMConversationServiceModeAIOnly).
		Where("current_team_id IN (?) AND ? IN (?)", activeProfiles, userID, activeEmployees).
		Updates(map[string]any{"current_assignee_id": userID, "status": enums.IMConversationStatusActive, "update_user_id": userID, "update_user_name": username, "updated_at": now})
	return result.RowsAffected == 1, result.Error
}
