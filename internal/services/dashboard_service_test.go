package services

// This file has been modified for the KestrelDesk project.

import (
	"strings"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/i18nx"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

func TestDashboardTextUsesEnglishLocale(t *testing.T) {
	t.Parallel()

	if got := dashboardText(i18nx.LocaleEnUS, "alert.pendingLongWait.title"); got != "Queued conversations are piling up" {
		t.Fatalf("dashboardText() = %q", got)
	}
	if got := dashboardText(i18nx.LocaleZhCN, "alert.pendingLongWait.title"); got != "待接入会话堆积" {
		t.Fatalf("dashboardText() = %q", got)
	}
}

func TestConversationStatusLabelUsesEnglishLocale(t *testing.T) {
	t.Parallel()

	if got := conversationStatusLabel(enums.IMConversationStatusPending, i18nx.LocaleEnUS); got != "Queued" {
		t.Fatalf("conversationStatusLabel() = %q", got)
	}
	if got := conversationStatusLabel(enums.IMConversationStatusPending, i18nx.LocaleZhCN); got != "待接入" {
		t.Fatalf("conversationStatusLabel() = %q", got)
	}
}

func TestDashboardAgentPresenceExpiresAtFreshnessWindow(t *testing.T) {
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-14 * time.Minute)
	windowBoundary := now.Add(-15 * time.Minute)
	stale := now.Add(-15*time.Minute - time.Nanosecond)
	profiles := []models.AgentProfile{
		{UserID: 101, TeamID: 1, LastOnlineAt: &fresh},
		{UserID: 102, TeamID: 1, LastOnlineAt: &windowBoundary},
		{UserID: 103, TeamID: 1, LastOnlineAt: &stale},
		{UserID: 104, TeamID: 1},
	}

	online, _, offline, _ := newDashboardService().buildAgentStats(now, nil, profiles, nil, nil)
	if online != 2 || offline != 2 {
		t.Fatalf("agents at freshness boundary: online=%d offline=%d, want 2 and 2", online, offline)
	}
}

func TestDashboardKnowledgeRetrievalMetricsCountLegacyAndAgentSteps(t *testing.T) {
	previousDB := sqls.DB()
	dbName := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dbName), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqls.SetDB(db)
	t.Cleanup(func() { sqls.SetDB(previousDB) })
	if err := db.AutoMigrate(
		&models.Conversation{}, &models.AgentProfile{}, &models.AgentTeam{}, &models.AgentTeamSchedule{},
		&models.AIAgent{}, &models.Channel{}, &models.KnowledgeRetrieveLog{}, &models.AgentRun{}, &models.AgentStep{},
	); err != nil {
		t.Fatalf("auto-migrate dashboard metric models: %v", err)
	}

	dayStart := startOfDay(time.Now())
	inRange := dayStart.Add(time.Hour)
	outOfRange := dayStart.Add(-time.Nanosecond)
	logs := []models.KnowledgeRetrieveLog{
		{AnswerStatus: 1, CreatedAt: inRange},
		{AnswerStatus: 2, CreatedAt: inRange.Add(time.Minute)},
		{AnswerStatus: 4, CreatedAt: outOfRange},
	}
	if err := db.Create(&logs).Error; err != nil {
		t.Fatalf("create knowledge retrieve logs: %v", err)
	}
	steps := []models.AgentStep{
		{AgentRunID: 1, StepType: "knowledge", StepCode: "knowledge_retrieve", Status: "completed", CreatedAt: inRange},
		{AgentRunID: 2, StepType: "knowledge", StepCode: "knowledge_retrieve", Status: "failed", CreatedAt: inRange.Add(2 * time.Minute)},
		{AgentRunID: 3, StepType: "knowledge", StepCode: "knowledge_retrieve", Status: "failed", CreatedAt: outOfRange},
		{AgentRunID: 4, StepType: "tool", StepCode: "knowledge_retrieve", Status: "failed", CreatedAt: inRange},
		{AgentRunID: 5, StepType: "knowledge", StepCode: "other_step", Status: "failed", CreatedAt: inRange},
		{AgentRunID: 6, StepType: "knowledge", StepCode: "knowledge_retrieve", Status: "cancelled", CreatedAt: inRange},
		{AgentRunID: 7, StepType: "knowledge", StepCode: "knowledge_retrieve", Status: "skipped", CreatedAt: inRange},
		{AgentRunID: 8, StepType: "knowledge", StepCode: "knowledge_retrieve", Status: "unknown", CreatedAt: inRange},
	}
	if err := db.Create(&steps).Error; err != nil {
		t.Fatalf("create agent steps: %v", err)
	}

	countToday := func(tx *gorm.DB) *gorm.DB { return tx.Where("created_at >= ?", dayStart) }
	logTotal := repositories.DashboardRepository.CountKnowledgeRetrieveLogs(db, countToday)
	logFailed := repositories.DashboardRepository.CountKnowledgeRetrieveLogs(db, func(tx *gorm.DB) *gorm.DB {
		return tx.Where("created_at >= ? AND answer_status IN ?", dayStart, []int{2, 3, 4})
	})
	stepTotal, stepFailed := repositories.DashboardRepository.CountAgentKnowledgeRetrievalSteps(db, countToday)

	if logTotal != 2 || logFailed != 1 {
		t.Fatalf("legacy log metrics = (%d total, %d failed), want (2, 1)", logTotal, logFailed)
	}
	if stepTotal != 2 || stepFailed != 1 {
		t.Fatalf("AgentStep metrics = (%d total, %d failed), want (2, 1)", stepTotal, stepFailed)
	}

	overview := newDashboardService().GetOverview("today", i18nx.LocaleEnUS)
	if got, want := overview.AIStats.TodayKnowledgeRetrieves, int64(4); got != want {
		t.Fatalf("TodayKnowledgeRetrieves = %d, want %d (legacy logs plus AgentSteps)", got, want)
	}
	if got, want := overview.AIStats.TodayKnowledgeRetrieveFailCount, int64(2); got != want {
		t.Fatalf("TodayKnowledgeRetrieveFailCount = %d, want %d", got, want)
	}
	if got, want := overview.AIStats.TodayKnowledgeRetrieveFailRate, 50.0; got != want {
		t.Fatalf("TodayKnowledgeRetrieveFailRate = %v, want %v", got, want)
	}

	if err := db.Where("1 = 1").Delete(&models.KnowledgeRetrieveLog{}).Error; err != nil {
		t.Fatalf("delete legacy logs for AgentStep-only case: %v", err)
	}
	overview = newDashboardService().GetOverview("today", i18nx.LocaleEnUS)
	if got, want := overview.AIStats.TodayKnowledgeRetrieves, int64(2); got != want {
		t.Fatalf("AgentStep-only TodayKnowledgeRetrieves = %d, want %d", got, want)
	}
	if got, want := overview.AIStats.TodayKnowledgeRetrieveFailCount, int64(1); got != want {
		t.Fatalf("AgentStep-only failure count = %d, want %d", got, want)
	}
}
