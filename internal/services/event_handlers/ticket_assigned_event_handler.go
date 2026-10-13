package event_handlers

// This file has been modified for the KestrelDesk project.

import (
	"context"
	"fmt"
	"github.com/Swhshd/kestreldesk/internal/events"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/eventbus"
	"github.com/Swhshd/kestreldesk/internal/pkg/i18nx"
	"github.com/Swhshd/kestreldesk/internal/services"
	"strings"
	"time"

	"github.com/mlogclub/simple/common/strs"
)

func init() {
	eventbus.
		Register[events.TicketAssignedEvent]().
		Subscribe(handleTicketAssignedNotify)
}

func handleTicketAssignedNotify(ctx context.Context, event events.TicketAssignedEvent) error {
	if event.TicketID <= 0 || event.ToUserID <= 0 {
		return nil
	}
	ticket := services.TicketService.Get(event.TicketID)
	if ticket == nil {
		return nil
	}
	content := buildTicketAssignedNotifyBody(ticket, event.ToUserID, event.Reason)
	return services.WxWorkNotifyService.SendTextToAssigneeOrDefault(event.ToUserID, i18nx.Get("notification.ticketAssigned.title"), content)
}

func buildTicketAssignedNotifyBody(ticket *models.Ticket, assigneeID int64, reason string) string {
	if ticket == nil {
		return ""
	}
	lines := []string{
		i18nx.Getf(i18nx.DefaultLocale, "notification.ticketAssigned.wxwork.no", strs.DefaultIfBlank(ticket.TicketNo, fmt.Sprintf("#%d", ticket.ID))),
		i18nx.Getf(i18nx.DefaultLocale, "notification.ticketAssigned.wxwork.title", strs.DefaultIfBlank(ticket.Title, "-")),
		i18nx.Getf(i18nx.DefaultLocale, "notification.ticketAssigned.wxwork.status", enums.GetTicketStatusLabel(ticket.Status)),
		i18nx.Getf(i18nx.DefaultLocale, "notification.assignee", resolveNotifyUserLabel(assigneeID)),
	}
	if strings.TrimSpace(reason) != "" {
		lines = append(lines, i18nx.Getf(i18nx.DefaultLocale, "notification.ticketAssigned.reason", strings.TrimSpace(reason)))
	}
	lines = append(lines, i18nx.Getf(i18nx.DefaultLocale, "notification.time", time.Now().Format("2006-01-02 15:04:05")))
	return strings.Join(lines, "\n")
}
