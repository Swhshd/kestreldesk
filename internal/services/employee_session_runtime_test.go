// This file has been modified for the KestrelDesk project.
package services

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

const employeeSessionRuntimeScenarioEnv = "KESTRELDESK_EMPLOYEE_SESSION_RUNTIME_SCENARIO"

// TestEmployeeSessionRuntimeAcceptance is an in-process synthetic employee
// session/WebSocket lifecycle acceptance. Each scenario gets a fresh test
// process so package globals and the in-memory database cannot cross-contaminate
// the next scenario. This is not production or browser E2E evidence.
func TestEmployeeSessionRuntimeAcceptance(t *testing.T) {
	if scenario := os.Getenv(employeeSessionRuntimeScenarioEnv); scenario != "" {
		runEmployeeSessionRuntimeScenario(t, scenario)
		return
	}

	for _, scenario := range []string{
		"exact_session_revoke", "revoke_all", "password_change", "disable_before_revoke_failure",
		"assign_roles_commit_and_rollback", "role_status_current_members", "role_permission_replacement",
		"builtin_permission_sync", "oidc_default_role_grant", "direct_user_permission_crud",
		"committed_impact_failure_customer_isolation", "pending_revoke_before_registration",
		"pending_revoke_after_registration", "timer_publication_and_callback_revoke",
		"idle_login_session_expiry", "idle_direct_allow_expiry", "idle_direct_deny_expiry",
	} {
		t.Run(scenario, func(t *testing.T) {
			binary, err := os.Executable()
			if err != nil {
				t.Fatal("locate test binary")
			}
			cmd := exec.Command(binary, "-test.run=^TestEmployeeSessionRuntimeAcceptance$", "-test.count=1")
			cmd.Env = append(os.Environ(), employeeSessionRuntimeScenarioEnv+"="+scenario)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("synthetic scenario %q failed (exit=%v): %s", scenario, err, safeRuntimeOutput(output))
			}
		})
	}
}

func safeRuntimeOutput(output []byte) string {
	// Child output is limited to test diagnostics. Never include authorization
	// headers, synthetic bearer values, or serialized session state in failures.
	return strings.TrimSpace(string(output))
}

