package services

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

const (
	agentPresenceTouchInterval = time.Minute
	agentPresenceWriteTimeout  = 3 * time.Second
	agentPresenceRetryBase     = time.Second
	agentPresenceRetryMax      = 30 * time.Second
	agentPresenceStateTTL      = 5 * time.Minute
)

type employeePresenceStore interface {
	TouchOnlineAt(ctx context.Context, userID int64, now time.Time) error
}

type employeePresenceOptions struct {
	now           func() time.Time
	touchInterval time.Duration
	writeTimeout  time.Duration
	retryBase     time.Duration
	retryMax      time.Duration
	stateTTL      time.Duration
}

type employeePresenceEntry struct {
	lastSuccess      time.Time
	lastActivity     time.Time
	retryAt          time.Time
	retryAttempt     int
	pending          bool
	inFlight         bool
	forceAfterFlight bool
	sessions         map[*ClientSession]struct{}
}

type employeePresenceWork struct {
	userID  int64
	session *ClientSession
}

// employeePresenceCoordinator keeps database work off the WebSocket read and
// Pong callbacks. A single worker bounds concurrent SQL writes; pending users
// are coalesced in the map and failed writes retry with capped backoff.
type employeePresenceCoordinator struct {
	store   employeePresenceStore
	options employeePresenceOptions
	ctx     context.Context
	cancel  context.CancelFunc
	wake    chan struct{}

	mu      sync.Mutex
	entries map[int64]*employeePresenceEntry
}

func newEmployeePresenceCoordinator(store employeePresenceStore) *employeePresenceCoordinator {
	return newEmployeePresenceCoordinatorWithOptions(store, employeePresenceOptions{})
}

func newEmployeePresenceCoordinatorWithOptions(store employeePresenceStore, options employeePresenceOptions) *employeePresenceCoordinator {
	if options.now == nil {
		options.now = time.Now
	}
	if options.touchInterval <= 0 {
		options.touchInterval = agentPresenceTouchInterval
	}
	if options.writeTimeout <= 0 {
		options.writeTimeout = agentPresenceWriteTimeout
	}
	if options.retryBase <= 0 {
		options.retryBase = agentPresenceRetryBase
	}
	if options.retryMax < options.retryBase {
		options.retryMax = agentPresenceRetryMax
	}
	if options.stateTTL <= 0 {
		options.stateTTL = agentPresenceStateTTL
	}
	ctx, cancel := context.WithCancel(context.Background())
	coordinator := &employeePresenceCoordinator{
		store: store, options: options, ctx: ctx, cancel: cancel,
		wake:    make(chan struct{}, 1),
		entries: make(map[int64]*employeePresenceEntry),
	}
	go coordinator.run()
	return coordinator
}

func (p *employeePresenceCoordinator) Close() {
	if p != nil && p.cancel != nil {
		p.cancel()
	}
}

// request is intentionally non-blocking. force=true is used exactly once
// after authenticated activation; ordinary Pong/activity touches obey the
// successful-write lease interval.
func (p *employeePresenceCoordinator) request(session *ClientSession, force bool) {
	if p == nil || p.store == nil {
		return
	}
	now := p.options.now()
	if !eligibleForEmployeePresence(session, now) {
		return
	}

	p.mu.Lock()
	entry := p.entries[session.EmployeeID]
	if entry == nil {
		entry = &employeePresenceEntry{sessions: make(map[*ClientSession]struct{})}
		p.entries[session.EmployeeID] = entry
	}
	entry.lastActivity = now
	switch {
	case force && entry.inFlight:
		entry.sessions[session] = struct{}{}
		entry.forceAfterFlight = true
	case force:
		entry.sessions[session] = struct{}{}
		entry.pending = true
		entry.retryAt = now
	case entry.pending:
		entry.sessions[session] = struct{}{}
		// The current queued write covers this activity. A failed write keeps
		// the user pending and retries automatically.
	case entry.inFlight:
		entry.sessions[session] = struct{}{}
		// The current queued or in-flight write covers this activity. A failed
		// in-flight write keeps the user pending and retries automatically.
	case entry.lastSuccess.IsZero() || now.Sub(entry.lastSuccess) >= p.options.touchInterval:
		entry.sessions[session] = struct{}{}
		entry.pending = true
		entry.retryAt = now
	default:
		// A heartbeat inside the successful-write lease interval needs no
		// follow-up write, so do not retain the socket just for its activity.
	}
	p.mu.Unlock()
	p.signal()
}

func eligibleForEmployeePresence(session *ClientSession, now time.Time) bool {
	return isEmployeeRealtimeSession(session) && session.EmployeeID > 0 &&
		session.LoginSessionID > 0 && session.Principal != nil &&
		session.Principal.UserID == session.EmployeeID &&
		employeeLifecyclePhase(session.employeePhase.Load()) == employeePhaseActive &&
		!session.Closed.Load() && now.Before(session.LifecycleDeadline)
}

