package services

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/mlogclub/simple/sqls"
)

const realtimeTicketNotificationScopeWait = 2 * time.Second

type employeeConversationQueuePayload struct {
	ConversationID      int64                           `json:"conversationId,omitempty"`
	Status              enums.IMConversationStatus      `json:"status,omitempty"`
	ServiceMode         enums.IMConversationServiceMode `json:"serviceMode,omitempty"`
	CurrentAssigneeID   int64                           `json:"currentAssigneeId,omitempty"`
	CurrentTeamID       int64                           `json:"currentTeamId,omitempty"`
	LastMessageID       int64                           `json:"lastMessageId,omitempty"`
	LastMessageAt       string                          `json:"lastMessageAt,omitempty"`
	LastActiveAt        string                          `json:"lastActiveAt,omitempty"`
	CustomerUnreadCount int                             `json:"customerUnreadCount,omitempty"`
	AgentUnreadCount    int                             `json:"agentUnreadCount,omitempty"`
}

func (employeeConversationQueuePayload) realtimeEventPayload() {}

type employeeMessageQueuePayload struct {
	ConversationID    int64                      `json:"conversationId,omitempty"`
	MessageID         int64                      `json:"messageId,omitempty"`
	SenderType        enums.IMSenderType         `json:"senderType,omitempty"`
	Status            enums.IMConversationStatus `json:"status,omitempty"`
	CurrentAssigneeID int64                      `json:"currentAssigneeId,omitempty"`
}

func (employeeMessageQueuePayload) realtimeEventPayload() {}

type employeeMessageRecallQueuePayload struct {
	ConversationID int64 `json:"conversationId,omitempty"`
	MessageID      int64 `json:"messageId,omitempty"`
}

func (employeeMessageRecallQueuePayload) realtimeEventPayload() {}

// Only the actual registered delivery destination selects authorization and
// audience. Concrete payload validation also applies to full destinations.
func (s *wsService) employeeEventForDelivery(session *ClientSession, deliveryTopic string, event RealtimeEvent) (RealtimeEvent, bool) {
	class := classifyEmployeeRealtimeEvent(event.Type)
	if !s.CanReceiveEvent(session, deliveryTopic, class) {
		return RealtimeEvent{}, false
	}
	audience := s.employeeDeliveryAudience(session, deliveryTopic, class)
	var queuePayload RealtimeEventPayload
	switch class {
	case employeeEventConversationChanged:
		payload, ok := event.Data.(RealtimeConversationChangedPayload)
		if !ok || !s.canReceiveEmployeeConversation(session, deliveryTopic, payload.ConversationID) {
			return RealtimeEvent{}, false
		}
		queuePayload = employeeConversationQueuePayload{
			ConversationID:      payload.ConversationID,
			Status:              payload.Status,
			ServiceMode:         payload.ServiceMode,
			CurrentAssigneeID:   payload.CurrentAssigneeID,
			CurrentTeamID:       payload.CurrentTeamID,
			LastMessageID:       payload.LastMessageID,
			LastMessageAt:       payload.LastMessageAt,
			LastActiveAt:        payload.LastActiveAt,
			CustomerUnreadCount: payload.CustomerUnreadCount,
			AgentUnreadCount:    payload.AgentUnreadCount,
		}
	case employeeEventMessageCreated:
		payload, ok := event.Data.(RealtimeMessageCreatedPayload)
		if !ok || !s.canReceiveEmployeeConversation(session, deliveryTopic, payload.ConversationID) {
			return RealtimeEvent{}, false
		}
		queuePayload = employeeMessageQueuePayload{
			ConversationID:    payload.ConversationID,
			MessageID:         payload.MessageID,
			SenderType:        payload.SenderType,
			Status:            payload.Status,
			CurrentAssigneeID: payload.CurrentAssigneeID,
		}
	case employeeEventMessageRecalled:
		payload, ok := event.Data.(RealtimeMessageRecalledPayload)
		if !ok || !s.canReceiveEmployeeConversation(session, deliveryTopic, payload.ConversationID) {
			return RealtimeEvent{}, false
		}
		queuePayload = employeeMessageRecallQueuePayload{ConversationID: payload.ConversationID, MessageID: payload.MessageID}
	case employeeEventResync:
		payload, ok := event.Data.(RealtimeResyncRequiredPayload)
		if !ok {
			return RealtimeEvent{}, false
		}
		queuePayload = payload
	case employeeEventControl:
		switch event.Type {
		case enums.IMRealtimeEventConnected:
			if _, ok := event.Data.(RealtimeConnectedPayload); !ok {
				return RealtimeEvent{}, false
			}
		case enums.IMRealtimeEventSubscribed, enums.IMRealtimeEventUnsubscribed:
			if _, ok := event.Data.(RealtimeTopicsPayload); !ok {
				return RealtimeEvent{}, false
			}
		case enums.IMRealtimeEventPong:
			if event.Data != nil {
				return RealtimeEvent{}, false
			}
		default:
			return RealtimeEvent{}, false
		}
		queuePayload = event.Data
	default:
		return RealtimeEvent{}, false
	}
	if audience == employeeAudienceQueue {
		event.Data = queuePayload
	}
	return event, true
}

