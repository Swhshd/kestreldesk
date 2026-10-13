package services

// This file has been modified for the KestrelDesk project.

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/pkg/i18nx"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"github.com/Swhshd/kestreldesk/internal/pkg/utils"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/mlogclub/simple/web"
)

var employeeWSManager = newWsConnectionManager()
var employeeRealtime = newEmployeeRealtimeLifecycle(employeeWSManager, employeeAuthStateReader{})
var employeePresence = newEmployeePresenceCoordinator(agentProfilePresenceRecorder{})
var WsService = newWsService(employeeWSManager, employeeRealtime, employeePresence)

type agentProfilePresenceRecorder struct{}

func (agentProfilePresenceRecorder) TouchOnlineAt(ctx context.Context, userID int64, now time.Time) error {
	return AgentProfileService.TouchOnlineAt(ctx, userID, now)
}

type wsService struct {
	upgrader                       websocket.Upgrader
	seq                            atomic.Uint64
	manager                        *WsConnectionManager
	lifecycle                      EmployeeSessionLifecycle
	presence                       *employeePresenceCoordinator
	employeeSessionRevalidationGap time.Duration
}

func newWsService(manager *WsConnectionManager, lifecycle EmployeeSessionLifecycle, presence ...*employeePresenceCoordinator) *wsService {
	service := &wsService{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		manager:                        manager,
		lifecycle:                      lifecycle,
		employeeSessionRevalidationGap: realtimePingPeriod,
	}
	if len(presence) > 0 {
		service.presence = presence[0]
	}
	return service
}

func (s *wsService) touchEmployeePresence(session *ClientSession, activation bool) {
	if s == nil || s.presence == nil {
		return
	}
	s.presence.request(session, activation)
}

func (s *wsService) HandleDashboardWS(ctx *gin.Context) {
	principal := AuthService.GetAuthPrincipal(ctx)
	if principal == nil {
		ctx.AbortWithStatusJSON(http.StatusUnauthorized, web.JsonErrorCode(errorsx.CodeAuthUnauthorized, i18nx.T(ctx, "error.auth.expired")))
		return
	}
	if err := s.upgradeConnection(ctx, principal, nil, realtimeRoleAdmin); err != nil {
		slog.Error("upgrade admin websocket failed", "error", err, "path", ctx.Request.URL.Path)
		ctx.Abort()
		return
	}
}

func (s *wsService) HandleDashboardNotificationWS(ctx *gin.Context) {
	principal := AuthService.GetAuthPrincipal(ctx)
	if principal == nil {
		ctx.AbortWithStatusJSON(http.StatusUnauthorized, web.JsonErrorCode(errorsx.CodeAuthUnauthorized, i18nx.T(ctx, "error.auth.expired")))
		return
	}
	if err := s.upgradeConnection(ctx, principal, nil, realtimeRoleNotification); err != nil {
		slog.Error("upgrade dashboard notification websocket failed", "error", err, "path", ctx.Request.URL.Path)
		ctx.Abort()
		return
	}
}

func (s *wsService) HandleOpenWS(ctx *gin.Context) {
	channel := ChannelService.GetEnabledChannel(ctx)
	if channel == nil {
		ctx.AbortWithStatusJSON(http.StatusBadRequest, web.JsonErrorCode(errorsx.CodeInvalidParam, i18nx.T(ctx, "error.e0209")))
		return
	}

	var (
		principal           = AuthService.GetAuthPrincipal(ctx)
		external            *openidentity.ExternalUser
		customerSessionInfo *CustomerSessionVerifyResult
	)
	if principal == nil {
		result, err := CustomerSessionService.VerifyRequest(ctx, channel)
		if err != nil {
			ctx.AbortWithStatusJSON(http.StatusUnauthorized, web.JsonError(err))
			return
		}
		external = result.ExternalUser
		customerSessionInfo = result
	}
	if err := s.upgradeConnection(ctx, principal, external, realtimeRoleUser, customerSessionInfo); err != nil {
		slog.Error("upgrade open im websocket failed", "error", err, "path", ctx.Request.URL.Path, "channelId", channel.ChannelID, "channel_id", channel.ID)
		ctx.Abort()
		return
	}
}

