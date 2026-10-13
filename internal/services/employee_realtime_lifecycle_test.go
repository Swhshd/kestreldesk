package services

import (
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
)

func TestEmployeeTimerCloseClearsReference(t *testing.T) {
	manager := newWsConnectionManager()
	session := &ClientSession{Send: make(chan []byte, 1)}
	timer := time.AfterFunc(time.Hour, func() {})
	t.Cleanup(func() { timer.Stop() })
	session.lifecycleTimerMu.Lock()
	session.lifecycleTimer = timer
	session.lifecycleTimerMu.Unlock()
	manager.CloseSession(session)
	session.lifecycleTimerMu.Lock()
	defer session.lifecycleTimerMu.Unlock()
	if session.lifecycleTimer != nil || timer.Stop() {
		t.Fatal("terminal close did not detach/stop installed timer")
	}
}

func TestEmployeeRealtimeLifecycleDetectsSharedLoginSessionRevocation(t *testing.T) {
	db, lifecycle, user, _ := employeeMutationFixture(t)
	now := time.Now().UTC()
	row := models.LoginSession{
		UserID:     user.ID,
		Token:      "synthetic-revocation-check",
		ClientType: "admin_web",
		ExpiredAt:  now.Add(time.Hour),
		AuditFields: models.AuditFields{
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if active, err := lifecycle.IsEmployeeLoginSessionActive(user.ID, row.ID, now); err != nil || !active {
		t.Fatalf("active synthetic session check = %t, %v", active, err)
	}
	if active, err := lifecycle.IsEmployeeLoginSessionActive(user.ID+1, row.ID, now); err != nil || active {
		t.Fatalf("wrong employee session check = %t, %v", active, err)
	}
	revokedAt := now.Add(time.Second)
	if err := db.Model(&models.LoginSession{}).Where("id = ?", row.ID).Update("revoked_at", revokedAt).Error; err != nil {
		t.Fatal(err)
	}
	if active, err := lifecycle.IsEmployeeLoginSessionActive(user.ID, row.ID, revokedAt); err != nil || active {
		t.Fatalf("revoked synthetic session check = %t, %v", active, err)
	}
}

func TestEmployeeLifecycleWiringValidation(t *testing.T) {
	oldManager, oldLifecycle, oldWS := employeeWSManager, employeeRealtime, WsService
	oldLogin, oldUser, oldRole, oldPermission := LoginSessionService, UserService, RoleService, PermissionService
	oldOIDC, oldUserPermission := OIDCLoginService, UserPermissionService
	t.Cleanup(func() {
		employeeWSManager, employeeRealtime, WsService = oldManager, oldLifecycle, oldWS
		LoginSessionService, UserService, RoleService, PermissionService = oldLogin, oldUser, oldRole, oldPermission
		OIDCLoginService, UserPermissionService = oldOIDC, oldUserPermission
	})
	manager := newWsConnectionManager()
	lifecycle := newEmployeeRealtimeLifecycle(manager, employeeAuthStateReader{})
	spy := &employeeRealtimeInvalidationSpy{}
	employeeWSManager, employeeRealtime = manager, lifecycle
	WsService = newWsService(manager, lifecycle)
	LoginSessionService = newLoginSessionService(lifecycle)
	UserService = newUserService(lifecycle)
	RoleService = newRoleService(lifecycle)
	PermissionService = newPermissionService(lifecycle)
	OIDCLoginService = newOIDCLoginService(lifecycle)
	UserPermissionService = newUserPermissionService(lifecycle)
	if err := ValidateEmployeeRealtimeLifecycleWiring(); err != nil {
		t.Fatalf("complete lifecycle wiring rejected: %v", err)
	}
	cases := []struct {
		name        string
		breakWiring func()
	}{
		{"dashboard websocket service missing", func() { WsService = nil }},
		{"dashboard websocket manager mismatch", func() { WsService.manager = newWsConnectionManager() }},
		{"dashboard lifecycle mismatch", func() {
			WsService.lifecycle = newEmployeeRealtimeLifecycle(newWsConnectionManager(), employeeAuthStateReader{})
		}},
		{"login-session invalidator missing", func() { LoginSessionService.invalidator = nil }},
		{"login-session invalidator mismatch", func() { LoginSessionService.invalidator = spy }},
		{"user invalidator missing", func() { UserService.invalidator = nil }},
		{"user invalidator mismatch", func() { UserService.invalidator = spy }},
		{"role invalidator missing", func() { RoleService.invalidator = nil }},
		{"role invalidator mismatch", func() { RoleService.invalidator = spy }},
		{"permission invalidator missing", func() { PermissionService.invalidator = nil }},
		{"permission invalidator mismatch", func() { PermissionService.invalidator = spy }},
		{"OIDC invalidator missing", func() { OIDCLoginService.invalidator = nil }},
		{"OIDC invalidator mismatch", func() { OIDCLoginService.invalidator = spy }},
		{"user-permission invalidator missing", func() { UserPermissionService.invalidator = nil }},
		{"user-permission invalidator mismatch", func() { UserPermissionService.invalidator = spy }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Restore the coherent graph between cases without using production globals as fallbacks.
			WsService = newWsService(manager, lifecycle)
			LoginSessionService, UserService = newLoginSessionService(lifecycle), newUserService(lifecycle)
			RoleService, PermissionService = newRoleService(lifecycle), newPermissionService(lifecycle)
			OIDCLoginService, UserPermissionService = newOIDCLoginService(lifecycle), newUserPermissionService(lifecycle)
			tc.breakWiring()
			if err := ValidateEmployeeRealtimeLifecycleWiring(); err == nil {
				t.Fatal("invalid lifecycle wiring accepted")
			}
		})
	}
}

// Each test catches lost timer ownership or an unauthorized lifecycle transition.
func lifecycleTestSession(t *testing.T, m *WsConnectionManager, id string, employeeID, loginID int64, active bool) *ClientSession {
	t.Helper()
	s := &ClientSession{ID: id, Role: realtimeRoleAdmin, EmployeeID: employeeID, LoginSessionID: loginID, Principal: &dto.AuthPrincipal{UserID: employeeID}, Topics: make(map[string]struct{}), Send: make(chan []byte, 1)}
	if !m.RegisterPending(s) {
		t.Fatal("pending registration failed")
	}
	t.Cleanup(func() { m.CloseSession(s) })
	if active {
		if _, ok := m.Activate(s, lifecycleTestSnapshot(employeeID, loginID), []string{"admin:test"}); !ok {
			t.Fatal("activation failed")
		}
	}
	return s
}

func lifecycleTestSnapshot(employeeID, loginID int64) EmployeeSessionSnapshot {
	return EmployeeSessionSnapshot{EmployeeID: employeeID, LoginSessionID: loginID, LoginSessionExpiresAt: time.Now().Add(time.Hour), Principal: &dto.AuthPrincipal{UserID: employeeID, Roles: []string{"synthetic"}, Permissions: []string{"synthetic.permission"}}}
}

func lifecycleTimerReference(s *ClientSession) *time.Timer {
	s.lifecycleTimerMu.Lock()
	defer s.lifecycleTimerMu.Unlock()
	return s.lifecycleTimer
}

func lifecycleAwait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("lifecycle barrier timed out")
	}
}

