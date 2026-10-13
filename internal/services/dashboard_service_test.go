package services

// This file has been modified for the KestrelDesk project.

import (
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/i18nx"
	"testing"
	"time"
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