func (s *wsService) upgradeConnection(ctx *gin.Context, principal *dto.AuthPrincipal, external *openidentity.ExternalUser, role string, customerSessionInfo ...*CustomerSessionVerifyResult) error {
	var trusted *authenticatedEmployeeSession
	employee := role == realtimeRoleAdmin || role == realtimeRoleNotification || (role == realtimeRoleUser && external == nil)
	if employee {
		var ok bool
		trusted, ok = AuthService.GetAuthenticatedEmployeeSession(ctx)
		if !ok || trusted.EmployeeID <= 0 || trusted.LoginSessionID <= 0 || trusted.Principal == nil || principal == nil || trusted.Principal.UserID != trusted.EmployeeID || principal.UserID != trusted.EmployeeID || s.lifecycle == nil {
			ctx.AbortWithStatusJSON(http.StatusUnauthorized, web.JsonErrorCode(errorsx.CodeAuthUnauthorized, i18nx.T(ctx, "error.auth.expired")))
			return fmt.Errorf("employee websocket requires trusted authentication metadata")
		}
	}
	conn, err := s.upgrader.Upgrade(ctx.Writer, ctx.Request, nil)
	if err != nil {
		return err
	}

	presenceCtx, presenceCancel := context.WithCancel(context.Background())
	session := &ClientSession{
		ID:             s.nextID("conn"),
		Conn:           conn,
		Principal:      principal,
		External:       external,
		Role:           role,
		TerminalType:   s.resolveTerminalType(ctx, role),
		Topics:         make(map[string]struct{}),
		Send:           make(chan []byte, realtimeSendBufferSize),
		presenceCtx:    presenceCtx,
		presenceCancel: presenceCancel,
	}
	if len(customerSessionInfo) > 0 && customerSessionInfo[0] != nil {
		session.CustomerID = customerSessionInfo[0].CustomerID
	}
	if employee {
		session.EmployeeID = trusted.EmployeeID
		session.LoginSessionID = trusted.LoginSessionID
	}
	session.touch()

	conn.SetReadLimit(realtimeMaxMessageSize)
	_ = conn.SetReadDeadline(time.Now().Add(realtimePongWait))
	conn.SetPongHandler(func(string) error {
		session.touch()
		s.touchEmployeePresence(session, false)
		return conn.SetReadDeadline(time.Now().Add(realtimePongWait))
	})

	var logUserID int64
	var logExternalID string
	if principal != nil {
		logUserID = principal.UserID
	}
	if external != nil {
		logExternalID = strings.TrimSpace(external.ExternalID)
	}

	var connectedTopics []string
	if employee {
		if !s.manager.RegisterPending(session) {
			s.closeSession(session)
			return fmt.Errorf("employee pending registration rejected")
		}
		snapshot, err := s.lifecycle.RevalidateEmployeeSession(session.LoginSessionID, time.Now())
		if err != nil || snapshot.EmployeeID != session.EmployeeID || snapshot.LoginSessionID != session.LoginSessionID || snapshot.Principal == nil || snapshot.Principal.UserID != session.EmployeeID || !employeeLifecycleDeadline(snapshot).After(time.Now()) {
			s.closeSession(session)
			return fmt.Errorf("employee authoritative revalidation rejected")
		}
		if !s.lifecycle.InstallLifecycleTimer(session, employeeLifecycleDeadline(snapshot)) {
			s.closeSession(session)
			return fmt.Errorf("employee lifecycle timer rejected")
		}
		var activated bool
		connectedTopics, activated = s.manager.Activate(session, snapshot, s.employeeDefaultTopics(session, snapshot))
		if !activated {
			s.closeSession(session)
			return fmt.Errorf("employee activation rejected")
		}
		s.touchEmployeePresence(session, true)
	} else {
		s.manager.Register(session, s.defaultTopics(session))
		connectedTopics = session.topicList()
	}
	sessionCount := s.manager.Count()
	slog.Info("realtime client connected",
		"connId", session.ID,
		"role", session.Role,
		"userId", logUserID,
		"externalId", logExternalID,
		"terminalType", session.TerminalType,
		"topicCount", len(connectedTopics),
		"sessionCount", sessionCount,
	)

	session.enqueueEvent(s.newEvent("", RealtimeConnectedEvent{
		Payload: RealtimeConnectedPayload{
			ConnID:       session.ID,
			UserID:       logUserID,
			GuestID:      logExternalID,
			Role:         role,
			TerminalType: session.TerminalType,
			Topics:       connectedTopics,
		},
	}))
	if len(customerSessionInfo) > 0 && customerSessionInfo[0] != nil && customerSessionInfo[0].Refreshed {
		session.enqueueEvent(s.newEvent("", RealtimeCustomerSessionRefreshEvent{
			Payload: RealtimeCustomerSessionRefreshPayload{
				CustomerSessionToken: customerSessionInfo[0].Token,
				ExpiresAt:            customerSessionInfo[0].ExpiresAt.Format(time.DateTime),
			},
		}))
	}
	// Terminal close uses this same lock. Pump creation must linearize before
	// close, and connected/control enqueue must run outside the lock.
	session.sendMu.Lock()
	if session.Closed.Load() {
		session.sendMu.Unlock()
		return fmt.Errorf("websocket closed before pump startup")
	}
	go s.writePump(session)
	go s.readPump(session)
	session.sendMu.Unlock()
	return nil
}

