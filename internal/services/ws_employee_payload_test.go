package services

import (
	"encoding/json"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"reflect"
	"testing"
)

var employeeConversationQueueKeys = []string{"conversationId", "status", "serviceMode", "currentAssigneeId", "currentTeamId", "lastMessageId", "lastMessageAt", "lastActiveAt", "customerUnreadCount", "agentUnreadCount"}
var employeeMessageQueueKeys = []string{"conversationId", "messageId", "senderType", "status", "currentAssigneeId"}
var employeeRecallQueueKeys = []string{"conversationId", "messageId"}

func employeeViewPrincipal() *dto.AuthPrincipal {
	return &dto.AuthPrincipal{UserID: 101, UserType: enums.UserTypeEmployee, Status: enums.StatusOk, Roles: []string{constants.RoleCodeCsUser}, Permissions: []string{constants.PermissionConversationView.Code}}
}
func employeePrivateMessage() RealtimeMessageCreatedPayload {
	return RealtimeMessageCreatedPayload{ConversationID: 42, MessageID: 9, RequestID: "synthetic-request", SenderType: enums.IMSenderTypeAgent, SenderID: 101, MessageType: enums.IMMessageTypeText, Content: "pr2-private-marker", Payload: "synthetic-payload", SendStatus: 1, SentAt: "synthetic-time", Status: enums.IMConversationStatusActive, CurrentAssigneeID: 101, Message: response.MessageResponse{ID: 9, ConversationID: 42, Content: "pr2-private-marker", Payload: "synthetic-payload", SenderName: "synthetic-sender", SenderAvatar: "synthetic-avatar", WorkflowRunID: 77}}
}
func TestEmployeeFanoutUsesDeliveryTopic(t *testing.T) {
	for _, topic := range []string{"admin:all", "admin:101"} {
		t.Run(topic, func(t *testing.T) {
			_, svc, principal := employeeRealtimeScopeFixture(t)
			queue := captureEmployeeRealtimeSession(t, svc, "queue", principal, topic)
			svc.PublishToTopics([]string{topic}, RealtimeEvent{EventID: "pr2-test", Type: enums.IMRealtimeEventMessageCreated, Topic: "conversation:42", Data: employeePrivateMessage(), At: "synthetic-at"})
			event := requireCapturedRealtimeEvent(t, queue, enums.IMRealtimeEventMessageCreated)
			assertEmployeeQueueData(t, event, employeeMessageQueueKeys)
			if event.EventID != "pr2-test" || event.Topic != "conversation:42" || event.At != "synthetic-at" {
				t.Fatal("envelope changed")
			}
		})
	}
	for _, p := range []*dto.AuthPrincipal{nil, {UserID: 101}} {
		t.Run("denied", func(t *testing.T) {
			svc := newWsServiceForTest()
			denied := captureEmployeeRealtimeSession(t, svc, "denied", p, "admin:101", "admin:all", "conversation:42")
			svc.PublishToTopics([]string{"admin:101", "admin:all", "conversation:42"}, RealtimeEvent{Type: enums.IMRealtimeEventMessageCreated, Topic: "conversation:42", Data: employeePrivateMessage()})
			requireNoCapturedRealtimeEvent(t, denied)
		})
	}
}
func TestEmployeeQueuePayloadAllowlist(t *testing.T) {
	conversationTypes := []string{enums.IMRealtimeEventConversationCreated, enums.IMRealtimeEventConversationUpdated, enums.IMRealtimeEventConversationAssigned, enums.IMRealtimeEventConversationTransferred, enums.IMRealtimeEventConversationClosed, enums.IMRealtimeEventConversationRead}
	for _, filled := range []bool{false, true} {
		conversation := RealtimeConversationChangedPayload{}
		message := RealtimeMessageCreatedPayload{}
		recall := RealtimeMessageRecalledPayload{}
		name := "zero"
		if filled {
			name = "filled"
			conversation = RealtimeConversationChangedPayload{ConversationID: 42, Status: 3, ServiceMode: 1, CurrentAssigneeID: 101, CurrentTeamID: 1, LastMessageID: 9, LastMessageAt: "synthetic-time", LastActiveAt: "synthetic-time", CustomerUnreadCount: 2, AgentUnreadCount: 3, LastMessageSummary: "synthetic-summary", CustomerLastReadMessageID: 8, CustomerLastReadAt: "synthetic-read", AgentLastReadMessageID: 7, AgentLastReadAt: "synthetic-read"}
			message = employeePrivateMessage()
			recall = RealtimeMessageRecalledPayload{ConversationID: 42, MessageID: 9, SenderType: enums.IMSenderTypeAgent, SenderID: 101, SendStatus: 1, RecalledAt: "synthetic-recall"}
		}
		tests := []struct {
			kind    string
			payload RealtimeEventPayload
			keys    []string
		}{}
		for _, kind := range conversationTypes {
			tests = append(tests, struct {
				kind    string
				payload RealtimeEventPayload
				keys    []string
			}{kind, conversation, employeeConversationQueueKeys})
		}
		tests = append(tests, struct {
			kind    string
			payload RealtimeEventPayload
			keys    []string
		}{enums.IMRealtimeEventMessageCreated, message, employeeMessageQueueKeys}, struct {
			kind    string
			payload RealtimeEventPayload
			keys    []string
		}{enums.IMRealtimeEventMessageRecalled, recall, employeeRecallQueueKeys})
		for _, tc := range tests {
			t.Run(name+"/"+tc.kind, func(t *testing.T) {
				_, svc, principal := employeeRealtimeScopeFixture(t)
				session := captureEmployeeRealtimeSession(t, svc, "queue", principal, "admin:all")
				svc.PublishToTopics([]string{"admin:all"}, RealtimeEvent{Type: tc.kind, Topic: "conversation:42", Data: tc.payload})
				if !filled {
					requireNoCapturedRealtimeEvent(t, session)
					return
				}
				event := requireCapturedRealtimeEvent(t, session, tc.kind)
				assertEmployeeQueueData(t, event, tc.keys)
				var original map[string]any
				body, err := json.Marshal(tc.payload)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(body, &original); err != nil {
					t.Fatal(err)
				}
				for _, key := range tc.keys {
					if !reflect.DeepEqual(event.Data[key], original[key]) {
						t.Fatalf("queue metadata %q changed: got %v want %v", key, event.Data[key], original[key])
					}
				}
			})
		}
	}
}