// Barriers are released during cleanup even when an earlier assertion fails.
func lifecycleTestRelease(t *testing.T) (chan struct{}, func()) {
	t.Helper()
	barrier := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(barrier) }) }
	t.Cleanup(release)
	return barrier, release
}

func TestEmployeeTimerPublicationLosesToClose(t *testing.T) {
	m := newWsConnectionManager()
	l := newEmployeeRealtimeLifecycle(m, employeeAuthStateReader{})
	s := lifecycleTestSession(t, m, "pending", 1, 11, false)
	before := make(chan struct{})
	resume, release := lifecycleTestRelease(t)
	done := make(chan bool, 1)
	go func() {
		done <- l.installLifecycleTimerWithBeforePublish(s, time.Now().Add(time.Hour), func() { close(before); <-resume })
	}()
	lifecycleAwait(t, before)
	if l.InvalidateEmployees([]int64{1}) != 1 {
		t.Fatal("pending revoke missed session")
	}
	release()
	var installed bool
	select {
	case installed = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publication stuck")
	}
	if installed || lifecycleTimerReference(s) != nil || !s.Closed.Load() {
		t.Fatal("closed session retained timer")
	}
	if _, ok := m.Activate(s, lifecycleTestSnapshot(1, 11), []string{"admin:1"}); ok {
		t.Fatal("closed pending activated")
	}
}