func (s *wsService) readPump(session *ClientSession) {
	defer s.closeSession(session)

	for {
		_, body, err := session.Conn.ReadMessage()
		if err != nil {
			return
		}
		session.touch()

		input := realtimeClientMessage{}
		if err := json.Unmarshal(body, &input); err != nil {
			session.enqueueEvent(s.newEvent("", RealtimeResyncRequiredEvent{
				Payload: RealtimeResyncRequiredPayload{
					Reason: enums.IMRealtimeResyncReasonInvalidPayload,
				},
			}))
			continue
		}

		switch strings.TrimSpace(input.Type) {
		case enums.IMRealtimeClientTypePing:
			s.touchEmployeePresence(session, false)
			session.enqueueEvent(s.newEvent("", RealtimePongEvent{}))
		case enums.IMRealtimeClientTypeSubscribe:
			s.touchEmployeePresence(session, false)
			topics := s.subscribeTopics(session, input.Topics)
			if len(topics) > 0 {
				session.enqueueEvent(s.newEvent("", RealtimeSubscribedEvent{
					Payload: RealtimeTopicsPayload{Topics: topics},
				}))
			}
		case enums.IMRealtimeClientTypeUnsubscribe:
			s.touchEmployeePresence(session, false)
			topics := s.unsubscribeTopics(session, input.Topics)
			if len(topics) > 0 {
				session.enqueueEvent(s.newEvent("", RealtimeUnsubscribedEvent{
					Payload: RealtimeTopicsPayload{Topics: topics},
				}))
			}
		case enums.IMRealtimeClientTypeAck:
			s.touchEmployeePresence(session, false)
			slog.Debug("realtime event ack",
				"connId", session.ID,
				"eventId", strings.TrimSpace(input.EventID),
			)
		default:
			session.enqueueEvent(s.newEvent("", RealtimeResyncRequiredEvent{
				Payload: RealtimeResyncRequiredPayload{
					Reason: enums.IMRealtimeResyncReasonUnsupportedMessageType,
				},
			}))
		}
	}
}