func TestEmployeeFullAndCustomerDelivery(t *testing.T) {
	_, svc, principal := employeeRealtimeScopeFixture(t)
	full := captureEmployeeRealtimeSession(t, svc, "full", principal, "conversation:42")
	customer := captureEmployeeRealtimeSession(t, svc, "customer", nil, "guest:synthetic")
	customer.Role = realtimeRoleUser
	notification := captureEmployeeRealtimeSession(t, svc, "notification", nil, "notification:101")
	notification.Role = realtimeRoleNotification
	event := RealtimeEvent{EventID: "synthetic-full", Type: enums.IMRealtimeEventMessageCreated, Topic: "conversation:42", Data: employeePrivateMessage(), At: "synthetic-at"}
	svc.PublishToTopics([]string{"conversation:42", "guest:synthetic", "notification:101"}, event)
	var want capturedRealtimeEvent
	body, _ := json.Marshal(event)
	_ = json.Unmarshal(body, &want)
	for _, session := range []*ClientSession{full, customer, notification} {
		got := requireCapturedRealtimeEvent(t, session, event.Type)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("original JSON changed: %#v", got)
		}
		requireNoCapturedRealtimeEvent(t, session)
	}
}
func TestEmployeeMultiDestinationDelivery(t *testing.T) {
	for _, topics := range [][]string{{"admin:all", "admin:101", "conversation:42", "guest:synthetic"}, {"guest:synthetic", "conversation:42", "admin:101", "admin:all"}} {
		t.Run(topics[0], func(t *testing.T) {
			_, svc, principal := employeeRealtimeScopeFixture(t)
			full := captureEmployeeRealtimeSession(t, svc, "full", principal, "admin:all", "admin:101", "conversation:42")
			queue := captureEmployeeRealtimeSession(t, svc, "queue", principal, "admin:all", "admin:101")
			customer := captureEmployeeRealtimeSession(t, svc, "customer", nil, "conversation:42", "guest:synthetic")
			customer.Role = realtimeRoleUser
			svc.PublishToTopics(topics, RealtimeEvent{EventID: "same-id", Type: enums.IMRealtimeEventMessageCreated, Topic: "admin:all", Data: employeePrivateMessage()})
			for _, session := range []*ClientSession{full, customer} {
				event := requireCapturedRealtimeEvent(t, session, enums.IMRealtimeEventMessageCreated)
				if event.Data["content"] != "pr2-private-marker" || event.EventID != "same-id" {
					t.Fatal("full delivery lost")
				}
				requireNoCapturedRealtimeEvent(t, session)
			}
			event := requireCapturedRealtimeEvent(t, queue, enums.IMRealtimeEventMessageCreated)
			assertEmployeeQueueData(t, event, employeeMessageQueueKeys)
			if event.EventID != "same-id" {
				t.Fatal("event ID changed")
			}
			requireNoCapturedRealtimeEvent(t, queue)
		})
	}
}