func (p *employeePresenceCoordinator) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *employeePresenceCoordinator) run() {
	for {
		work, nextAt := p.nextWork(p.options.now())
		if work != nil {
			p.write(*work)
			continue
		}

		var timer *time.Timer
		var timerC <-chan time.Time
		if !nextAt.IsZero() {
			delay := time.Until(nextAt)
			if delay < 0 {
				delay = 0
			}
			timer = time.NewTimer(delay)
			timerC = timer.C
		}
		select {
		case <-p.ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-p.wake:
			if timer != nil {
				timer.Stop()
			}
		case <-timerC:
		}
	}
}

func (p *employeePresenceCoordinator) nextWork(now time.Time) (*employeePresenceWork, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var nextAt time.Time
	for userID, entry := range p.entries {
		if !entry.pending || entry.inFlight {
			if !entry.pending && !entry.inFlight && !entry.lastSuccess.IsZero() {
				expiresAt := entry.lastSuccess.Add(p.options.stateTTL)
				if !now.Before(expiresAt) {
					delete(p.entries, userID)
				} else if nextAt.IsZero() || expiresAt.Before(nextAt) {
					nextAt = expiresAt
				}
			}
			continue
		}
		if entry.retryAt.After(now) {
			if nextAt.IsZero() || entry.retryAt.Before(nextAt) {
				nextAt = entry.retryAt
			}
			continue
		}
		var selected *ClientSession
		for session := range entry.sessions {
			if eligibleForEmployeePresence(session, now) {
				selected = session
				break
			}
			delete(entry.sessions, session)
		}
		if selected == nil {
			delete(p.entries, userID)
			continue
		}
		entry.pending = false
		entry.inFlight = true
		return &employeePresenceWork{userID: userID, session: selected}, time.Time{}
	}
	return nil, nextAt
}

func (p *employeePresenceCoordinator) write(work employeePresenceWork) {
	writeAt := p.options.now()
	if work.session == nil || work.session.EmployeeID != work.userID || !eligibleForEmployeePresence(work.session, writeAt) {
		p.mu.Lock()
		entry := p.entries[work.userID]
		if entry != nil {
			entry.inFlight = false
			entry.forceAfterFlight = false
			if hasEligiblePresenceSession(entry, writeAt) {
				entry.pending = true
				entry.retryAt = writeAt
			} else {
				delete(p.entries, work.userID)
			}
		}
		p.mu.Unlock()
		p.signal()
		return
	}

	baseCtx, cancelBase := context.WithCancel(work.session.presenceContext())
	stopCoordinatorCancel := context.AfterFunc(p.ctx, cancelBase)
	deadline := time.Now().Add(p.options.writeTimeout)
	if sessionDeadline := work.session.LifecycleDeadline; !sessionDeadline.IsZero() && sessionDeadline.Before(deadline) {
		deadline = sessionDeadline
	}
	if parentDeadline, ok := baseCtx.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	ctx, cancel := context.WithDeadline(baseCtx, deadline)
	err := p.store.TouchOnlineAt(ctx, work.userID, writeAt)
	cancel()
	stopCoordinatorCancel()
	cancelBase()
	completedAt := p.options.now()

	p.mu.Lock()
	entry := p.entries[work.userID]
	if entry == nil {
		p.mu.Unlock()
		return
	}
	entry.inFlight = false
	if err != nil {
		// A forced activation that arrived during this attempt is covered by
		// the retry; do not schedule an extra write after that retry succeeds.
		entry.forceAfterFlight = false
		if hasEligiblePresenceSession(entry, completedAt) {
			entry.retryAttempt++
			entry.pending = true
			entry.retryAt = completedAt.Add(p.retryDelay(entry.retryAttempt))
		} else {
			delete(p.entries, work.userID)
		}
		p.mu.Unlock()
		if !errorsIsContextCancellation(err) {
			slog.Warn("refresh employee online presence failed; retry scheduled", "userId", work.userID, "error", err)
		}
		p.signal()
		return
	}

	entry.lastSuccess = writeAt
	entry.retryAttempt = 0
	if entry.forceAfterFlight || (entry.lastActivity.After(writeAt) && completedAt.Sub(writeAt) >= p.options.touchInterval) {
		entry.pending = true
		entry.retryAt = completedAt
	} else {
		entry.sessions = make(map[*ClientSession]struct{})
	}
	entry.forceAfterFlight = false
	p.mu.Unlock()
	p.signal()
}

func hasEligiblePresenceSession(entry *employeePresenceEntry, now time.Time) bool {
	for session := range entry.sessions {
		if eligibleForEmployeePresence(session, now) {
			return true
		}
		delete(entry.sessions, session)
	}
	return false
}

func (p *employeePresenceCoordinator) retryDelay(attempt int) time.Duration {
	delay := p.options.retryBase
	for i := 1; i < attempt && delay < p.options.retryMax; i++ {
		delay *= 2
	}
	if delay > p.options.retryMax {
		return p.options.retryMax
	}
	return delay
}

func errorsIsContextCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