func (s *wsService) writePump(session *ClientSession) {
	ticker := time.NewTicker(realtimePingPeriod)
	defer func() {
		ticker.Stop()
		s.closeSession(session)
	}()
	var employeeRevalidationTicker *time.Ticker
	var employeeRevalidationC <-chan time.Time
	if isEmployeeRealtimeSession(session) {
		interval := s.employeeSessionRevalidationGap
		if interval <= 0 {
			interval = realtimePingPeriod
		}
		employeeRevalidationTicker = time.NewTicker(interval)
		employeeRevalidationC = employeeRevalidationTicker.C
		defer employeeRevalidationTicker.Stop()
	}

	for {
		select {
		case payload, ok := <-session.Send:
			if !ok {
				_ = session.Conn.SetWriteDeadline(time.Now().Add(realtimeWriteWait))
				_ = session.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			session.sendMu.Lock()
			if session.Closed.Load() {
				session.sendMu.Unlock()
				return
			}
			if isEmployeeRealtimeSession(session) && !canDeliverEmployeeRealtime(session, time.Now()) {
				session.sendMu.Unlock()
				s.closeSession(session)
				return
			}
			if authorize := session.takeQueuedWriteAuthorization(payload); authorize != nil && !authorize() {
				session.sendMu.Unlock()
				continue
			}
			if session.Role == realtimeRoleNotification && !s.notificationPayloadAllowedAtSocketWrite(session, payload) {
				session.sendMu.Unlock()
				continue
			}
			// Authorization can take time. Recheck employee expiry immediately
			// before the frame is allowed onto the socket.
			if isEmployeeRealtimeSession(session) && !canDeliverEmployeeRealtime(session, time.Now()) {
				session.sendMu.Unlock()
				s.closeSession(session)
				return
			}
			_ = session.Conn.SetWriteDeadline(time.Now().Add(realtimeWriteWait))
			err := session.Conn.WriteMessage(websocket.TextMessage, payload)
			session.sendMu.Unlock()
			if err != nil {
				return
			}
		case <-ticker.C:
			_ = session.Conn.SetWriteDeadline(time.Now().Add(realtimeWriteWait))
			if err := session.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-employeeRevalidationC:
			if !s.employeeRealtimeSessionStillValid(session, time.Now()) {
				slog.Info("realtime employee session revalidation rejected", "connId", session.ID)
				s.closeSession(session)
				return
			}
		}
	}
}

// A local-operator recovery command runs outside this server process, so its
// process-local invalidation callback cannot close this socket immediately.
// Revalidate employee sockets periodically against the shared session store
// to enforce revocations made by another process.
func (s *wsService) employeeRealtimeSessionStillValid(session *ClientSession, now time.Time) bool {
	if s == nil || s.lifecycle == nil || session == nil || session.LoginSessionID <= 0 {
		return false
	}
	active, err := s.lifecycle.IsEmployeeLoginSessionActive(session.EmployeeID, session.LoginSessionID, now)
	return err == nil && active
}

func (s *wsService) closeSession(session *ClientSession) {
	s.manager.CloseSession(session)
}

func (s *wsService) subscribeTopics(session *ClientSession, topics []string) []string {
	allowed := s.filterAllowedTopics(session, topics)
	if len(allowed) == 0 {
		return nil
	}
	return s.manager.Subscribe(session, allowed)
}

func (s *wsService) unsubscribeTopics(session *ClientSession, topics []string) []string {
	allowed := s.filterAllowedTopics(session, topics)
	if len(allowed) == 0 {
		return nil
	}
	return s.manager.Unsubscribe(session, allowed, sliceToSet(s.defaultTopics(session)))
}

func (s *wsService) PublishMessageCreated(conversation *models.Conversation, message *models.Message) {
	if conversation == nil || message == nil {
		return
	}
	content, payload := utils.BuildRenderableMessage(message)

	event := s.newEvent(s.conversationTopic(conversation.ID), RealtimeMessageCreatedEvent{
		Payload: RealtimeMessageCreatedPayload{
			ConversationID:    conversation.ID,
			MessageID:         message.ID,
			RequestID:         message.RequestID,
			Message:           s.buildRealtimeMessage(message),
			Status:            conversation.Status,
			CurrentAssigneeID: conversation.CurrentAssigneeID,
			SenderType:        message.SenderType,
			SenderID:          message.SenderID,
			MessageType:       message.MessageType,
			Content:           content,
			Payload:           payload,
			SendStatus:        message.SendStatus,
			SentAt:            formatWsTime(message.SentAt),
		},
	})
	s.PublishToTopics(s.routeConversationTopics(conversation), event)
}

func (s *wsService) buildRealtimeMessage(item *models.Message) response.MessageResponse {
	if item == nil {
		return response.MessageResponse{}
	}
	agentReadState, customerReadState := ConversationReadStateService.GetConversationReadStates(item.ConversationID)
	content, payload := utils.BuildRenderableMessage(item)
	ret := response.MessageResponse{
		ID:              item.ID,
		ConversationID:  item.ConversationID,
		RequestID:       item.RequestID,
		WorkflowRunID:   item.WorkflowRunID,
		ClientMsgID:     item.ClientMsgID,
		SenderType:      item.SenderType,
		SenderID:        item.SenderID,
		MessageType:     item.MessageType,
		Content:         content,
		Payload:         payload,
		SendStatus:      item.SendStatus,
		SentAt:          utils.FormatTimePtr(item.SentAt),
		DeliveredAt:     utils.FormatTimePtr(item.DeliveredAt),
		ReadAt:          utils.FormatTimePtr(item.ReadAt),
		CustomerRead:    isRealtimeMessageRead(item, customerReadState),
		CustomerReadAt:  realtimeReadMessageAt(item, customerReadState),
		AgentRead:       isRealtimeMessageRead(item, agentReadState),
		AgentReadAt:     realtimeReadMessageAt(item, agentReadState),
		RecalledAt:      utils.FormatTimePtr(item.RecalledAt),
		QuotedMessageID: item.QuotedMessageID,
	}
	s.fillRealtimeMessageSender(&ret, item)
	return ret
}

func (s *wsService) fillRealtimeMessageSender(ret *response.MessageResponse, item *models.Message) {
	if ret == nil || item == nil || item.SenderID <= 0 {
		return
	}
	switch item.SenderType {
	case enums.IMSenderTypeAI:
		if aiAgent := AIAgentService.Get(item.SenderID); aiAgent != nil {
			ret.SenderName = aiAgent.Name
		}
	case enums.IMSenderTypeAgent:
		if profile := AgentProfileService.GetByUserID(item.SenderID); profile != nil {
			if displayName := strings.TrimSpace(profile.DisplayName); displayName != "" {
				ret.SenderName = displayName
			}
			if avatar := strings.TrimSpace(profile.Avatar); avatar != "" {
				ret.SenderAvatar = avatar
			}
		}
		if ret.SenderName == "" {
			s.fillRealtimeMessageUserName(ret, item.SenderID)
		}
	default:
		s.fillRealtimeMessageUserName(ret, item.SenderID)
	}
}

func (s *wsService) fillRealtimeMessageUserName(ret *response.MessageResponse, userID int64) {
	if ret == nil || userID <= 0 {
		return
	}
	if user := UserService.Get(userID); user != nil {
		ret.SenderName = user.Nickname
		if ret.SenderName == "" {
			ret.SenderName = user.Username
		}
	}
}

func isRealtimeMessageRead(item *models.Message, state *models.ConversationReadState) bool {
	return item != nil && state != nil && state.LastReadMessageID >= item.ID
}

func realtimeReadMessageAt(item *models.Message, state *models.ConversationReadState) string {
	if !isRealtimeMessageRead(item, state) {
		return ""
	}
	return utils.FormatTimePtr(state.LastReadAt)
}

func (s *wsService) PublishMessageRecalled(conversation *models.Conversation, message *models.Message) {
	if conversation == nil || message == nil {
		return
	}

	event := s.newEvent(s.conversationTopic(conversation.ID), RealtimeMessageRecalledEvent{
		Payload: RealtimeMessageRecalledPayload{
			ConversationID: conversation.ID,
			MessageID:      message.ID,
			SenderType:     message.SenderType,
			SenderID:       message.SenderID,
			SendStatus:     message.SendStatus,
			RecalledAt:     formatWsTime(message.RecalledAt),
		},
	})
	s.PublishToTopics(s.routeConversationTopics(conversation), event)
}

func (s *wsService) PublishConversationChanged(conversation *models.Conversation, eventType string) {
	if conversation == nil {
		return
	}
	agentReadState, customerReadState := ConversationReadStateService.GetConversationReadStates(conversation.ID)

	event := s.newEvent(s.conversationTopic(conversation.ID), RealtimeConversationChangedEvent{
		Type: eventType,
		Payload: RealtimeConversationChangedPayload{
			ConversationID:            conversation.ID,
			Status:                    conversation.Status,
			ServiceMode:               conversation.ServiceMode,
			CurrentAssigneeID:         conversation.CurrentAssigneeID,
			CurrentTeamID:             conversation.CurrentTeamID,
			LastMessageID:             conversation.LastMessageID,
			LastMessageAt:             formatWsTime(&conversation.LastMessageAt),
			LastActiveAt:              formatWsTime(&conversation.LastActiveAt),
			LastMessageSummary:        conversation.LastMessageSummary,
			CustomerUnreadCount:       conversation.CustomerUnreadCount,
			AgentUnreadCount:          conversation.AgentUnreadCount,
			CustomerLastReadMessageID: readStateMessageID(customerReadState),
			CustomerLastReadAt:        readStateAt(customerReadState),
			AgentLastReadMessageID:    readStateMessageID(agentReadState),
			AgentLastReadAt:           readStateAt(agentReadState),
		},
	})
	s.PublishToTopics(s.routeConversationTopics(conversation), event)
}

func (s *wsService) PublishNotificationCreated(userID int64, notification response.NotificationResponse) {
	if userID <= 0 || notification.ID <= 0 {
		return
	}
	topic := s.notificationTopic(userID)
	event := s.newEvent(topic, RealtimeNotificationCreatedEvent{
		Payload: RealtimeNotificationCreatedPayload{
			Notification: notification,
		},
	})
	s.PublishToTopic(topic, event)
}

func (s *wsService) PublishResyncRequired(topics []string, reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = enums.IMRealtimeResyncReasonManual
	}
	s.PublishToTopics(topics, s.newEvent("", RealtimeResyncRequiredEvent{
		Payload: RealtimeResyncRequiredPayload{Reason: reason},
	}))
}

