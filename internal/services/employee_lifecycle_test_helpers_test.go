package services

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"

	"github.com/gin-gonic/gin"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

// Isolated fixtures retain the production timer and close path. Only the auth
// read is synthetic; each handshake must explicitly bind its trusted snapshot.
type employeeWsTestLifecycle struct {
	*employeeRealtimeLifecycle
	mu          sync.Mutex
	snapshots   map[int64]EmployeeSessionSnapshot
	beforeRead  func(int64)
	readCurrent func(int64, time.Time) (EmployeeSessionSnapshot, error)
}

func newWsServiceForTest() *wsService {
	manager := newWsConnectionManager()
	lifecycle := &employeeWsTestLifecycle{employeeRealtimeLifecycle: newEmployeeRealtimeLifecycle(manager, employeeAuthStateReader{}), snapshots: make(map[int64]EmployeeSessionSnapshot)}
	return newWsService(manager, lifecycle)
}

func (l *employeeWsTestLifecycle) RevalidateEmployeeSession(id int64, now time.Time) (EmployeeSessionSnapshot, error) {
	if l.beforeRead != nil {
		l.beforeRead(id)
	}
	if l.readCurrent != nil {
		return l.readCurrent(id, now)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	snapshot, ok := l.snapshots[id]
	if !ok {
		return EmployeeSessionSnapshot{}, fmt.Errorf("unbound synthetic employee session")
	}
	return snapshot, nil
}

func (l *employeeWsTestLifecycle) IsEmployeeLoginSessionActive(employeeID, loginSessionID int64, now time.Time) (bool, error) {
	snapshot, err := l.RevalidateEmployeeSession(loginSessionID, now)
	if err != nil {
		return false, err
	}
	return snapshot.EmployeeID == employeeID && snapshot.LoginSessionID == loginSessionID &&
		employeeLifecycleDeadline(snapshot).After(now), nil
}

func bindEmployeeWsTestSession(t *testing.T, svc *wsService, ctx *gin.Context, principal *dto.AuthPrincipal) {
	t.Helper()
	lifecycle, ok := svc.lifecycle.(*employeeWsTestLifecycle)
	if !ok || principal == nil || principal.UserID <= 0 {
		t.Fatal("synthetic binding requires test lifecycle and positive principal")
	}
	snapshot := EmployeeSessionSnapshot{EmployeeID: principal.UserID, LoginSessionID: principal.UserID + 1000000, LoginSessionExpiresAt: time.Now().Add(time.Hour), Principal: principal}
	lifecycle.mu.Lock()
	lifecycle.snapshots[snapshot.LoginSessionID] = snapshot
	lifecycle.mu.Unlock()
	ctx.Set(authPrincipalContextKey, principal)
	AuthService.setAuthenticatedEmployeeSession(ctx, authenticatedEmployeeSession{EmployeeID: snapshot.EmployeeID, LoginSessionID: snapshot.LoginSessionID, LoginSessionExpiresAt: snapshot.LoginSessionExpiresAt, Principal: principal})
}

func markActiveEmployeeTestSession(session *ClientSession) {
	if !isEmployeeRealtimeSession(session) {
		return
	}
	if session.Principal != nil {
		session.EmployeeID = session.Principal.UserID
	}
	session.LoginSessionID = session.EmployeeID + 1000000
	session.LoginSessionExpiresAt = time.Now().Add(time.Hour)
	session.LifecycleDeadline = session.LoginSessionExpiresAt
	session.employeePhase.Store(uint32(employeePhaseActive))
}

func lifecycleAuthTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := sqls.DB()
	db := setupAuthServiceTestDB(t)
	t.Cleanup(func() {
		sqls.SetDB(previousDB)
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// Mutation observers inspect committed state before delegating to real closes.
type employeeMutationInvalidator struct {
	*employeeRealtimeLifecycle
	onLogin     func(int64)
	onEmployees func([]int64)
}

func (l *employeeMutationInvalidator) InvalidateLoginSession(id int64) int {
	if l.onLogin != nil {
		l.onLogin(id)
	}
	return l.employeeRealtimeLifecycle.InvalidateLoginSession(id)
}

func (l *employeeMutationInvalidator) InvalidateEmployees(ids []int64) int {
	if l.onEmployees != nil {
		l.onEmployees(ids)
	}
	return l.employeeRealtimeLifecycle.InvalidateEmployees(ids)
}

func employeeMutationFixture(t *testing.T) (*gorm.DB, *employeeMutationInvalidator, *models.User, *dto.AuthPrincipal) {
	t.Helper()
	db := lifecycleAuthTestDB(t)
	user := createAuthTestUser(t, db, "mutation-employee", "old-password")
	if err := db.Model(user).Update("user_type", enums.UserTypeEmployee).Error; err != nil {
		t.Fatal(err)
	}
	lifecycle := &employeeMutationInvalidator{employeeRealtimeLifecycle: newEmployeeRealtimeLifecycle(newWsConnectionManager(), employeeAuthStateReader{})}
	previous := LoginSessionService
	LoginSessionService = newLoginSessionService(lifecycle)
	t.Cleanup(func() { LoginSessionService = previous })
	return db, lifecycle, user, &dto.AuthPrincipal{UserID: user.ID, Username: user.Username}
}

func employeeMutationSockets(t *testing.T, l *employeeMutationInvalidator, userID, loginID int64, prefix string) (*ClientSession, *ClientSession) {
	t.Helper()
	return lifecycleTestSession(t, l.manager, prefix+"-pending", userID, loginID, false),
		lifecycleTestSession(t, l.manager, prefix+"-active", userID, loginID, true)
}

func employeeMutationLogin(t *testing.T, db *gorm.DB, userID int64, token string) models.LoginSession {
	t.Helper()
	now := time.Now()
	row := models.LoginSession{UserID: userID, Token: token, ClientType: "admin_web", ExpiredAt: now.Add(time.Hour), AuditFields: models.AuditFields{CreatedAt: now, UpdatedAt: now}}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func employeeMutationReadLogin(t *testing.T, db *gorm.DB, id int64) models.LoginSession {
	t.Helper()
	var row models.LoginSession
	if err := db.First(&row, id).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func employeeMutationFailUpdate(t *testing.T, db *gorm.DB, table string, failure error) {
	t.Helper()
	const name = "test:employee_mutation_update_failure"
	if err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == table {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Update().Remove(name) })
}
