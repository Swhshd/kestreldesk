package services

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

const realtimeScopeLookupWait = 2 * time.Second

func realtimeScopeDB() (*gorm.DB, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), realtimeScopeLookupWait)
	db := sqls.DB()
	if db == nil {
		cancel()
		return nil, func() {}
	}
	return db.WithContext(ctx), cancel
}

type employeeRealtimeEventClass uint8

const (
	employeeEventUnknown employeeRealtimeEventClass = iota
	employeeEventControl
	employeeEventConversationChanged
	employeeEventMessageCreated
	employeeEventMessageRecalled
	employeeEventResync
)

type employeeRealtimeAudience uint8

const (
	employeeAudienceDenied employeeRealtimeAudience = iota
	employeeAudienceQueue
	employeeAudienceFull
)

func classifyEmployeeRealtimeEvent(eventType string) employeeRealtimeEventClass {
	switch eventType {
	case enums.IMRealtimeEventConnected, enums.IMRealtimeEventPong,
		enums.IMRealtimeEventSubscribed, enums.IMRealtimeEventUnsubscribed:
		return employeeEventControl
	case enums.IMRealtimeEventConversationCreated, enums.IMRealtimeEventConversationUpdated,
		enums.IMRealtimeEventConversationAssigned, enums.IMRealtimeEventConversationTransferred,
		enums.IMRealtimeEventConversationClosed, enums.IMRealtimeEventConversationRead:
		return employeeEventConversationChanged
	case enums.IMRealtimeEventMessageCreated:
		return employeeEventMessageCreated
	case enums.IMRealtimeEventMessageRecalled:
		return employeeEventMessageRecalled
	case enums.IMRealtimeEventResyncRequired:
		return employeeEventResync
	default:
		return employeeEventUnknown
	}
}

func employeeHasConversationView(session *ClientSession) bool {
	return session != nil && session.Role == realtimeRoleAdmin &&
		session.Principal != nil && session.Principal.UserID > 0 &&
		slices.Contains(session.Principal.Permissions, constants.PermissionConversationView.Code)
}

// CanSubscribeTopic admits an employee registration destination. Customer and
// notification registrations retain their existing service branches.
func (s *wsService) CanSubscribeTopic(session *ClientSession, candidateTopic string) bool {
	if session == nil || session.Role != realtimeRoleAdmin || session.Principal == nil || session.Principal.UserID <= 0 {
		return false
	}
	if candidateTopic == s.adminTopic(session.Principal.UserID) {
		return true
	}
	if !employeeHasConversationView(session) {
		return false
	}
	if candidateTopic == realtimeTopicAdminAll {
		return true
	}
	conversationID, ok := parseConversationTopic(candidateTopic)
	return ok && s.canReceiveEmployeeConversation(session, candidateTopic, conversationID)
}

// CanReceiveEvent takes the actual registered destination selected by fan-out.
// Registry membership is supplied by the manager's locked delivery lookup; the
// event's serialized topic and the session's Topics map are not policy inputs.
func (s *wsService) CanReceiveEvent(session *ClientSession, deliveryTopic string, eventClass employeeRealtimeEventClass) bool {
	if !s.CanSubscribeTopic(session, deliveryTopic) {
		return false
	}
	switch eventClass {
	case employeeEventControl:
		return true
	case employeeEventConversationChanged, employeeEventMessageCreated, employeeEventMessageRecalled, employeeEventResync:
		if !employeeHasConversationView(session) {
			return false
		}
		if conversationID, ok := parseConversationTopic(deliveryTopic); ok {
			return s.canReceiveEmployeeConversation(session, deliveryTopic, conversationID)
		}
		if eventClass == employeeEventResync {
			return s.hasCurrentEmployeeRealtimeScope(session)
		}
		return true
	default:
		return false
	}
}

func (s *wsService) hasCurrentEmployeeRealtimeScope(session *ClientSession) bool {
	if session == nil || session.Role != realtimeRoleAdmin || session.Principal == nil ||
		session.EmployeeID <= 0 || session.EmployeeID != session.Principal.UserID || !employeeHasConversationView(session) {
		return false
	}
	db, cancel := realtimeScopeDB()
	defer cancel()
	if db == nil {
		return false
	}
	_, err := ConversationScopeService.Resolve(db, session.Principal)
	return err == nil
}

// canReceiveEmployeeConversation resolves identity and conversation state from
// the database for every admission or delivery check. The registered topic is
// only a destination; it never substitutes for current conversation scope.
func (s *wsService) canReceiveEmployeeConversation(session *ClientSession, deliveryTopic string, conversationID int64) bool {
	if session == nil || session.Role != realtimeRoleAdmin || session.Principal == nil ||
		session.EmployeeID <= 0 || session.EmployeeID != session.Principal.UserID || conversationID <= 0 || !employeeHasConversationView(session) {
		return false
	}
	if topicConversationID, ok := parseConversationTopic(deliveryTopic); ok && topicConversationID != conversationID {
		return false
	}
	db, cancel := realtimeScopeDB()
	defer cancel()
	if db == nil {
		return false
	}
	conversation, err := ConversationScopeService.Authorize(db, session.Principal, conversationID, ConversationScopeRead)
	if err != nil {
		return false
	}
	if strings.HasPrefix(deliveryTopic, realtimeTopicAdminPrefix) &&
		(deliveryTopic == realtimeTopicAdminAll || conversation.CurrentAssigneeID == 0) {
		return conversation.Status == enums.IMConversationStatusPending &&
			conversation.CurrentAssigneeID == 0 &&
			conversation.ServiceMode != enums.IMConversationServiceModeAIOnly
	}
	return true
}

func (s *wsService) employeeDeliveryAudience(session *ClientSession, deliveryTopic string, eventClass employeeRealtimeEventClass) employeeRealtimeAudience {
	if !s.CanReceiveEvent(session, deliveryTopic, eventClass) {
		return employeeAudienceDenied
	}
	if _, ok := parseConversationTopic(deliveryTopic); ok {
		return employeeAudienceFull
	}
	return employeeAudienceQueue
}