func readStateMessageID(state *models.ConversationReadState) int64 {
	if state == nil {
		return 0
	}
	return state.LastReadMessageID
}

func readStateAt(state *models.ConversationReadState) string {
	if state == nil {
		return ""
	}
	return utils.FormatTimePtr(state.LastReadAt)
}

func (s *wsService) Publish(event RealtimeEvent) {
	if strings.TrimSpace(event.Topic) == "" {
		return
	}
	s.PublishToTopic(event.Topic, event)
}

func (s *wsService) PublishToTopic(topic string, event RealtimeEvent) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return
	}
	if event.Topic == "" {
		event.Topic = topic
	}
	s.PublishToTopics([]string{topic}, event)
}

func (s *wsService) PublishToTopics(topics []string, event RealtimeEvent) {
	normalized := normalizeRealtimeTopics(topics)
	if len(normalized) == 0 {
		return
	}

	deliveries := s.manager.FindDeliveries(normalized)
	if len(deliveries) == 0 {
		return
	}

	type selectedDelivery struct {
		event                   RealtimeEvent
		sourceEvent             RealtimeEvent
		topic                   string
		audience                employeeRealtimeAudience
		customerConversationIDs []int64
	}
	selected := make(map[*ClientSession]selectedDelivery)
	class := classifyEmployeeRealtimeEvent(event.Type)
	for _, delivery := range deliveries {
		session, topic := delivery.Session, delivery.DeliveryTopic
		if !s.validateEmployeeDelivery(session, time.Now()) {
			continue
		}
		var customerConversationIDs []int64
		if session.Role == realtimeRoleUser && session.External != nil {
			var valid bool
			customerConversationIDs, valid = customerConversationScopes(topic, event)
			if !valid {
				continue
			}
			allowed := true
			for _, conversationID := range customerConversationIDs {
				if !s.canSubscribeConversation(session, conversationID) {
					allowed = false
					break
				}
			}
			if !allowed {
				continue
			}
		}
		variant, audience := event, employeeAudienceFull
		if session.Role == realtimeRoleAdmin {
			var allowed bool
			variant, allowed = s.employeeEventForDelivery(session, topic, event)
			if !allowed {
				continue
			}
			audience = s.employeeDeliveryAudience(session, topic, class)
		} else if session.Role == realtimeRoleNotification {
			var allowed bool
			variant, allowed = s.notificationEventForDelivery(session, topic, event)
			if !allowed {
				continue
			}
		}
		previous, exists := selected[session]
		if !exists || audience > previous.audience || audience == previous.audience && topic < previous.topic {
			selected[session] = selectedDelivery{
				event: variant, sourceEvent: event, topic: topic, audience: audience,
				customerConversationIDs: customerConversationIDs,
			}
		}
	}

	for session, delivery := range selected {
		deliveryEvent := delivery.event
		if session.Role == realtimeRoleAdmin {
			var allowed bool
			deliveryEvent, allowed = s.employeeEventForDelivery(session, delivery.topic, delivery.sourceEvent)
			if !allowed {
				continue
			}
		} else if session.Role == realtimeRoleNotification {
			var allowed bool
			deliveryEvent, allowed = s.notificationEventForDelivery(session, delivery.topic, delivery.sourceEvent)
			if !allowed {
				continue
			}
		}
		payload, err := json.Marshal(deliveryEvent)
		if err != nil {
			slog.Error("marshal realtime event failed", "error", err, "type", event.Type)
			continue
		}
		if !s.validateEmployeeDelivery(session, time.Now()) {
			continue
		}
		var enqueued bool
		if len(delivery.customerConversationIDs) > 0 {
			authorizeCustomer := func() bool {
				for _, conversationID := range delivery.customerConversationIDs {
					if !s.canSubscribeConversation(session, conversationID) {
						return false
					}
				}
				return true
			}
			enqueued = session.enqueueWithAuthorization(payload, authorizeCustomer)
			if !enqueued && !authorizeCustomer() {
				continue
			}
		} else if session.Role == realtimeRoleAdmin && class != employeeEventControl {
			enqueued = session.enqueueWithAuthorization(payload, func() bool {
				_, authorized := s.employeeEventForDelivery(session, delivery.topic, delivery.sourceEvent)
				return authorized
			})
		} else {
			enqueued = session.enqueue(payload)
		}
		if enqueued {
			continue
		}
		// Enqueue rechecks employee eligibility under sendMu. If its lock wait
		// reached the deadline, terminal close runs here after releasing sendMu.
		if !s.validateEmployeeDelivery(session, time.Now()) {
			continue
		}
		slog.Warn("drop slow realtime client",
			"connId", session.ID,
			"type", event.Type,
			"topic", delivery.topic,
		)
		go s.closeSession(session)
	}
}