// notificationEventForDelivery rechecks Ticket access for the employee who
// will receive a Ticket notification. Other notification types keep their
// existing delivery path and payload.
func (s *wsService) notificationEventForDelivery(session *ClientSession, deliveryTopic string, event RealtimeEvent) (RealtimeEvent, bool) {
	if event.Type != enums.IMRealtimeEventNotificationCreated {
		return event, true
	}
	payload, ok := event.Data.(RealtimeNotificationCreatedPayload)
	if !ok {
		pointerPayload, pointerOK := event.Data.(*RealtimeNotificationCreatedPayload)
		if !pointerOK || pointerPayload == nil {
			return RealtimeEvent{}, false
		}
		payload = *pointerPayload
	}
	notification := payload.Notification
	if notification.BizType != "ticket" {
		return event, true
	}
	if !s.ticketNotificationAllowedForDelivery(session, deliveryTopic, notification) {
		return RealtimeEvent{}, false
	}
	return event, true
}

// notificationPayloadAllowedAtSocketWrite checks the serialized notification
// again after it leaves the per-session queue. Ticket scope may change while a
// slow client keeps queued payloads pending, so the enqueue-time check alone is
// not a delivery authorization decision.
func (s *wsService) notificationPayloadAllowedAtSocketWrite(session *ClientSession, payload []byte) bool {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return false
	}
	if envelope.Type != enums.IMRealtimeEventNotificationCreated {
		return true
	}
	var event struct {
		Topic string `json:"topic"`
		Data  struct {
			Notification *response.NotificationResponse `json:"notification"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &event); err != nil || event.Data.Notification == nil {
		return false
	}
	notification := *event.Data.Notification
	if notification.BizType != "ticket" {
		return true
	}
	return s.ticketNotificationAllowedForDelivery(session, event.Topic, notification)
}

func (s *wsService) ticketNotificationAllowedForDelivery(session *ClientSession, deliveryTopic string, notification response.NotificationResponse) bool {
	if session == nil || session.Role != realtimeRoleNotification || session.EmployeeID <= 0 ||
		session.Principal == nil || session.Principal.UserID != session.EmployeeID ||
		notification.RecipientUserID != session.EmployeeID || notification.BizID <= 0 ||
		deliveryTopic != s.notificationTopic(session.EmployeeID) {
		return false
	}
	db := sqls.DB()
	if db == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), realtimeTicketNotificationScopeWait)
	defer cancel()
	db = db.WithContext(ctx)
	scope, err := TicketAccessPolicyService.Resolve(db, session.Principal)
	if err != nil || scope == nil {
		return false
	}
	if _, err := scope.FindTicket(db, notification.BizID); err != nil {
		return false
	}
	return true
}