func runEmployeeSessionRuntimeScenario(t *testing.T, scenario string) {
	gin.SetMode(gin.TestMode)
	db := setupAuthServiceTestDB(t)
	previousLoginSessions, previousWS := LoginSessionService, WsService
	manager := newWsConnectionManager()
	lifecycle := newEmployeeRealtimeLifecycle(manager, employeeAuthStateReader{})
	LoginSessionService = newLoginSessionService(lifecycle)
	WsService = newWsService(manager, lifecycle)
	t.Cleanup(func() {
		lifecycle.InvalidateAllEmployees()
		LoginSessionService, WsService = previousLoginSessions, previousWS
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	alpha := runtimeEmployee(t, db, "runtime-alpha", "runtime-token-alpha")
	beta := runtimeEmployee(t, db, "runtime-beta", "runtime-token-beta")
	alphaA := runtimeSocket(t, alpha.token)
	alphaB := runtimeSocket(t, alpha.token)
	betaA := runtimeSocket(t, beta.token)
	betaB := runtimeSocket(t, beta.token)
	for _, conn := range []*websocket.Conn{alphaA, alphaB, betaA, betaB} {
		readRuntimeEvent(t, conn, enums.IMRealtimeEventConnected)
	}

	switch scenario {
	case "exact_session_revoke":
		if err := LoginSessionService.Revoke(alpha.login.ID, alpha.user.ID, alpha.user.Username); err != nil {
			t.Fatal("revoke synthetic session")
		}
		assertRuntimeSocketClosed(t, alphaA)
		assertRuntimeSocketClosed(t, alphaB)
		assertRuntimePingPong(t, betaA)
		assertRuntimePingPong(t, betaB)
		if got := authenticateRuntime(alpha.token); got != http.StatusUnauthorized {
			t.Fatalf("fresh authentication after exact revoke = %d", got)
		}
	case "revoke_all":
		if err := LoginSessionService.RevokeByUser(alpha.user.ID, beta.user.ID, beta.user.Username); err != nil {
			t.Fatal("revoke all synthetic employee sessions")
		}
		assertRuntimeSocketClosed(t, alphaA)
		assertRuntimeSocketClosed(t, alphaB)
		assertRuntimePingPong(t, betaA)
		assertRuntimePingPong(t, betaB)
	case "password_change":
		if err := newUserService(lifecycle).ChangeOwnPassword("synthetic-new-password", &dto.AuthPrincipal{UserID: alpha.user.ID, Username: alpha.user.Username}); err != nil {
			t.Fatal("change synthetic employee password")
		}
		assertRuntimeSocketClosed(t, alphaA)
		assertRuntimeSocketClosed(t, alphaB)
		assertRuntimePingPong(t, betaA)
		if got := authenticateRuntime(alpha.token); got != http.StatusUnauthorized {
			t.Fatalf("fresh authentication after password change = %d", got)
		}
	case "disable_before_revoke_failure":
		failure := registerRuntimeLoginSessionUpdateFailure(t, db)
		_ = failure
		// User status is persisted and sockets are closed before the later
		// revoke-all write fails. The failure is deliberately synthetic.
		service := newUserService(lifecycle)
		err := service.UpdateStatus(alpha.user.ID, int(enums.StatusDisabled), &dto.AuthPrincipal{UserID: beta.user.ID, Username: beta.user.Username})
		if err == nil {
			t.Fatal("expected later LoginSession revoke write to fail")
		}
		var persisted models.User
		if err := db.First(&persisted, alpha.user.ID).Error; err != nil || persisted.Status != enums.StatusDisabled {
			t.Fatal("employee disable did not persist before revoke failure")
		}
		assertRuntimeSocketClosed(t, alphaA)
		assertRuntimeSocketClosed(t, alphaB)
		assertRuntimePingPong(t, betaA)
		if got := authenticateRuntime(alpha.token); got != http.StatusUnauthorized {
			t.Fatalf("fresh authentication after disable = %d", got)
		}
		assertRuntimeWebSocketRejected(t, alpha.token)
	case "assign_roles_commit_and_rollback":
		role := models.Role{Code: "runtime-role", Name: "Runtime role", Status: enums.StatusOk}
		if err := db.Create(&role).Error; err != nil {
			t.Fatal("create synthetic role")
		}
		service := newUserService(lifecycle)
		if err := service.AssignRoles(alpha.user.ID, []int64{role.ID}, &dto.AuthPrincipal{UserID: beta.user.ID, Username: beta.user.Username}); err != nil {
			t.Fatal("commit role assignment")
		}
		var committedRoles []models.UserRole
		if err := db.Where("user_id = ?", alpha.user.ID).Order("role_id").Find(&committedRoles).Error; err != nil {
			t.Fatal("read committed synthetic role membership")
		}
		assertRuntimeSocketClosed(t, alphaA)
		assertRuntimeSocketClosed(t, alphaB)
		assertRuntimePingPong(t, betaA)
		// An invalid role causes the replacement transaction to roll back; this
		// is checked with a fresh socket in the same isolated scenario.
		alphaFresh := runtimeShortSession(t, db, alpha.user, time.Hour)
		alphaC := runtimeSocket(t, alphaFresh.token)
		readRuntimeEvent(t, alphaC, enums.IMRealtimeEventConnected)
		if err := service.AssignRoles(alpha.user.ID, []int64{int64(1 << 60)}, &dto.AuthPrincipal{UserID: beta.user.ID, Username: beta.user.Username}); err == nil {
			t.Fatal("invalid role assignment unexpectedly committed")
		}
		var afterRollback []models.UserRole
		if err := db.Where("user_id = ?", alpha.user.ID).Order("role_id").Find(&afterRollback).Error; err != nil {
			t.Fatal("read role membership after rollback")
		}
		if !slices.EqualFunc(committedRoles, afterRollback, func(a, b models.UserRole) bool { return a.UserID == b.UserID && a.RoleID == b.RoleID }) {
			t.Fatal("failed role replacement changed persisted UserRole membership")
		}
		assertRuntimePingPong(t, alphaC)
	case "role_permission_replacement":
		oldPermission := models.Permission{Code: "runtime.old.permission", Name: "Old permission", Status: enums.StatusOk}
		newPermission := models.Permission{Code: "runtime.new.permission", Name: "New permission", Status: enums.StatusOk}
		role := models.Role{Code: "runtime-replacement-role", Name: "Replacement role", Status: enums.StatusOk}
		for _, record := range []any{&oldPermission, &newPermission, &role} {
			if err := db.Create(record).Error; err != nil {
				t.Fatal("create replacement fixture")
			}
		}
		if err := db.Create(&models.RolePermission{RoleID: role.ID, PermissionID: oldPermission.ID}).Error; err != nil {
			t.Fatal("seed old role permission")
		}
		if err := db.Create(&models.UserRole{UserID: alpha.user.ID, RoleID: role.ID}).Error; err != nil {
			t.Fatal("seed alpha role membership")
		}
		if err := newRoleService(lifecycle).AssignPermissions(role.ID, []int64{newPermission.ID}, &dto.AuthPrincipal{UserID: beta.user.ID, Username: beta.user.Username}); err != nil {
			t.Fatal("replace role permissions")
		}
		assertRuntimeSocketClosed(t, alphaA)
		assertRuntimeSocketClosed(t, alphaB)
		assertRuntimePingPong(t, betaA)
		snapshot, err := (employeeAuthStateReader{}).ReadEmployeeSession(alpha.login.ID, time.Now())
		if err != nil || slices.Contains(snapshot.Principal.Permissions, oldPermission.Code) || !slices.Contains(snapshot.Principal.Permissions, newPermission.Code) {
			t.Fatal("fresh alpha permission snapshot does not reflect replacement")
		}
	case "builtin_permission_sync":
		roleCode := ""
		var target constants.Permission
		for _, permission := range constants.Permissions {
			for code, permissions := range constants.RolePermissions {
				for _, candidate := range permissions {
					if candidate.Code == permission.Code {
						roleCode, target = code, permission
						break
					}
				}
				if roleCode != "" {
					break
				}
			}
			if roleCode != "" {
				break
			}
		}
		if roleCode == "" {
			t.Fatal("no built-in role permission fixture available")
		}
		roles := map[string]*models.Role{}
		for _, roleSpec := range constants.Roles {
			role := &models.Role{Code: roleSpec.Code, Name: roleSpec.Name, Status: enums.StatusOk, IsSystem: true}
			if err := db.Create(role).Error; err != nil {
				t.Fatal("create built-in role")
			}
			roles[role.Code] = role
		}
		permissions := map[string]*models.Permission{}
		for _, spec := range constants.Permissions {
			status := enums.StatusOk
			if spec.Code == target.Code {
				status = enums.StatusDisabled
			}
			item := &models.Permission{Name: spec.Name, Code: spec.Code, Type: spec.Type, GroupName: spec.GroupName, Method: spec.Method, APIPath: spec.APIPath, SortNo: spec.SortNo, Status: status, IsBuiltin: true}
			if err := db.Create(item).Error; err != nil {
				t.Fatal("create built-in permission")
			}
			permissions[item.Code] = item
		}
		for code, permissionSpecs := range constants.RolePermissions {
			for _, spec := range permissionSpecs {
				if err := db.Create(&models.RolePermission{RoleID: roles[code].ID, PermissionID: permissions[spec.Code].ID}).Error; err != nil {
					t.Fatal("seed built-in role permission")
				}
			}
		}
		grantingRole := roles[roleCode]
		if err := db.Create(&models.UserRole{UserID: alpha.user.ID, RoleID: grantingRole.ID}).Error; err != nil {
			t.Fatal("assign alpha built-in role")
		}
		if _, err := newPermissionService(lifecycle).SyncBuiltinPermissions(); err != nil {
			t.Fatal("sync built-in permissions")
		}
		assertRuntimeSocketClosed(t, alphaA)
		assertRuntimeSocketClosed(t, alphaB)
		assertRuntimePingPong(t, betaA)
		snapshot, err := (employeeAuthStateReader{}).ReadEmployeeSession(alpha.login.ID, time.Now())
		if err != nil || !slices.Contains(snapshot.Principal.Permissions, target.Code) {
			t.Fatal("fresh snapshot did not include re-enabled synchronized permission")
		}
	case "oidc_default_role_grant":
		role := models.Role{Code: constants.RoleCodeCsUser, Name: "Support Agent", Status: enums.StatusOk}
		if err := db.Create(&role).Error; err != nil {
			t.Fatal("create OIDC default role")
		}
		identity := models.UserIdentity{UserID: alpha.user.ID, Provider: enums.ThirdProviderOIDC, ProviderUserID: "synthetic-oidc-subject", ProviderName: "OIDC", RawProfile: "{}", Status: enums.StatusOk}
		if err := db.Create(&identity).Error; err != nil {
			t.Fatal("create synthetic OIDC identity")
		}
		if _, err := newOIDCLoginService(lifecycle).loginWithOIDCProfile(&oidcLoginProfile{Subject: identity.ProviderUserID, PreferredUsername: alpha.user.Username, RawProfile: "{}"}, config.AuthConfig{TokenTTLHours: 1}, "127.0.0.1", "synthetic-runtime"); err != nil {
			t.Fatal("complete synthetic OIDC login")
		}
		assertRuntimeSocketClosed(t, alphaA)
		assertRuntimeSocketClosed(t, alphaB)
		assertRuntimePingPong(t, betaA)
		var membership models.UserRole
		if err := db.Where("user_id = ? AND role_id = ?", alpha.user.ID, role.ID).First(&membership).Error; err != nil {
			t.Fatal("OIDC default role was not persisted")
		}
	case "direct_user_permission_crud":
		permission := models.Permission{Code: "runtime.direct.crud", Name: "Direct CRUD permission", Status: enums.StatusOk}
		if err := db.Create(&permission).Error; err != nil {
			t.Fatal("create direct permission")
		}
		service := newUserPermissionService(lifecycle)
		var row models.UserPermission
		for _, operation := range []string{"create", "update", "updates", "update_column", "delete"} {
			if operation == "create" {
				row = models.UserPermission{UserID: alpha.user.ID, PermissionID: permission.ID, Effect: 1}
				if err := service.Create(&row); err != nil {
					t.Fatal("create direct override")
				}
			} else {
				conn := runtimeSocket(t, alpha.token)
				readRuntimeEvent(t, conn, enums.IMRealtimeEventConnected)
				switch operation {
				case "update":
					row.Effect = -1
					if err := service.Update(&row); err != nil {
						t.Fatal("update direct override")
					}
				case "updates":
					if err := service.Updates(row.ID, map[string]interface{}{"effect": 1}); err != nil {
						t.Fatal("update direct override columns")
					}
				case "update_column":
					if err := service.UpdateColumn(row.ID, "effect", -1); err != nil {
						t.Fatal("update direct override column")
					}
				case "delete":
					if err := service.Delete(row.ID); err != nil {
						t.Fatal("delete direct override")
					}
				}
				assertRuntimeSocketClosed(t, conn)
			}
			if operation == "create" {
				assertRuntimeSocketClosed(t, alphaA)
				assertRuntimeSocketClosed(t, alphaB)
			}
			assertRuntimePingPong(t, betaA)
		}
	case "role_status_current_members":
		role := models.Role{Code: "runtime-status-role", Name: "Runtime status role", Status: enums.StatusOk}
		permission := models.Permission{Code: "runtime.status.permission", Name: "Runtime status permission", Status: enums.StatusOk}
		if err := db.Create(&role).Error; err != nil {
			t.Fatal("create synthetic role")
		}
		if err := db.Create(&permission).Error; err != nil {
			t.Fatal("create synthetic permission")
		}
		if err := db.Create(&models.RolePermission{RoleID: role.ID, PermissionID: permission.ID}).Error; err != nil {
			t.Fatal("create synthetic role permission")
		}
		if err := db.Create(&models.UserRole{UserID: alpha.user.ID, RoleID: role.ID}).Error; err != nil {
			t.Fatal("assign synthetic role")
		}
		if err := newRoleService(lifecycle).UpdateStatus(role.ID, enums.StatusDisabled, &dto.AuthPrincipal{UserID: beta.user.ID, Username: beta.user.Username}); err != nil {
			t.Fatal("disable synthetic role")
		}
		assertRuntimeSocketClosed(t, alphaA)
		assertRuntimeSocketClosed(t, alphaB)
		assertRuntimePingPong(t, betaA)
		snapshot, err := (employeeAuthStateReader{}).ReadEmployeeSession(alpha.login.ID, time.Now())
		if err != nil || slices.Contains(snapshot.Principal.Permissions, permission.Code) {
			t.Fatal("fresh authoritative snapshot retained disabled role permission")
		}
	case "committed_impact_failure_customer_isolation":
		customer := runtimeCustomerSocket(t, 77001)
		readRuntimeEvent(t, customer, enums.IMRealtimeEventConnected)
		role := models.Role{Code: "runtime-impact-role", Name: "Runtime impact role", Status: enums.StatusOk}
		if err := db.Create(&role).Error; err != nil {
			t.Fatal("create synthetic role")
		}
		if err := db.Create(&models.UserRole{UserID: alpha.user.ID, RoleID: role.ID}).Error; err != nil {
			t.Fatal("assign synthetic role")
		}
		failure := errors.New("synthetic post-commit impact read failure")
		const callback = "test:employee_runtime_impact_query_failure"
		if err := db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "t_user_role" {
				tx.AddError(failure)
			}
		}); err != nil {
			t.Fatal("install synthetic impact query failure")
		}
		t.Cleanup(func() { db.Callback().Query().Remove(callback) })
		if err := newRoleService(lifecycle).UpdateStatus(role.ID, enums.StatusDisabled, &dto.AuthPrincipal{UserID: beta.user.ID, Username: beta.user.Username}); err != nil {
			t.Fatal("committed role mutation should preserve write result")
		}
		assertRuntimeSocketClosed(t, alphaA)
		assertRuntimeSocketClosed(t, alphaB)
		assertRuntimeSocketClosed(t, betaA)
		assertRuntimeSocketClosed(t, betaB)
		assertRuntimePingPong(t, customer)
		for _, login := range []models.LoginSession{alpha.login, beta.login} {
			var persisted models.LoginSession
			if err := db.First(&persisted, login.ID).Error; err != nil || persisted.RevokedAt != nil {
				t.Fatal("impact fallback unexpectedly revoked a LoginSession row")
			}
		}
	case "pending_revoke_before_registration":
		short := runtimeShortSession(t, db, alpha.user, time.Hour)
		if err := LoginSessionService.Revoke(short.login.ID, beta.user.ID, beta.user.Username); err != nil {
			t.Fatal("revoke session before websocket registration")
		}
		assertRuntimeWebSocketRejected(t, short.token)
		if got := manager.Count(); got != 4 {
			t.Fatalf("revoked pre-registration session changed manager count: %d", got)
		}
		assertRuntimePingPong(t, betaA)
	case "pending_revoke_after_registration":
		short := runtimeShortSession(t, db, alpha.user, time.Hour)
		barrier := &runtimeRevalidationBarrier{employeeRealtimeLifecycle: lifecycle, entered: make(chan struct{}), resume: make(chan struct{})}
		WsService.lifecycle = barrier
		server := runtimeRouter()
		defer server.Close()
		type dialResult struct {
			conn     *websocket.Conn
			response *http.Response
			err      error
		}
		result := make(chan dialResult, 1)
		go func() {
			header := http.Header{"Authorization": []string{"Bearer " + short.token}}
			conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/dashboard", header)
			result <- dialResult{conn, response, err}
		}()
		select {
		case <-barrier.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("WebSocket handshake did not reach revalidation barrier")
		}
		pending := manager.snapshotEmployeeSessions(short.login.ID, nil, false)
		if len(pending) != 1 || employeeLifecyclePhase(pending[0].employeePhase.Load()) != employeePhasePending || len(pending[0].Topics) != 0 || pending[0].Closed.Load() {
			t.Fatal("registered handshake was not a topic-free pending session")
		}
		if err := LoginSessionService.Revoke(short.login.ID, beta.user.ID, beta.user.Username); err != nil {
			t.Fatal("revoke session while handshake revalidation is blocked")
		}
		if manager.Count() != 4 {
			t.Fatal("revoked pending session remained registered")
		}
		barrier.release()
		var dial dialResult
		select {
		case dial = <-result:
		case <-time.After(3 * time.Second):
			t.Fatal("blocked WebSocket handshake did not finish after revalidation resumed")
		}
		if dial.err == nil {
			if dial.conn == nil {
				t.Fatal("successful handshake returned no connection")
			}
			assertRuntimeSocketClosedEventually(t, dial.conn, time.Second)
			_ = dial.conn.Close()
		} else if dial.response == nil || dial.response.StatusCode != http.StatusUnauthorized {
			t.Fatal("post-revoke handshake did not fail closed")
		}
		if manager.Count() != 4 || len(manager.snapshotEmployeeSessions(short.login.ID, nil, false)) != 0 {
			t.Fatal("revoked pending session was resurrected")
		}
		assertRuntimePingPong(t, betaA)
	case "timer_publication_and_callback_revoke":
		publicationSession, publicationConn := runtimeConnectAndFindSession(t, manager, alpha)
		if old := publicationSession.detachLifecycleTimer(); old != nil {
			old.Stop()
		}
		beforePublish, resumePublish := make(chan struct{}), make(chan struct{})
		installDone := make(chan bool, 1)
		go func() {
			installDone <- lifecycle.installLifecycleTimerWithBeforePublish(publicationSession, time.Now().Add(time.Hour), func() { close(beforePublish); <-resumePublish })
		}()
		select {
		case <-beforePublish:
		case <-time.After(3 * time.Second):
			t.Fatal("timer installation did not reach publication barrier")
		}
		if err := LoginSessionService.Revoke(alpha.login.ID, beta.user.ID, beta.user.Username); err != nil {
			t.Fatal("revoke during timer publication")
		}
		close(resumePublish)
		select {
		case installed := <-installDone:
			if installed {
				t.Fatal("timer published after revoke closed the session")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timer publication raced into a wait cycle")
		}
		if !publicationSession.Closed.Load() || lifecycleTimerReference(publicationSession) != nil || manager.CloseSession(publicationSession) {
			t.Fatal("publication race did not leave one terminal close with no timer")
		}
		assertRuntimeSendClosed(t, publicationSession)
		assertRuntimeSocketClosed(t, publicationConn)
		assertRuntimePingPong(t, betaA)
		fresh := runtimeShortSession(t, db, alpha.user, time.Hour)
		callbacks := make(chan func(), 1)
		lifecycle.afterFunc = func(_ time.Duration, callback func()) *time.Timer {
			callbacks <- callback
			return time.AfterFunc(time.Hour, callback)
		}
		callbackSession, callbackConn := runtimeConnectAndFindSession(t, manager, fresh)
		var callback func()
		select {
		case callback = <-callbacks:
		case <-time.After(3 * time.Second):
			t.Fatal("lifecycle timer callback was not captured")
		}
		ready := make(chan struct{}, 2)
		start := make(chan struct{})
		callbackDone, revokeDone := make(chan struct{}), make(chan error, 1)
		go func() { ready <- struct{}{}; <-start; callback(); close(callbackDone) }()
		go func() {
			ready <- struct{}{}
			<-start
			revokeDone <- LoginSessionService.Revoke(fresh.login.ID, beta.user.ID, beta.user.Username)
		}()
		<-ready
		<-ready
		close(start)
		select {
		case <-callbackDone:
		case <-time.After(3 * time.Second):
			t.Fatal("timer callback and revoke deadlocked")
		}
		select {
		case err := <-revokeDone:
			if err != nil {
				t.Fatal("revoke racing timer callback failed")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("revoke and timer callback did not complete")
		}
		if !callbackSession.Closed.Load() || lifecycleTimerReference(callbackSession) != nil || manager.CloseSession(callbackSession) {
			t.Fatal("timer callback race did not produce one terminal close and clear timer")
		}
		assertRuntimeSendClosed(t, callbackSession)
		assertRuntimeSocketClosed(t, callbackConn)
		assertRuntimePingPong(t, betaA)
	case "idle_login_session_expiry":
		shortenRuntimeSession(t, db, alpha.login.ID, 300*time.Millisecond)
		// Reconnect using a dedicated short-lived LoginSession so authoritative
		// handshake revalidation sees the same persisted deadline.
		short := runtimeShortSession(t, db, alpha.user, 400*time.Millisecond)
		conn := runtimeSocket(t, short.token)
		readRuntimeEvent(t, conn, enums.IMRealtimeEventConnected)
		assertRuntimeSocketClosedEventually(t, conn, time.Second)
		assertRuntimePingPong(t, betaA)
	case "idle_direct_allow_expiry":
		permission := models.Permission{Code: "runtime.direct", Name: "Runtime direct", Status: enums.StatusOk}
		if err := db.Create(&permission).Error; err != nil {
			t.Fatal("create synthetic permission")
		}
		expires := time.Now().Add(400 * time.Millisecond)
		allow := 1
		if err := db.Create(&models.UserPermission{UserID: alpha.user.ID, PermissionID: permission.ID, Effect: allow, ExpiredAt: &expires}).Error; err != nil {
			t.Fatal("create synthetic direct override")
		}
		conn := runtimeSocket(t, alpha.token)
		readRuntimeEvent(t, conn, enums.IMRealtimeEventConnected)
		assertRuntimeSocketClosedEventually(t, conn, time.Second)
		assertRuntimePingPong(t, betaA)
		if _, err := (employeeAuthStateReader{}).ReadEmployeeSession(alpha.login.ID, time.Now()); err != nil {
			t.Fatal("still-valid LoginSession did not revalidate after direct allow expiry")
		}
	case "idle_direct_deny_expiry":
		permission := models.Permission{Code: "runtime.role.permission", Name: "Runtime role permission", Status: enums.StatusOk}
		role := models.Role{Code: "runtime-deny-role", Name: "Runtime deny role", Status: enums.StatusOk}
		if err := db.Create(&permission).Error; err != nil {
			t.Fatal("create synthetic permission")
		}
		if err := db.Create(&role).Error; err != nil {
			t.Fatal("create synthetic role")
		}
		if err := db.Create(&models.RolePermission{RoleID: role.ID, PermissionID: permission.ID}).Error; err != nil {
			t.Fatal("create role permission")
		}
		if err := db.Create(&models.UserRole{UserID: alpha.user.ID, RoleID: role.ID}).Error; err != nil {
			t.Fatal("assign synthetic role")
		}
		expires := time.Now().Add(400 * time.Millisecond)
		if err := db.Create(&models.UserPermission{UserID: alpha.user.ID, PermissionID: permission.ID, Effect: -1, ExpiredAt: &expires}).Error; err != nil {
			t.Fatal("create synthetic direct deny")
		}
		conn := runtimeSocket(t, alpha.token)
		readRuntimeEvent(t, conn, enums.IMRealtimeEventConnected)
		assertRuntimeSocketClosedEventually(t, conn, time.Second)
		snapshot, err := (employeeAuthStateReader{}).ReadEmployeeSession(alpha.login.ID, time.Now())
		if err != nil || !slices.Contains(snapshot.Principal.Permissions, permission.Code) {
			t.Fatal("still-valid LoginSession did not restore role-derived grant after direct deny expiry")
		}
		assertRuntimePingPong(t, betaA)
	default:
		t.Fatalf("unknown synthetic scenario %q", scenario)
	}
}

type runtimeEmployeeFixture struct {
	user  *models.User
	token string
	login models.LoginSession
}

func runtimeEmployee(t *testing.T, db *gorm.DB, username, token string) runtimeEmployeeFixture {
	t.Helper()
	// Use the repository's auth fixture for password and audit fields, then mark
	// the synthetic identity as an enabled employee and seed a private session.
	user := createAuthTestUser(t, db, username, "synthetic-password")
	if err := db.Model(user).Update("user_type", enums.UserTypeEmployee).Error; err != nil {
		t.Fatal("mark synthetic user as employee")
	}
	login := employeeMutationLogin(t, db, user.ID, token)
	return runtimeEmployeeFixture{user: user, token: token, login: login}
}

func runtimeShortSession(t *testing.T, db *gorm.DB, user *models.User, ttl time.Duration) runtimeEmployeeFixture {
	t.Helper()
	token := "runtime-short-session"
	now := time.Now()
	login := models.LoginSession{UserID: user.ID, Token: token, ClientType: "admin_web", ExpiredAt: now.Add(ttl), AuditFields: models.AuditFields{CreatedAt: now, UpdatedAt: now}}
	if err := db.Create(&login).Error; err != nil {
		t.Fatal("create short synthetic session")
	}
	return runtimeEmployeeFixture{user: user, token: token, login: login}
}

func shortenRuntimeSession(t *testing.T, db *gorm.DB, id int64, ttl time.Duration) {
	t.Helper()
	if err := db.Model(&models.LoginSession{}).Where("id = ?", id).Update("expired_at", time.Now().Add(ttl)).Error; err != nil {
		t.Fatal("set synthetic expiry")
	}
}

func runtimeRouter() *httptest.Server {
	router := gin.New()
	router.GET("/auth", func(ctx *gin.Context) {
		if _, err := AuthService.Authenticate(ctx); err != nil {
			ctx.Status(http.StatusUnauthorized)
			return
		}
		ctx.Status(http.StatusOK)
	})
	router.GET("/ws/dashboard", func(ctx *gin.Context) {
		if _, err := AuthService.Authenticate(ctx); err != nil {
			ctx.Status(http.StatusUnauthorized)
			return
		}
		WsService.HandleDashboardWS(ctx)
	})
	return httptest.NewServer(router)
}

func runtimeSocket(t *testing.T, token string) *websocket.Conn {
	t.Helper()
	server := runtimeRouter()
	t.Cleanup(server.Close)
	header := http.Header{"Authorization": []string{"Bearer " + token}}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/dashboard", header)
	if err != nil {
		t.Fatal("synthetic authenticated WebSocket handshake failed")
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

type runtimeRevalidationBarrier struct {
	*employeeRealtimeLifecycle
	entered     chan struct{}
	resume      chan struct{}
	enteredOnce sync.Once
	resumeOnce  sync.Once
}

func (b *runtimeRevalidationBarrier) RevalidateEmployeeSession(id int64, now time.Time) (EmployeeSessionSnapshot, error) {
	b.enteredOnce.Do(func() { close(b.entered) })
	<-b.resume
	return b.employeeRealtimeLifecycle.RevalidateEmployeeSession(id, now)
}

func (b *runtimeRevalidationBarrier) release() { b.resumeOnce.Do(func() { close(b.resume) }) }

func runtimeConnectAndFindSession(t *testing.T, manager *WsConnectionManager, fixture runtimeEmployeeFixture) (*ClientSession, *websocket.Conn) {
	t.Helper()
	previous := make(map[string]struct{})
	for _, session := range manager.snapshotEmployeeSessions(fixture.login.ID, nil, false) {
		previous[session.ID] = struct{}{}
	}
	conn := runtimeSocket(t, fixture.token)
	readRuntimeEvent(t, conn, enums.IMRealtimeEventConnected)
	for _, session := range manager.snapshotEmployeeSessions(fixture.login.ID, nil, false) {
		if _, existed := previous[session.ID]; !existed && !session.Closed.Load() {
			return session, conn
		}
	}
	t.Fatal("new runtime WebSocket did not activate an employee session")
	return nil, nil
}

func runtimeCustomerSocket(t *testing.T, customerID int64) *websocket.Conn {
	t.Helper()
	router := gin.New()
	router.GET("/ws/customer", func(ctx *gin.Context) {
		external := &openidentity.ExternalUser{ExternalID: "runtime-customer", ExternalSource: enums.ExternalSourceGuest}
		if err := WsService.upgradeConnection(ctx, nil, external, realtimeRoleUser, &CustomerSessionVerifyResult{CustomerID: customerID}); err != nil {
			t.Errorf("synthetic customer upgrade failed: %v", err)
		}
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/customer", nil)
	if err != nil {
		t.Fatal("synthetic verified-customer WebSocket handshake failed")
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func authenticateRuntime(token string) int {
	server := runtimeRouter()
	defer server.Close()
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/auth", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0
	}
	defer response.Body.Close()
	return response.StatusCode
}

func assertRuntimeWebSocketRejected(t *testing.T, token string) {
	t.Helper()
	server := runtimeRouter()
	defer server.Close()
	header := http.Header{"Authorization": []string{"Bearer " + token}}
	conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws/dashboard", header)
	if err == nil {
		_ = conn.Close()
		t.Fatal("disabled employee established a fresh WebSocket")
	}
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatal("disabled employee WebSocket was not rejected by fresh authentication")
	}
}

func readRuntimeEvent(t *testing.T, conn *websocket.Conn, want string) map[string]any {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal("set synthetic socket deadline")
	}
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read lifecycle control event %q: %v", want, err)
	}
	var event struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(payload, &event); err != nil || event.Type != want {
		t.Fatalf("unexpected lifecycle control event: type=%q decodeErr=%v", event.Type, err)
	}
	return event.Data
}

func assertRuntimeSocketClosed(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("revoked synthetic socket remained open")
	}
}

func assertRuntimeSendClosed(t *testing.T, session *ClientSession) {
	t.Helper()
	if session == nil || session.Send == nil {
		t.Fatal("terminal session has no Send channel")
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case _, ok := <-session.Send:
			if !ok {
				return
			}
		case <-deadline.C:
			t.Fatal("Send channel did not close after terminal transition")
		}
	}
}

func assertRuntimeSocketClosedEventually(t *testing.T, conn *websocket.Conn, timeout time.Duration) {
	t.Helper()
	if conn == nil || timeout <= 0 {
		t.Fatal("socket-close timeout and connection must be valid")
	}
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatal("set bounded socket-close deadline")
	}
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("revoked synthetic socket remained open")
	} else {
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			t.Fatalf("synthetic socket did not close within %s", timeout)
		}
	}
}

func assertRuntimePingPong(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	if err := conn.WriteJSON(map[string]any{"type": "ping"}); err != nil {
		t.Fatal("write synthetic ping")
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal("set synthetic ping deadline")
	}
	_, payload, err := conn.ReadMessage()
	if err != nil {
		t.Fatal("unaffected employee socket did not respond")
	}
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &event); err != nil || event.Type != enums.IMRealtimeEventPong {
		t.Fatal("unaffected employee socket did not return pong")
	}
}

func registerRuntimeLoginSessionUpdateFailure(t *testing.T, db *gorm.DB) error {
	t.Helper()
	failure := errors.New("synthetic later revoke write failure")
	const callback = "test:employee_session_runtime_login_update_failure"
	if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "t_login_session" {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal("install synthetic LoginSession failure")
	}
	t.Cleanup(func() { db.Callback().Update().Remove(callback) })
	return failure
}
