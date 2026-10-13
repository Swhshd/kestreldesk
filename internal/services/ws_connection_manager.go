package services

// This file has been modified for the KestrelDesk project.

import (
	"log/slog"
	"strings"
	"sync"
	"time"
)

type WsConnectionManager struct {
	mu       sync.RWMutex
	sessions map[string]*ClientSession
	topics   map[string]map[string]*ClientSession
}

type realtimeDeliveryTarget struct {
	Session       *ClientSession
	DeliveryTopic string
}

func newWsConnectionManager() *WsConnectionManager {
	return &WsConnectionManager{
		sessions: make(map[string]*ClientSession),
		topics:   make(map[string]map[string]*ClientSession),
	}
}

func (m *WsConnectionManager) Register(session *ClientSession, defaultTopics []string) int {
	if session == nil {
		return 0
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.sessions[session.ID] = session
	for _, topic := range defaultTopics {
		m.subscribeLocked(session, topic)
	}
	return len(m.sessions)
}

func (m *WsConnectionManager) Unregister(session *ClientSession) int {
	if session == nil {
		return 0
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.sessions, session.ID)
	for topic := range session.Topics {
		m.unsubscribeLocked(session, topic)
	}
	return len(m.sessions)
}

func (m *WsConnectionManager) Subscribe(session *ClientSession, topics []string) []string {
	added, expired := m.subscribeWithEmployeeEligibility(session, topics)
	// Close unregisters through manager.mu, so both admission locks must be
	// released before terminal cleanup.
	if expired {
		m.CloseSession(session)
	}
	return added
}

func (m *WsConnectionManager) subscribeWithEmployeeEligibility(session *ClientSession, topics []string) ([]string, bool) {
	if session == nil || len(topics) == 0 {
		return nil, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	employee := isEmployeeRealtimeSession(session)
	if employee {
		// Match activation's manager.mu -> sendMu order and fence terminal close
		// against membership insertion. Customer admission keeps its own branch.
		session.sendMu.Lock()
		defer session.sendMu.Unlock()
	}
	if session.Closed.Load() {
		return nil, false
	}

	ret := make([]string, 0, len(topics))
	for _, topic := range topics {
		if employee {
			now := time.Now()
			if !canDeliverEmployeeRealtime(session, now) {
				expired := employeeLifecyclePhase(session.employeePhase.Load()) == employeePhaseActive && !now.Before(session.LifecycleDeadline)
				return nil, expired
			}
		}
		if _, exists := session.Topics[topic]; exists {
			continue
		}
		m.subscribeLocked(session, topic)
		ret = append(ret, topic)
	}
	return ret, false
}

func (m *WsConnectionManager) Unsubscribe(session *ClientSession, topics []string, keep map[string]struct{}) []string {
	if session == nil || len(topics) == 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	ret := make([]string, 0, len(topics))
	for _, topic := range topics {
		if _, isDefault := keep[topic]; isDefault {
			continue
		}
		if _, exists := session.Topics[topic]; !exists {
			continue
		}
		m.unsubscribeLocked(session, topic)
		ret = append(ret, topic)
	}
	return ret
}

func (m *WsConnectionManager) FindByTopics(topics []string) []*ClientSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	uniq := make(map[string]*ClientSession)
	for _, topic := range topics {
		for connID, session := range m.topics[topic] {
			uniq[connID] = session
		}
	}

	ret := make([]*ClientSession, 0, len(uniq))
	for _, session := range uniq {
		ret = append(ret, session)
	}
	return ret
}

func (m *WsConnectionManager) HasTopic(topic string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := m.topics[topic]
	return len(sessions) > 0
}

// FindDeliveries preserves each registered session/destination pair so employee
// authorization and audience selection can use the actual fan-out destination.
func (m *WsConnectionManager) FindDeliveries(topics []string) []realtimeDeliveryTarget {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var ret []realtimeDeliveryTarget
	for _, topic := range normalizeRealtimeTopics(topics) {
		for _, session := range m.topics[topic] {
			ret = append(ret, realtimeDeliveryTarget{Session: session, DeliveryTopic: topic})
		}
	}
	return ret
}

func (m *WsConnectionManager) subscribeLocked(session *ClientSession, topic string) {
	if _, exists := session.Topics[topic]; exists {
		return
	}
	if m.topics[topic] == nil {
		m.topics[topic] = make(map[string]*ClientSession)
	}
	m.topics[topic][session.ID] = session
	session.Topics[topic] = struct{}{}
}

func (m *WsConnectionManager) unsubscribeLocked(session *ClientSession, topic string) {
	if sessions, ok := m.topics[topic]; ok {
		delete(sessions, session.ID)
		if len(sessions) == 0 {
			delete(m.topics, topic)
		}
	}
	delete(session.Topics, topic)
}

func (m *WsConnectionManager) CloseSession(session *ClientSession) bool {
	if session == nil {
		return false
	}
	won := false
	session.closeOnce.Do(func() {
		won = true
		session.sendMu.Lock()
		session.cancelPresence()
		session.Closed.Store(true)
		session.clearQueuedWriteAuthorizations()
		close(session.Send)
		session.sendMu.Unlock()

		if timer := session.detachLifecycleTimer(); timer != nil {
			timer.Stop()
		}

		remaining := m.Unregister(session)
		if session.Conn != nil {
			_ = session.Conn.Close()
		}

		var discUserID int64
		var discExternalID string
		if session.EmployeeID > 0 {
			discUserID = session.EmployeeID
		} else if session.Principal != nil {
			discUserID = session.Principal.UserID
		}
		if session.External != nil {
			discExternalID = strings.TrimSpace(session.External.ExternalID)
		}
		slog.Info("realtime client disconnected",
			"connId", session.ID,
			"role", session.Role,
			"userId", discUserID,
			"externalId", discExternalID,
			"terminalType", session.TerminalType,
			"sessionCount", remaining,
		)
	})
	return won
}

func (m *WsConnectionManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// RegisterPending publishes identity for scans without granting topic membership.
// Registration and activation lock manager.mu before sendMu; terminal close
// releases sendMu before unregistering, so the opposite lock order never occurs.
func (m *WsConnectionManager) RegisterPending(s *ClientSession) bool {
	if s == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if !isEmployeeRealtimeSession(s) || s.EmployeeID <= 0 || s.LoginSessionID <= 0 || s.Closed.Load() || employeeLifecyclePhase(s.employeePhase.Load()) != employeePhaseNew {
		return false
	}
	if _, exists := m.sessions[s.ID]; exists {
		return false
	}
	m.sessions[s.ID] = s
	s.employeePhase.Store(uint32(employeePhasePending))
	return true
}

func (m *WsConnectionManager) Activate(s *ClientSession, snapshot EmployeeSessionSnapshot, topics []string) ([]string, bool) {
	if s == nil {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	deadline := employeeLifecycleDeadline(snapshot)
	if m.sessions[s.ID] != s || employeeLifecyclePhase(s.employeePhase.Load()) != employeePhasePending || s.Closed.Load() || !isEmployeeRealtimeSession(s) || snapshot.EmployeeID != s.EmployeeID || snapshot.LoginSessionID != s.LoginSessionID || snapshot.Principal == nil || snapshot.Principal.UserID != s.EmployeeID || !deadline.After(time.Now()) {
		return nil, false
	}
	principal := *snapshot.Principal
	principal.Roles = append([]string(nil), principal.Roles...)
	principal.Permissions = append([]string(nil), principal.Permissions...)
	s.Principal = &principal
	s.LoginSessionExpiresAt = snapshot.LoginSessionExpiresAt
	s.LifecycleDeadline = deadline
	if snapshot.NextAuthzChangeAt != nil {
		expiry := *snapshot.NextAuthzChangeAt
		s.NextAuthzChangeAt = &expiry
	} else {
		s.NextAuthzChangeAt = nil
	}
	if s.Topics == nil {
		s.Topics = make(map[string]struct{})
	}
	for _, topic := range topics {
		m.subscribeLocked(s, topic)
	}
	s.employeePhase.Store(uint32(employeePhaseActive))
	return s.topicList(), true
}

func (m *WsConnectionManager) snapshotEmployeeSessions(loginID int64, employeeIDs map[int64]struct{}, all bool) []*ClientSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var ret []*ClientSession
	for _, s := range m.sessions {
		phase := employeeLifecyclePhase(s.employeePhase.Load())
		if s.Closed.Load() || (phase != employeePhasePending && phase != employeePhaseActive) || !isEmployeeRealtimeSession(s) {
			continue
		}
		_, target := employeeIDs[s.EmployeeID]
		if all || (loginID > 0 && s.LoginSessionID == loginID) || target {
			ret = append(ret, s)
		}
	}
	return ret
}