func TestEmployeeTimerPublicationWins(t *testing.T) {
	m := newWsConnectionManager()
	l := newEmployeeRealtimeLifecycle(m, employeeAuthStateReader{})
	s := lifecycleTestSession(t, m, "active", 1, 11, true)
	if !l.InstallLifecycleTimer(s, time.Now().Add(time.Hour)) {
		t.Fatal("timer not installed")
	}
	timer := lifecycleTimerReference(s)
	wins := 0
	if m.CloseSession(s) {
		wins++
	}
	if m.CloseSession(s) {
		wins++
	}
	if lifecycleTimerReference(s) != nil || timer.Stop() || wins != 1 {
		t.Fatal("timer publication winner did not clean up once")
	}
}

func TestEmployeeTimerCallbackRevokeOverlap(t *testing.T) {
	m := newWsConnectionManager()
	l := newEmployeeRealtimeLifecycle(m, employeeAuthStateReader{})
	s := lifecycleTestSession(t, m, "overlap", 1, 11, true)
	started := make(chan struct{})
	resume, release := lifecycleTestRelease(t)
	done := make(chan struct{})
	var wins atomic.Int32
	l.afterFunc = func(_ time.Duration, cb func()) *time.Timer {
		return time.AfterFunc(0, func() {
			close(started)
			<-resume
			cb()
			if m.CloseSession(s) {
				wins.Add(1)
			}
			close(done)
		})
	}
	if !l.InstallLifecycleTimer(s, time.Now().Add(time.Hour)) {
		t.Fatal("install failed")
	}
	lifecycleAwait(t, started)
	if l.InvalidateEmployees([]int64{1}) == 1 {
		wins.Add(1)
	}
	release()
	lifecycleAwait(t, done)
	if wins.Load() != 1 || !s.Closed.Load() {
		t.Fatal("callback/revoke did not close once")
	}
	select {
	case _, ok := <-s.Send:
		if ok {
			t.Fatal("send channel remains open")
		}
	default:
		t.Fatal("send channel remains open")
	}
}

func TestEmployeeTimerStopFalseDoesNotWait(t *testing.T) {
	m := newWsConnectionManager()
	l := newEmployeeRealtimeLifecycle(m, employeeAuthStateReader{})
	s := lifecycleTestSession(t, m, "running", 1, 11, true)
	started := make(chan struct{})
	resume, release := lifecycleTestRelease(t)
	callbackDone := make(chan struct{})
	closeDone := make(chan struct{})
	l.afterFunc = func(_ time.Duration, cb func()) *time.Timer {
		return time.AfterFunc(0, func() { close(started); <-resume; cb(); close(callbackDone) })
	}
	if !l.InstallLifecycleTimer(s, time.Now().Add(time.Hour)) {
		t.Fatal("install failed")
	}
	lifecycleAwait(t, started)
	go func() { m.CloseSession(s); close(closeDone) }()
	lifecycleAwait(t, closeDone)
	if lifecycleTimerReference(s) != nil {
		t.Fatal("running callback timer retained")
	}
	release()
	lifecycleAwait(t, callbackDone)
}