type employeeUnsupportedPayload struct {
	Content string `json:"content"`
}

func (employeeUnsupportedPayload) realtimeEventPayload() {}
func TestEmployeePayloadFailsClosed(t *testing.T) {
	message := employeePrivateMessage()
	for _, tc := range []struct {
		name, kind string
		data       RealtimeEventPayload
	}{
		{"unknown", "conversation.future", message}, {"mismatched", enums.IMRealtimeEventMessageCreated, RealtimeConversationChangedPayload{}}, {"unsupported", enums.IMRealtimeEventMessageCreated, employeeUnsupportedPayload{Content: "synthetic-secret"}}, {"pointer", enums.IMRealtimeEventMessageCreated, &message}, {"nil", enums.IMRealtimeEventMessageCreated, nil},
		{"recall mismatch", enums.IMRealtimeEventMessageRecalled, message}, {"conversation mismatch", enums.IMRealtimeEventConversationRead, message}, {"resync mismatch", enums.IMRealtimeEventResyncRequired, message}, {"connected mismatch", enums.IMRealtimeEventConnected, message}, {"topics mismatch", enums.IMRealtimeEventSubscribed, message}, {"pong mismatch", enums.IMRealtimeEventPong, message},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, svc, principal := employeeRealtimeScopeFixture(t)
			queue := captureEmployeeRealtimeSession(t, svc, "queue", principal, "admin:all")
			full := captureEmployeeRealtimeSession(t, svc, "full", principal, "conversation:42")
			svc.PublishToTopics([]string{"admin:all", "conversation:42"}, RealtimeEvent{Type: tc.kind, Topic: "conversation:42", Data: tc.data})
			requireNoCapturedRealtimeEvent(t, queue)
			requireNoCapturedRealtimeEvent(t, full)
		})
	}
}
func TestEmployeeControlAndResyncDelivery(t *testing.T) {
	for _, tc := range []struct {
		kind string
		data RealtimeEventPayload
	}{{enums.IMRealtimeEventConnected, RealtimeConnectedPayload{ConnID: "synthetic"}}, {enums.IMRealtimeEventSubscribed, RealtimeTopicsPayload{Topics: []string{"admin:101"}}}, {enums.IMRealtimeEventUnsubscribed, RealtimeTopicsPayload{Topics: []string{"admin:101"}}}, {enums.IMRealtimeEventPong, nil}, {enums.IMRealtimeEventResyncRequired, RealtimeResyncRequiredPayload{Reason: "synthetic-reason"}}} {
		t.Run(tc.kind, func(t *testing.T) {
			_, svc, withViewPrincipal := employeeRealtimeScopeFixture(t)
			noViewPrincipal := *withViewPrincipal
			noViewPrincipal.Permissions = nil
			withView := captureEmployeeRealtimeSession(t, svc, "with", withViewPrincipal, "admin:101")
			noView := captureEmployeeRealtimeSession(t, svc, "without", &noViewPrincipal, "admin:101")
			event := RealtimeEvent{Type: tc.kind, Data: tc.data}
			svc.PublishToTopics([]string{"admin:101"}, event)
			got := requireCapturedRealtimeEvent(t, withView, tc.kind)
			var want capturedRealtimeEvent
			body, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(body, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("control or resync payload changed")
			}
			if tc.kind == enums.IMRealtimeEventResyncRequired {
				requireNoCapturedRealtimeEvent(t, noView)
			} else {
				requireCapturedRealtimeEvent(t, noView, tc.kind)
			}
		})
	}
}
