package event_handlers

// This file has been modified for the KestrelDesk project.

import (
	"context"
	"fmt"
	"github.com/Swhshd/kestreldesk/internal/events"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/eventbus"
	"github.com/Swhshd/kestreldesk/internal/services"
	"strings"
	"time"

	"github.com/mlogclub/simple/common/strs"
)

func init() {
	eventbus.
		Register[events.TicketCreatedEvent]().
		Subscribe(handleTicketCreatedNotify)
}

func handleTicketCreatedNotify(ctx context.Context, event events.TicketCreatedEvent) error {
	if event.TicketID <= 0 {
		return nil
	}
	ticket := services.TicketService.Get(event.TicketID)
	if ticket == nil {
		return nil
	}
	content := buildTicketCreatedNotifyBody(ticket)
	return services.WxWorkNotifyService.SendTextToAssigneeOrDefault(ticket.CurrentAssigneeID, "工单创建提醒", content)
}

func buildTicketCreatedNotifyBody(ticket *models.Ticket) string {
	if ticket == nil {
		return ""
	}
	lines := []string{
		fmt.Sprintf("工单号: %s", strs.DefaultIfBlank(ticket.TicketNo, fmt.Sprintf("#%d", ticket.ID))),
		fmt.Sprintf("工单标题: %s", strs.DefaultIfBlank(ticket.Title, "-")),
		fmt.Sprintf("工单来源: %s", strs.DefaultIfBlank(string(ticket.Source), "-")),
		fmt.Sprintf("当前状态: %s", enums.GetTicketStatusLabel(ticket.Status)),
	}
	if ticket.CurrentAssigneeID > 0 {
		lines = append(lines, fmt.Sprintf("处理人: %s", resolveNotifyUserLabel(ticket.CurrentAssigneeID)))
	}
	lines = append(lines, fmt.Sprintf("时间: %s", time.Now().Format("2006-01-02 15:04:05")))
	return strings.Join(lines, "\n")
}