func TestEmployeeTimerPastDeadline(t *testing.T) {
	for _, offset := range []time.Duration{0, -time.Second} {
		t.Run(offset.String(), func(t *testing.T) {
			m := newWsConnectionManager()
			l := newEmployeeRealtimeLifecycle(m, employeeAuthStateReader{})
			s := lifecycleTestSession(t, m, "past", 1, 11, false)
			now := time.Now()
			l.now = func() time.Time { return now }
			installed := l.InstallLifecycleTimer(s, now.Add(offset))
			if installed || lifecycleTimerReference(s) != nil {
				t.Fatal("past deadline retained timer")
			}
			if !s.Closed.Load() {
				t.Fatal("past deadline remains open")
			}
		})
	}
}

func TestEmployeeTimerSingleInstallation(t *testing.T) {
	m := newWsConnectionManager()
	l := newEmployeeRealtimeLifecycle(m, employeeAuthStateReader{})
	s := lifecycleTestSession(t, m, "single", 1, 11, false)
	var timers []*time.Timer
	l.afterFunc = func(d time.Duration, cb func()) *time.Timer {
		timer := time.AfterFunc(d, cb)
		timers = append(timers, timer)
		t.Cleanup(func() { timer.Stop() })
		return timer
	}
	if !l.InstallLifecycleTimer(s, time.Now().Add(time.Hour)) {
		t.Fatal("first install failed")
	}
	firstTimer := lifecycleTimerReference(s)
	secondInstalled := l.InstallLifecycleTimer(s, time.Now().Add(time.Hour))
	installedPointer := lifecycleTimerReference(s)
	if secondInstalled || installedPointer != firstTimer {
		t.Fatal("multiple lifecycle timers retained")
	}
	if len(timers) != 2 || timers[1].Stop() {
		t.Fatal("rejected local timer not stopped")
	}
}

func TestEmployeeLifecycleActivationTerminalTruth(t *testing.T) {
	for _, mode := range []string{"pending", "stale", "unregistered", "closed", "new", "active", "wrong employee", "wrong login", "wrong principal", "past deadline"} {
		t.Run(mode, func(t *testing.T) {
			m := newWsConnectionManager()
			s := lifecycleTestSession(t, m, "candidate", 1, 11, false)
			snapshot := lifecycleTestSnapshot(1, 11)
			switch mode {
			case "stale":
				m.mu.Lock()
				m.sessions[s.ID] = &ClientSession{ID: s.ID}
				m.mu.Unlock()
			case "unregistered":
				m.Unregister(s)
			case "closed":
				s.Closed.Store(true)
			case "new":
				s.employeePhase.Store(uint32(employeePhaseNew))
			case "active":
				s.employeePhase.Store(uint32(employeePhaseActive))
			case "wrong employee":
				snapshot.EmployeeID = 2
			case "wrong login":
				snapshot.LoginSessionID = 12
			case "wrong principal":
				snapshot.Principal.UserID = 2
			case "past deadline":
				snapshot.LoginSessionExpiresAt = time.Now().Add(-time.Second)
			}
			topics, ok := m.Activate(s, snapshot, []string{"admin:1"})
			if ok != (mode == "pending") {
				t.Fatalf("Activate = %v", ok)
			}
			if !ok {
				if len(s.Topics) != 0 || len(topics) != 0 {
					t.Fatal("failed activation changed topics")
				}
				return
			}
			if !slices.Equal(topics, []string{"admin:1"}) || employeeLifecyclePhase(s.employeePhase.Load()) != employeePhaseActive || !s.LifecycleDeadline.Equal(snapshot.LoginSessionExpiresAt) {
				t.Fatal("activation did not publish snapshot and topics")
			}
			topics[0] = "mutated"
			snapshot.Principal.Permissions[0] = "mutated"
			snapshot.Principal.UserID = 9
			if _, ok := s.Topics["admin:1"]; !ok || s.Principal.UserID != 1 || s.Principal.Permissions[0] != "synthetic.permission" {
				t.Fatal("activation retained mutable input")
			}
		})
	}
}