func customerConversationScopes(deliveryTopic string, event RealtimeEvent) ([]int64, bool) {
	ids := make([]int64, 0, 2)
	seen := make(map[int64]struct{}, 2)
	addID := func(id int64) {
		if id <= 0 {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	addTopic := func(topic string) bool {
		if !strings.HasPrefix(topic, realtimeTopicConversationPrefix) {
			return true
		}
		id, ok := parseConversationTopic(topic)
		if !ok {
			return false
		}
		addID(id)
		return true
	}
	if !addTopic(deliveryTopic) || !addTopic(event.Topic) {
		return nil, false
	}

	switch payload := event.Data.(type) {
	case RealtimeMessageCreatedPayload:
		addID(payload.ConversationID)
		addID(payload.Message.ConversationID)
	case *RealtimeMessageCreatedPayload:
		if payload != nil {
			addID(payload.ConversationID)
			addID(payload.Message.ConversationID)
		}
	case RealtimeMessageRecalledPayload:
		addID(payload.ConversationID)
	case *RealtimeMessageRecalledPayload:
		if payload != nil {
			addID(payload.ConversationID)
		}
	case RealtimeConversationChangedPayload:
		addID(payload.ConversationID)
	case *RealtimeConversationChangedPayload:
		if payload != nil {
			addID(payload.ConversationID)
		}
	}

	if len(ids) == 0 {
		switch event.Type {
		case enums.IMRealtimeEventMessageCreated, enums.IMRealtimeEventMessageRecalled,
			enums.IMRealtimeEventConversationCreated, enums.IMRealtimeEventConversationUpdated,
			enums.IMRealtimeEventConversationAssigned, enums.IMRealtimeEventConversationTransferred,
			enums.IMRealtimeEventConversationClosed, enums.IMRealtimeEventConversationRead:
			return nil, false
		}
	}
	return ids, true
}

func (s *wsService) routeConversationTopics(conversation *models.Conversation) []string {
	if conversation == nil {
		return nil
	}

	topics := []string{s.conversationTopic(conversation.ID)}
	if conversation.CustomerID > 0 {
		topics = append(topics, s.customerTopic(conversation.CustomerID))
	}
	if conversation.CurrentAssigneeID > 0 {
		topics = append(topics, s.adminTopic(conversation.CurrentAssigneeID))
	} else {
		topics = append(topics, realtimeTopicAdminAll)
	}
	return normalizeRealtimeTopics(topics)
}

func (s *wsService) defaultTopics(session *ClientSession) []string {
	if session == nil {
		return nil
	}

	switch session.Role {
	case realtimeRoleNotification:
		if session.Principal == nil || session.Principal.UserID <= 0 {
			return nil
		}
		return []string{s.notificationTopic(session.Principal.UserID)}
	case realtimeRoleAdmin:
		if session.Principal == nil || session.Principal.UserID <= 0 {
			return nil
		}
		var topics []string
		for _, candidateTopic := range []string{s.adminTopic(session.Principal.UserID), realtimeTopicAdminAll} {
			if s.CanSubscribeTopic(session, candidateTopic) {
				topics = append(topics, candidateTopic)
			}
		}
		return topics
	default:
		if session.External != nil {
			if topic := s.customerTopic(session.CustomerID); topic != "" {
				return []string{topic}
			}
			return nil
		}
		if session.Principal != nil && session.Principal.UserID > 0 {
			return []string{s.userTopic(session.Principal.UserID)}
		}
		return nil
	}
}

// Default admission uses the fresh snapshot before activation publishes its
// principal and topics together. Policy helpers remain independent of phase.
func (s *wsService) employeeDefaultTopics(session *ClientSession, snapshot EmployeeSessionSnapshot) []string {
	if session == nil {
		return nil
	}
	return s.defaultTopics(&ClientSession{Role: session.Role, Principal: snapshot.Principal})
}

func canDeliverEmployeeRealtime(session *ClientSession, now time.Time) bool {
	return session != nil && employeeLifecyclePhase(session.employeePhase.Load()) == employeePhaseActive && !session.Closed.Load() && now.Before(session.LifecycleDeadline)
}

func (s *wsService) validateEmployeeDelivery(session *ClientSession, now time.Time) bool {
	if !isEmployeeRealtimeSession(session) {
		return true
	}
	if canDeliverEmployeeRealtime(session, now) {
		return true
	}
	if employeeLifecyclePhase(session.employeePhase.Load()) == employeePhaseActive && !now.Before(session.LifecycleDeadline) {
		s.closeSession(session)
	}
	return false
}

func (s *wsService) filterAllowedTopics(session *ClientSession, topics []string) []string {
	normalized := normalizeRealtimeTopics(topics)
	if len(normalized) == 0 || session == nil {
		return nil
	}
	if !s.validateEmployeeDelivery(session, time.Now()) {
		return nil
	}
	switch session.Role {
	case realtimeRoleNotification:
		if session.Principal == nil {
			return nil
		}
	case realtimeRoleAdmin:
		var allowed []string
		for _, candidateTopic := range normalized {
			if s.CanSubscribeTopic(session, candidateTopic) {
				allowed = append(allowed, candidateTopic)
			}
		}
		return allowed
	default:
		hasUser := session.Principal != nil && session.Principal.UserID > 0
		if !hasUser && session.CustomerID <= 0 {
			return nil
		}
	}

	defaultTopics := sliceToSet(s.defaultTopics(session))
	ret := make([]string, 0, len(normalized))
	for _, topic := range normalized {
		if _, ok := defaultTopics[topic]; ok {
			ret = append(ret, topic)
			continue
		}
		if conversationID, ok := parseConversationTopic(topic); ok && s.canSubscribeConversation(session, conversationID) {
			ret = append(ret, topic)
		}
	}
	return ret
}

func (s *wsService) canSubscribeConversation(session *ClientSession, conversationID int64) bool {
	if session == nil || conversationID <= 0 {
		return false
	}
	if session.Role == realtimeRoleAdmin {
		return s.CanSubscribeTopic(session, s.conversationTopic(conversationID))
	}
	if session.CustomerID <= 0 {
		return false
	}
	db, cancel := realtimeScopeDB()
	defer cancel()
	if db == nil {
		return false
	}
	conversation := repositories.ConversationRepository.Get(db, conversationID)
	if conversation == nil {
		return false
	}
	return conversation.CustomerID == session.CustomerID
}

func (s *wsService) resolveTerminalType(ctx *gin.Context, role string) string {
	if ctx == nil {
		return "web"
	}
	terminalType := strings.TrimSpace(ctx.Query("terminalType"))
	if terminalType != "" {
		return terminalType
	}
	if role == realtimeRoleAdmin {
		return "dashboard_web"
	}
	return "web"
}

func (s *wsService) newEvent(topic string, event RealtimeDomainEvent) RealtimeEvent {
	if event == nil {
		return RealtimeEvent{
			EventID: s.nextID("evt"),
			Topic:   topic,
			At:      time.Now().Format(time.DateTime),
		}
	}
	return RealtimeEvent{
		EventID: s.nextID("evt"),
		Type:    event.EventType(),
		Topic:   topic,
		Data:    event.EventPayload(),
		At:      time.Now().Format(time.DateTime),
	}
}

func (s *wsService) nextID(prefix string) string {
	seq := s.seq.Add(1)
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), seq)
}

