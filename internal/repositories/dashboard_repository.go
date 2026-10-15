package repositories

// This file has been modified for the KestrelDesk project.

import (
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"time"

	"gorm.io/gorm"
)

var DashboardRepository = newDashboardRepository()

func newDashboardRepository() *dashboardRepository {
	return &dashboardRepository{}
}

type dashboardRepository struct {
}

func (r *dashboardRepository) CountConversations(db *gorm.DB, query func(tx *gorm.DB) *gorm.DB) int64 {
	var count int64
	tx := db.Model(&models.Conversation{})
	if query != nil {
		tx = query(tx)
	}
	tx.Count(&count)
	return count
}

func (r *dashboardRepository) ListConversations(db *gorm.DB, query func(tx *gorm.DB) *gorm.DB) []models.Conversation {
	var list []models.Conversation
	tx := db.Model(&models.Conversation{})
	if query != nil {
		tx = query(tx)
	}
	tx.Find(&list)
	return list
}

func (r *dashboardRepository) ListEnabledAgentProfiles(db *gorm.DB) []models.AgentProfile {
	var list []models.AgentProfile
	db.Model(&models.AgentProfile{}).Where("status = ?", enums.StatusOk).Find(&list)
	return list
}

func (r *dashboardRepository) ListEnabledAgentTeams(db *gorm.DB) []models.AgentTeam {
	var list []models.AgentTeam
	db.Model(&models.AgentTeam{}).Where("status = ?", enums.StatusOk).Order("id asc").Find(&list)
	return list
}

func (r *dashboardRepository) ListActiveTeamSchedules(db *gorm.DB, startAt, endAt time.Time) []models.AgentTeamSchedule {
	var list []models.AgentTeamSchedule
	db.Model(&models.AgentTeamSchedule{}).
		Where("status = ? AND start_at <= ? AND end_at >= ?", enums.StatusOk, endAt, startAt).
		Find(&list)
	return list
}

func (r *dashboardRepository) CountAIAgents(db *gorm.DB, query func(tx *gorm.DB) *gorm.DB) int64 {
	var count int64
	tx := db.Model(&models.AIAgent{})
	if query != nil {
		tx = query(tx)
	}
	tx.Count(&count)
	return count
}

func (r *dashboardRepository) ListAIAgents(db *gorm.DB, query func(tx *gorm.DB) *gorm.DB) []models.AIAgent {
	var list []models.AIAgent
	tx := db.Model(&models.AIAgent{})
	if query != nil {
		tx = query(tx)
	}
	tx.Find(&list)
	return list
}

func (r *dashboardRepository) CountChannels(db *gorm.DB, query func(tx *gorm.DB) *gorm.DB) int64 {
	var count int64
	tx := db.Model(&models.Channel{})
	if query != nil {
		tx = query(tx)
	}
	tx.Count(&count)
	return count
}

func (r *dashboardRepository) CountKnowledgeRetrieveLogs(db *gorm.DB, query func(tx *gorm.DB) *gorm.DB) int64 {
	var count int64
	tx := db.Model(&models.KnowledgeRetrieveLog{})
	if query != nil {
		tx = query(tx)
	}
	tx.Count(&count)
	return count
}

func (r *dashboardRepository) CountAgentKnowledgeRetrievalSteps(db *gorm.DB, query func(tx *gorm.DB) *gorm.DB) (int64, int64) {
	var counts struct {
		TotalCount  int64
		FailedCount int64
	}
	tx := db.Model(&models.AgentStep{}).
		Select("COUNT(*) AS total_count, COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0) AS failed_count", "failed").
		Where("step_type = ? AND step_code = ? AND status IN ?", "knowledge", "knowledge_retrieve", []string{"completed", "failed"})
	if query != nil {
		tx = query(tx)
	}
	tx.Scan(&counts)
	return counts.TotalCount, counts.FailedCount
}

func (r *dashboardRepository) CountAgentRuns(db *gorm.DB, query func(tx *gorm.DB) *gorm.DB) int64 {
	var count int64
	tx := db.Model(&models.AgentRun{})
	if query != nil {
		tx = query(tx)
	}
	tx.Count(&count)
	return count
}