func TestEmployeeLifecycleScanIsolation(t *testing.T) {
	m := newWsConnectionManager()
	l := newEmployeeRealtimeLifecycle(m, employeeAuthStateReader{})
	a1 := lifecycleTestSession(t, m, "a1", 1, 11, false)
	a2 := lifecycleTestSession(t, m, "a2", 1, 11, true)
	b1 := lifecycleTestSession(t, m, "b1", 1, 12, false)
	b2 := lifecycleTestSession(t, m, "b2", 1, 12, true)
	other := lifecycleTestSession(t, m, "other", 2, 21, true)
	customer := &ClientSession{ID: "customer", Role: realtimeRoleUser, CustomerID: 1, External: &openidentity.ExternalUser{ExternalID: "synthetic"}, Topics: make(map[string]struct{}), Send: make(chan []byte, 1)}
	m.Register(customer, nil)
	t.Cleanup(func() { m.CloseSession(customer) })
	guest := &ClientSession{ID: "guest", Role: realtimeRoleUser, Topics: make(map[string]struct{}), Send: make(chan []byte, 1)}
	m.Register(guest, nil)
	t.Cleanup(func() { m.CloseSession(guest) })
	if got := l.InvalidateLoginSession(11); got != 2 {
		t.Fatalf("login close count=%d", got)
	}
	if !a1.Closed.Load() || !a2.Closed.Load() || b1.Closed.Load() || b2.Closed.Load() {
		t.Fatal("login isolation failed")
	}
	if got := l.InvalidateEmployees([]int64{1}); got != 2 {
		t.Fatalf("employee close count=%d", got)
	}
	if !b1.Closed.Load() || !b2.Closed.Load() || other.Closed.Load() || customer.Closed.Load() || guest.Closed.Load() {
		t.Fatal("employee isolation failed")
	}
}

func TestEmployeeLifecycleEmptyAndDuplicateTargets(t *testing.T) {
	m := newWsConnectionManager()
	l := newEmployeeRealtimeLifecycle(m, employeeAuthStateReader{})
	s := lifecycleTestSession(t, m, "target", 1, 11, false)
	for _, ids := range [][]int64{nil, {}, {0, -1}} {
		if l.InvalidateEmployees(ids) != 0 || s.Closed.Load() {
			t.Fatal("empty/invalid employee targets mean all")
		}
	}
	for _, id := range []int64{0, -1, 99} {
		if l.InvalidateLoginSession(id) != 0 || s.Closed.Load() {
			t.Fatal("invalid login targets mean all")
		}
	}
	if l.InvalidateEmployees([]int64{1, 1, 0, -1}) != 1 || l.InvalidateEmployees([]int64{1}) != 0 || l.InvalidateLoginSession(11) != 0 {
		t.Fatal("duplicate or closed targets inflate close count")
	}
}

func TestEmployeeLifecycleManagerLockReleased(t *testing.T) {
	m := newWsConnectionManager()
	l := newEmployeeRealtimeLifecycle(m, employeeAuthStateReader{})
	s := lifecycleTestSession(t, m, "blocked", 1, 11, false)
	s.sendMu.Lock()
	locked := true
	defer func() {
		if locked {
			s.sendMu.Unlock()
		}
	}()
	closeDone := make(chan struct{})
	go func() { l.InvalidateEmployees([]int64{1}); close(closeDone) }()
	// Confirm the invalidator reached the terminal close while sendMu remains held.
	waitWsGoroutineStacks(t, "invalidator blocked on sendMu", func(stacks map[string]string) bool {
		for _, stack := range stacks {
			if strings.Contains(stack, "(*WsConnectionManager).CloseSession.func1") && strings.Contains(stack, "(*employeeRealtimeLifecycle).InvalidateEmployees") {
				return true
			}
		}
		return false
	})
	lockDone := make(chan struct{})
	go func() { m.Count(); close(lockDone) }()
	lifecycleAwait(t, lockDone)
	s.sendMu.Unlock()
	locked = false
	lifecycleAwait(t, closeDone)
}