func (s *wsService) userTopic(userID int64) string {
	return realtimeTopicUserPrefix + strconv.FormatInt(userID, 10)
}

func (s *wsService) guestTopic(guestID string) string {
	return realtimeTopicGuestPrefix + strings.TrimSpace(guestID)
}

func (s *wsService) adminTopic(userID int64) string {
	return realtimeTopicAdminPrefix + strconv.FormatInt(userID, 10)
}

func (s *wsService) notificationTopic(userID int64) string {
	return realtimeTopicNotificationPrefix + strconv.FormatInt(userID, 10)
}

func (s *wsService) conversationTopic(conversationID int64) string {
	return realtimeTopicConversationPrefix + strconv.FormatInt(conversationID, 10)
}

func normalizeRealtimeTopics(topics []string) []string {
	if len(topics) == 0 {
		return nil
	}
	ret := make([]string, 0, len(topics))
	seen := make(map[string]struct{}, len(topics))
	for _, topic := range topics {
		item := strings.TrimSpace(topic)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		ret = append(ret, item)
	}
	return ret
}

func parseConversationTopic(topic string) (int64, bool) {
	if !strings.HasPrefix(topic, realtimeTopicConversationPrefix) {
		return 0, false
	}
	value := strings.TrimPrefix(topic, realtimeTopicConversationPrefix)
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func sliceToSet(items []string) map[string]struct{} {
	ret := make(map[string]struct{}, len(items))
	for _, item := range items {
		ret[item] = struct{}{}
	}
	return ret
}

func formatWsTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.Format(time.DateTime)
}