func TestEmployeeLifecycleAllEmployeeFallback(t *testing.T) {
	db := lifecycleAuthTestDB(t)
	row := models.LoginSession{UserID: 1}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	m := newWsConnectionManager()
	l := newEmployeeRealtimeLifecycle(m, employeeAuthStateReader{})
	a := lifecycleTestSession(t, m, "pending", 1, 11, false)
	b := lifecycleTestSession(t, m, "active", 2, 21, true)
	customer := &ClientSession{ID: "customer", Role: realtimeRoleUser, CustomerID: 1, Topics: make(map[string]struct{}), Send: make(chan []byte, 1)}
	guest := &ClientSession{ID: "guest", Role: realtimeRoleUser, Topics: make(map[string]struct{}), Send: make(chan []byte, 1)}
	for _, s := range []*ClientSession{customer, guest} {
		m.Register(s, nil)
		t.Cleanup(func() { m.CloseSession(s) })
	}
	if l.InvalidateAllEmployees() != 2 || !a.Closed.Load() || !b.Closed.Load() || customer.Closed.Load() || guest.Closed.Load() {
		t.Fatal("fallback selection failed")
	}
	var got models.LoginSession
	if err := db.First(&got, row.ID).Error; err != nil || got.UserID != row.UserID || got.RevokedAt != row.RevokedAt {
		t.Fatal("socket fallback modified login row")
	}
}

func TestEmployeeTimerDeadlineReachedBeforePublication(t *testing.T) {
	m := newWsConnectionManager()
	l := newEmployeeRealtimeLifecycle(m, employeeAuthStateReader{})
	s := lifecycleTestSession(t, m, "elapsed", 1, 11, false)
	now := time.Now()
	deadline := now.Add(time.Hour)
	l.now = func() time.Time { return now }
	var local *time.Timer
	l.afterFunc = func(d time.Duration, cb func()) *time.Timer {
		local = time.AfterFunc(d, cb)
		t.Cleanup(func() { local.Stop() })
		return local
	}
	installed := l.installLifecycleTimerWithBeforePublish(s, deadline, func() { now = deadline })
	if installed || lifecycleTimerReference(s) != nil || !s.Closed.Load() || local.Stop() {
		t.Fatal("deadline reached before publication retained timer/session")
	}
}

func TestEmployeeTimerCallbackClosesInstalledSession(t *testing.T) {
	m := newWsConnectionManager()
	l := newEmployeeRealtimeLifecycle(m, employeeAuthStateReader{})
	s := lifecycleTestSession(t, m, "callback-winner", 1, 11, true)
	resume, release := lifecycleTestRelease(t)
	done := make(chan struct{})
	l.afterFunc = func(_ time.Duration, cb func()) *time.Timer {
		return time.AfterFunc(0, func() { <-resume; cb(); close(done) })
	}
	if !l.InstallLifecycleTimer(s, time.Now().Add(time.Hour)) {
		t.Fatal("install failed")
	}
	release()
	lifecycleAwait(t, done)
	if !s.Closed.Load() || lifecycleTimerReference(s) != nil || m.Count() != 0 || m.CloseSession(s) {
		t.Fatal("expiry callback did not use terminal close")
	}
	select {
	case _, ok := <-s.Send:
		if ok {
			t.Fatal("callback left send open")
		}
	default:
		t.Fatal("callback left send open")
	}
}

func TestEmployeeLifecyclePendingRegistration(t *testing.T) {
	for _, mode := range []string{"new", "pending", "active", "closed", "duplicate", "customer", "guest", "invalid employee", "invalid login"} {
		t.Run(mode, func(t *testing.T) {
			m := newWsConnectionManager()
			s := &ClientSession{ID: "candidate", Role: realtimeRoleAdmin, EmployeeID: 1, LoginSessionID: 11, Principal: &dto.AuthPrincipal{UserID: 1}, Topics: make(map[string]struct{}), Send: make(chan []byte, 1)}
			switch mode {
			case "pending":
				s.employeePhase.Store(uint32(employeePhasePending))
			case "active":
				s.employeePhase.Store(uint32(employeePhaseActive))
			case "closed":
				m.CloseSession(s)
			case "duplicate":
				m.Register(&ClientSession{ID: s.ID, Topics: make(map[string]struct{})}, nil)
			case "customer":
				s.Role = realtimeRoleUser
				s.CustomerID = 1
			case "guest":
				s.Role = realtimeRoleUser
				s.Principal = nil
			case "invalid employee":
				s.EmployeeID = 0
			case "invalid login":
				s.LoginSessionID = 0
			}
			if got := m.RegisterPending(s); got != (mode == "new") {
				t.Fatalf("RegisterPending=%v", got)
			}
			if mode == "new" {
				if m.Count() != 1 || len(s.Topics) != 0 || len(m.FindByTopics([]string{"admin:1"})) != 0 {
					t.Fatal("pending acquired topic membership")
				}
				if len(m.Subscribe(s, []string{"admin:1"})) != 0 || m.HasTopic("admin:1") {
					t.Fatal("pending subscription acquired topics")
				}
				if m.RegisterPending(s) {
					t.Fatal("pending registered twice")
				}
			}
			m.CloseSession(s)
		})
	}
}

func TestEmployeeLifecycleActivateCloseOverlap(t *testing.T) {
	for range 100 {
		m := newWsConnectionManager()
		s := lifecycleTestSession(t, m, "activation-close", 1, 11, false)
		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		go func() { defer group.Done(); <-start; m.Activate(s, lifecycleTestSnapshot(1, 11), []string{"admin:1"}) }()
		go func() { defer group.Done(); <-start; m.CloseSession(s) }()
		close(start)
		group.Wait()
		if !s.Closed.Load() || m.Count() != 0 || m.HasTopic("admin:1") {
			t.Fatal("activation resurrected terminal session")
		}
	}
}

func TestEmployeeLifecycleDeadlineAndRevalidation(t *testing.T) {
	now := time.Now()
	expiry := now.Add(time.Hour)
	earlier := now.Add(time.Minute)
	later := now.Add(2 * time.Hour)
	for _, tc := range []struct {
		name     string
		override *time.Time
		want     time.Time
	}{{"login only", nil, expiry}, {"earlier override", &earlier, earlier}, {"later override", &later, expiry}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := employeeLifecycleDeadline(EmployeeSessionSnapshot{LoginSessionExpiresAt: expiry, NextAuthzChangeAt: tc.override}); !got.Equal(tc.want) {
				t.Fatalf("deadline=%v want=%v", got, tc.want)
			}
		})
	}
	db := lifecycleAuthTestDB(t)
	grant := createRBACTestGrant(t, db, "conversation.view", enums.StatusOk, enums.StatusOk, true, nil, nil)
	row := seedEmployeeSnapshotSession(t, db, grant.UserID, now)
	var before models.LoginSession
	if err := db.First(&before, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	l := newEmployeeRealtimeLifecycle(newWsConnectionManager(), employeeAuthStateReader{})
	got, err := l.RevalidateEmployeeSession(row.ID, now)
	if err != nil || got.LoginSessionID != row.ID || got.EmployeeID != grant.UserID || !got.LoginSessionExpiresAt.Equal(row.ExpiredAt) {
		t.Fatal("revalidation did not use current employee session")
	}
	if _, err := l.RevalidateEmployeeSession(row.ID, row.ExpiredAt); err == nil {
		t.Fatal("revalidation ignored supplied expiry time")
	}
	var after models.LoginSession
	if err := db.First(&after, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("revalidation changed login state")
	}
}
