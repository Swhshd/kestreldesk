package services

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"gorm.io/gorm"
)

type employeeRealtimeInvalidationSpy struct {
	loginIDs      []int64
	ids           []int64
	allCalls      int
	employeeCalls int
}

func TestEmployeeRBACPermissionSyncChangedKeys(t *testing.T) {
	db, invalidator, user, _ := employeeMutationFixture(t)
	other := createAuthTestUser(t, db, "rbac-sync-other", "secret")
	roles := make(map[string]models.Role)
	for code := range constants.RolePermissions {
		role := models.Role{Code: code, Name: code, Status: enums.StatusOk}
		if err := db.Create(&role).Error; err != nil {
			t.Fatal(err)
		}
		roles[code] = role
	}
	permissions := make(map[string]models.Permission)
	for _, spec := range constants.Permissions {
		status := enums.StatusOk
		if spec.Code == constants.Permissions[0].Code {
			status = enums.StatusDisabled
		}
		permission := models.Permission{Code: spec.Code, Name: spec.Name, Status: status, IsBuiltin: true}
		if err := db.Create(&permission).Error; err != nil {
			t.Fatal(err)
		}
		permissions[spec.Code] = permission
	}
	roleCodes := make([]string, 0, len(constants.RolePermissions))
	for code := range constants.RolePermissions {
		roleCodes = append(roleCodes, code)
	}
	slices.Sort(roleCodes)
	missingRoleCode := ""
	for _, code := range roleCodes {
		for _, spec := range constants.RolePermissions[code] {
			permission := permissions[spec.Code]
			if missingRoleCode == "" && spec.Code != constants.Permissions[0].Code {
				missingRoleCode = code
				continue
			}
			if err := db.Create(&models.RolePermission{RoleID: roles[code].ID, PermissionID: permission.ID}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	firstRoleCode := ""
	for _, code := range roleCodes {
		if slices.ContainsFunc(constants.RolePermissions[code], func(p constants.Permission) bool { return p.Code == constants.Permissions[0].Code }) {
			firstRoleCode = code
			break
		}
	}
	if firstRoleCode == "" || missingRoleCode == "" {
		t.Fatal("fixture lacks expected builtin role mappings")
	}
	for _, row := range []models.UserRole{{UserID: user.ID, RoleID: roles[firstRoleCode].ID}, {UserID: other.ID, RoleID: roles[missingRoleCode].ID}} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&models.UserPermission{UserID: user.ID, PermissionID: permissions[constants.Permissions[0].Code].ID, Effect: -1}).Error; err != nil {
		t.Fatal(err)
	}
	calls := 0
	invalidator.onEmployees = func(ids []int64) {
		calls++
		if !reflect.DeepEqual(ids, []int64{user.ID, other.ID}) {
			t.Fatalf("sync affected employees = %v", ids)
		}
		var enabled models.Permission
		if err := db.First(&enabled, permissions[constants.Permissions[0].Code].ID).Error; err != nil || enabled.Status != enums.StatusOk {
			t.Fatal("sync invalidated before permission commit")
		}
	}
	if _, err := newPermissionService(invalidator).SyncBuiltinPermissions(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("sync invalidation callbacks = %d, want 1", calls)
	}
}

func TestEmployeeRBACPermissionSyncNoop(t *testing.T) {
	db := lifecycleAuthTestDB(t)
	invalidator := &employeeRealtimeInvalidationSpy{}
	for code := range constants.RolePermissions {
		if err := db.Create(&models.Role{Code: code, Name: code, Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// A first sync establishes the canonical rows and links; the second is an
	// authorization-key no-op even though metadata timestamps are refreshed.
	svc := newPermissionService(invalidator)
	if _, err := svc.SyncBuiltinPermissions(); err != nil {
		t.Fatal(err)
	}
	invalidator.ids = nil
	invalidator.allCalls = 0
	invalidator.employeeCalls = 0
	if _, err := svc.SyncBuiltinPermissions(); err != nil {
		t.Fatal(err)
	}
	if len(invalidator.ids) != 0 || invalidator.allCalls != 0 || invalidator.employeeCalls != 0 {
		t.Fatal("metadata-only builtin sync invalidated employees")
	}
}

func (s *employeeRealtimeInvalidationSpy) InvalidateLoginSession(id int64) int {
	s.loginIDs = append(s.loginIDs, id)
	return 1
}
func (s *employeeRealtimeInvalidationSpy) InvalidateEmployees(ids []int64) int {
	s.employeeCalls++
	s.ids = append(s.ids, ids...)
	return len(ids)
}
func (s *employeeRealtimeInvalidationSpy) InvalidateAllEmployees() int { s.allCalls++; return 1 }

func TestEmployeeRBACRoleStatusCommittedMembers(t *testing.T) {
	db, invalidator, user, actor := employeeMutationFixture(t)
	role := models.Role{Code: "rbac-status", Name: "RBAC status", Status: enums.StatusOk}
	if err := db.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.UserRole{UserID: user.ID, RoleID: role.ID}).Error; err != nil {
		t.Fatal(err)
	}
	row := employeeMutationLogin(t, db, user.ID, "rbac-status")
	a, b := employeeMutationSockets(t, invalidator, user.ID, row.ID, "rbac-status")
	other := createAuthTestUser(t, db, "rbac-status-other", "secret")
	otherRow := employeeMutationLogin(t, db, other.ID, "rbac-status-other")
	otherA, otherB := employeeMutationSockets(t, invalidator, other.ID, otherRow.ID, "rbac-status-other")
	invalidator.onEmployees = func(ids []int64) {
		var persisted models.Role
		if err := db.First(&persisted, role.ID).Error; err != nil || persisted.Status != enums.StatusDisabled {
			t.Fatal("role status invalidation ran before commit")
		}
		if !reflect.DeepEqual(ids, []int64{user.ID}) {
			t.Fatalf("affected employees = %v", ids)
		}
	}
	if err := newRoleService(invalidator).UpdateStatus(role.ID, enums.StatusDisabled, actor); err != nil {
		t.Fatal(err)
	}
	if !a.Closed.Load() || !b.Closed.Load() || otherA.Closed.Load() || otherB.Closed.Load() {
		t.Fatal("role status did not close only current members")
	}
}

func TestEmployeeRBACRolePermissionsCommitRollback(t *testing.T) {
	db, invalidator, user, actor := employeeMutationFixture(t)
	role := models.Role{Code: "rbac-permissions", Name: "RBAC permissions", Status: enums.StatusOk}
	permission := models.Permission{Code: "rbac.permission", Name: "RBAC permission", Status: enums.StatusOk}
	if err := db.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&permission).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.UserRole{UserID: user.ID, RoleID: role.ID}).Error; err != nil {
		t.Fatal(err)
	}
	row := employeeMutationLogin(t, db, user.ID, "rbac-permissions")
	a, b := employeeMutationSockets(t, invalidator, user.ID, row.ID, "rbac-permissions")
	invalidator.onEmployees = func(ids []int64) {
		var count int64
		if err := db.Model(&models.RolePermission{}).Where("role_id = ? AND permission_id = ?", role.ID, permission.ID).Count(&count).Error; err != nil || count != 1 {
			t.Fatal("role permissions invalidation ran before committed replacement")
		}
		if !reflect.DeepEqual(ids, []int64{user.ID}) {
			t.Fatalf("affected employees = %v", ids)
		}
	}
	if err := newRoleService(invalidator).AssignPermissions(role.ID, []int64{permission.ID}, actor); err != nil {
		t.Fatal(err)
	}
	if !a.Closed.Load() || !b.Closed.Load() {
		t.Fatal("committed role permission change did not close its member")
	}
}

func TestEmployeeRBACRolePermissionsRollbackNoInvalidation(t *testing.T) {
	db, invalidator, user, actor := employeeMutationFixture(t)
	role := models.Role{Code: "rbac-permissions-rollback", Name: "RBAC permissions rollback", Status: enums.StatusOk}
	permission := models.Permission{Code: "rbac.permission.rollback", Name: "RBAC permission", Status: enums.StatusOk}
	if err := db.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&permission).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.UserRole{UserID: user.ID, RoleID: role.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.RolePermission{RoleID: role.ID, PermissionID: permission.ID}).Error; err != nil {
		t.Fatal(err)
	}
	row := employeeMutationLogin(t, db, user.ID, "rbac-permissions-rollback")
	a, b := employeeMutationSockets(t, invalidator, user.ID, row.ID, "rbac-permissions-rollback")
	invalidator.onEmployees = func([]int64) { t.Fatal("rolled-back permission replacement invalidated") }
	if err := newRoleService(invalidator).AssignPermissions(role.ID, []int64{999999}, actor); err == nil {
		t.Fatal("expected missing permission to roll back replacement")
	}
	var count int64
	if err := db.Model(&models.RolePermission{}).Where("role_id = ? AND permission_id = ?", role.ID, permission.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("rollback did not preserve original role permission")
	}
	if a.Closed.Load() || b.Closed.Load() {
		t.Fatal("rolled-back permission replacement closed sockets")
	}
}

func TestEmployeeRBACImpactResolutionFailure(t *testing.T) {
	db := lifecycleAuthTestDB(t)
	spy := &employeeRealtimeInvalidationSpy{}
	role := models.Role{Code: "rbac-impact-failure", Name: "RBAC impact failure", Status: enums.StatusOk}
	if err := db.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.UserRole{UserID: 991, RoleID: role.ID}).Error; err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected committed impact query failure")
	const name = "test:employee_impact_resolution_failure"
	if err := db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "t_user_role" {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Query().Remove(name)
	if err := newRoleService(spy).UpdateStatus(role.ID, enums.StatusDisabled, &dto.AuthPrincipal{UserID: 1, Username: "operator"}); err != nil {
		t.Fatalf("committed status update returned error: %v", err)
	}
	var persisted models.Role
	if err := db.First(&persisted, role.ID).Error; err != nil || persisted.Status != enums.StatusDisabled {
		t.Fatal("post-commit impact failure rolled back role status")
	}
	if spy.allCalls != 1 || len(spy.ids) != 0 {
		t.Fatalf("committed impact failure did not use fail-closed fallback: ids=%v all=%d", spy.ids, spy.allCalls)
	}
}

func TestEmployeeRBACImpactKeyFailure(t *testing.T) {
	db := lifecycleAuthTestDB(t)
	spy := &employeeRealtimeInvalidationSpy{}
	for code := range constants.RolePermissions {
		if err := db.Create(&models.Role{Code: code, Name: code, Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
	}
	failure := errors.New("injected permission impact key query failure")
	const name = "test:employee_impact_key_failure"
	if err := db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "t_permission" {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Query().Remove(name)
	if _, err := newPermissionService(spy).SyncBuiltinPermissions(); !errors.Is(err, failure) {
		t.Fatalf("sync pre-key error = %v", err)
	}
	db.Callback().Query().Remove(name)
	var count int64
	if err := db.Model(&models.Permission{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("impact key failure did not roll back permission writes")
	}
	if spy.employeeCalls != 0 || spy.allCalls != 0 {
		t.Fatal("pre-commit impact key failure invalidated employees")
	}
}

func TestEmployeeRoleMemberOrdering(t *testing.T) {
	db, invalidator, user, actor := employeeMutationFixture(t)
	_, role := employeeMutationRoles(t, db, user.ID)
	started := make(chan struct{})
	release := make(chan struct{})
	const name = "test:employee_role_member_ordering"
	if err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "t_role" {
			close(started)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		db.Callback().Update().Remove(name)
	})
	done := make(chan error, 1)
	go func() { done <- newRoleService(invalidator).UpdateStatus(role.ID, enums.StatusDisabled, actor) }()
	lifecycleAwait(t, started)
	if err := newUserService(invalidator).AssignRoles(user.ID, []int64{role.ID}, actor); err != nil {
		t.Fatalf("membership mutation while role enabled: %v", err)
	}
	row := employeeMutationLogin(t, db, user.ID, "role-ordering-fresh")
	fresh := lifecycleTestSession(t, invalidator.manager, "role-ordering-fresh", user.ID, row.ID, true)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !fresh.Closed.Load() {
		t.Fatal("post-write current-member lookup missed a newly assigned employee socket")
	}
}

func TestEmployeePermissionRelationOrdering(t *testing.T) {
	db, invalidator, user, actor := employeeMutationFixture(t)
	roles := make(map[string]models.Role)
	for code := range constants.RolePermissions {
		role := models.Role{Code: code, Name: code, Status: enums.StatusOk}
		if err := db.Create(&role).Error; err != nil {
			t.Fatal(err)
		}
		roles[code] = role
	}
	permissions := make(map[string]models.Permission)
	for _, spec := range constants.Permissions {
		permission := models.Permission{Code: spec.Code, Name: spec.Name, Status: enums.StatusOk, IsBuiltin: true}
		if err := db.Create(&permission).Error; err != nil {
			t.Fatal(err)
		}
		permissions[spec.Code] = permission
	}
	roleCodes := make([]string, 0, len(constants.RolePermissions))
	for code := range constants.RolePermissions {
		roleCodes = append(roleCodes, code)
	}
	slices.Sort(roleCodes)
	var targetRole string
	for _, code := range roleCodes {
		for _, spec := range constants.RolePermissions[code] {
			if targetRole == "" {
				targetRole = code
				continue
			}
			if err := db.Create(&models.RolePermission{RoleID: roles[code].ID, PermissionID: permissions[spec.Code].ID}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	if targetRole == "" {
		t.Fatal("fixture has no builtin role link")
	}
	started, release := make(chan struct{}), make(chan struct{})
	const name = "test:employee_permission_relation_ordering"
	if err := db.Callback().Query().Before("gorm:query").Register(name, func(query *gorm.DB) {
		if query.Statement.Table == "t_user_role" {
			if _, inTransaction := query.Statement.ConnPool.(gorm.TxCommitter); !inTransaction {
				close(started)
				<-release
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		db.Callback().Query().Remove(name)
	})
	done := make(chan error, 1)
	go func() { _, err := newPermissionService(invalidator).SyncBuiltinPermissions(); done <- err }()
	lifecycleAwait(t, started)
	if err := newUserService(invalidator).AssignRoles(user.ID, []int64{roles[targetRole].ID}, actor); err != nil {
		t.Fatalf("membership change while sync impact query paused: %v", err)
	}
	row := employeeMutationLogin(t, db, user.ID, "permission-ordering-fresh")
	fresh := lifecycleTestSession(t, invalidator.manager, "permission-ordering-fresh", user.ID, row.ID, true)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !fresh.Closed.Load() {
		t.Fatal("post-commit permission impact query missed a newly added role member")
	}
}

func employeeStatusMutation(s *userService, mode string, id int64, actor *dto.AuthPrincipal) error {
	if mode == "delete" {
		return s.DeleteUser(id, actor)
	}
	status := enums.StatusDisabled
	if mode == "deleted status" {
		status = enums.StatusDeleted
	}
	return s.UpdateStatus(id, int(status), actor)
}

func TestEmployeeStatusInvalidationBeforeRevokeFailure(t *testing.T) {
	for _, mode := range []string{"disable", "deleted status", "delete"} {
		t.Run(mode, func(t *testing.T) {
			db, l, user, actor := employeeMutationFixture(t)
			row := employeeMutationLogin(t, db, user.ID, "status")
			a, b := employeeMutationSockets(t, l, user.ID, row.ID, "target")
			otherA, otherB := employeeMutationSockets(t, l, user.ID+1, row.ID+1, "other")
			failure := errors.New("injected later session update failure")
			employeeMutationFailUpdate(t, db, "t_login_session", failure)
			calls := 0
			wantStatus := enums.StatusDisabled
			if mode == "deleted status" {
				wantStatus = enums.StatusDeleted
			}
			l.onEmployees = func(ids []int64) {
				calls++
				persisted := newUserService(l).Get(user.ID)
				if !reflect.DeepEqual(ids, []int64{user.ID}) || persisted.Status != wantStatus || (mode == "delete" && persisted.DeletedAt == nil) || employeeMutationReadLogin(t, db, row.ID).RevokedAt != nil {
					t.Fatal("status close ran before status persistence or after revoke")
				}
			}
			err := employeeStatusMutation(newUserService(l), mode, user.ID, actor)
			persisted := newUserService(l).Get(user.ID)
			if !errors.Is(err, failure) || calls != 1 || persisted.Status != wantStatus || (mode == "delete" && persisted.DeletedAt == nil) || !a.Closed.Load() || !b.Closed.Load() || otherA.Closed.Load() || otherB.Closed.Load() {
				t.Fatal("persisted status did not close target before later revoke failure")
			}
		})
	}
}

func TestEmployeeStatusWriteFailureDoesNotInvalidate(t *testing.T) {
	for _, mode := range []string{"disable", "deleted status", "delete"} {
		t.Run(mode, func(t *testing.T) {
			db, l, user, actor := employeeMutationFixture(t)
			row := employeeMutationLogin(t, db, user.ID, "status")
			a, b := employeeMutationSockets(t, l, user.ID, row.ID, "target")
			failure := errors.New("injected user update failure")
			employeeMutationFailUpdate(t, db, "t_user", failure)
			l.onEmployees = func([]int64) { t.Fatal("failed status write invalidated") }
			err := employeeStatusMutation(newUserService(l), mode, user.ID, actor)
			persisted := newUserService(l).Get(user.ID)
			if !errors.Is(err, failure) || persisted.Status != enums.StatusOk || persisted.DeletedAt != nil || a.Closed.Load() || b.Closed.Load() || employeeMutationReadLogin(t, db, row.ID).RevokedAt != nil {
				t.Fatal("failed first write changed status/session/socket state")
			}
		})
	}
}

func TestEmployeeStatusSuccess(t *testing.T) {
	for _, mode := range []string{"disable", "deleted status", "delete", "enable"} {
		t.Run(mode, func(t *testing.T) {
			db, l, user, actor := employeeMutationFixture(t)
			row := employeeMutationLogin(t, db, user.ID, "status")
			a, b := employeeMutationSockets(t, l, user.ID, row.ID, "target")
			svc := newUserService(l)
			var err error
			if mode == "enable" {
				err = svc.UpdateStatus(user.ID, int(enums.StatusOk), actor)
			} else {
				err = employeeStatusMutation(svc, mode, user.ID, actor)
			}
			if err != nil {
				t.Fatal(err)
			}
			wantClosed := mode != "enable"
			if a.Closed.Load() != wantClosed || b.Closed.Load() != wantClosed || (employeeMutationReadLogin(t, db, row.ID).RevokedAt != nil) != wantClosed {
				t.Fatal("status success changed wrong lifecycle state")
			}
		})
	}
}

func employeeMutationRoles(t *testing.T, db *gorm.DB, userID int64) (models.Role, models.Role) {
	t.Helper()
	oldRole := models.Role{Code: "old", Name: "old", Status: enums.StatusOk}
	newRole := models.Role{Code: "new", Name: "new", Status: enums.StatusOk}
	for _, row := range []any{&oldRole, &newRole} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&models.UserRole{UserID: userID, RoleID: oldRole.ID}).Error; err != nil {
		t.Fatal(err)
	}
	return oldRole, newRole
}

func employeeMutationRoleIDs(t *testing.T, db *gorm.DB, userID int64) []int64 {
	t.Helper()
	var ids []int64
	if err := db.Model(&models.UserRole{}).Where("user_id = ?", userID).Order("role_id").Pluck("role_id", &ids).Error; err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestEmployeeAssignRolesCommitOrdering(t *testing.T) {
	for _, failRevoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "later revoke failure"}[failRevoke], func(t *testing.T) {
			db, l, user, actor := employeeMutationFixture(t)
			_, newRole := employeeMutationRoles(t, db, user.ID)
			row := employeeMutationLogin(t, db, user.ID, "roles")
			a, b := employeeMutationSockets(t, l, user.ID, row.ID, "target")
			otherA, otherB := employeeMutationSockets(t, l, user.ID+1, row.ID+1, "other")
			failure := errors.New("injected later revoke failure")
			if failRevoke {
				employeeMutationFailUpdate(t, db, "t_login_session", failure)
			}
			calls := 0
			l.onEmployees = func(ids []int64) {
				calls++
				if !reflect.DeepEqual(ids, []int64{user.ID}) || !reflect.DeepEqual(employeeMutationRoleIDs(t, db, user.ID), []int64{newRole.ID}) {
					t.Fatal("role invalidation did not see committed target membership")
				}
				if calls == 1 && employeeMutationReadLogin(t, db, row.ID).RevokedAt != nil {
					t.Fatal("role invalidation ran after revoke-all")
				}
			}
			err := newUserService(l).AssignRoles(user.ID, []int64{newRole.ID}, actor)
			wantCalls := 2
			if failRevoke {
				wantCalls = 1
				if !errors.Is(err, failure) {
					t.Fatalf("later revoke error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if calls != wantCalls || !a.Closed.Load() || !b.Closed.Load() || otherA.Closed.Load() || otherB.Closed.Load() {
				t.Fatal("committed role membership did not invalidate before revoke-all")
			}
		})
	}
}

func TestEmployeeAssignRolesRollbackNoInvalidation(t *testing.T) {
	for _, mode := range []string{"invalid role", "insert failure", "commit failure"} {
		t.Run(mode, func(t *testing.T) {
			db, l, user, actor := employeeMutationFixture(t)
			oldRole, newRole := employeeMutationRoles(t, db, user.ID)
			row := employeeMutationLogin(t, db, user.ID, "roles")
			a, b := employeeMutationSockets(t, l, user.ID, row.ID, "target")
			roleIDs := []int64{newRole.ID, 999999}
			if mode == "insert failure" {
				roleIDs = []int64{newRole.ID}
				const name = "test:role_insert_failure"
				if err := db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Table == "t_user_role" {
						tx.AddError(errors.New("injected membership insert failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Callback().Create().Remove(name) })
			}
			if mode == "commit failure" {
				roleIDs = []int64{newRole.ID}
				const name = "test:role_commit_failure"
				if err := db.Callback().Create().After("gorm:create").Register(name, func(tx *gorm.DB) {
					if tx.Statement.Table == "t_user_role" {
						if err := tx.Rollback().Error; err != nil {
							t.Fatal(err)
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Callback().Create().Remove(name) })
			}
			l.onEmployees = func([]int64) { t.Fatal("rolled-back role mutation invalidated") }
			if err := newUserService(l).AssignRoles(user.ID, roleIDs, actor); err == nil {
				t.Fatal("expected membership transaction failure")
			}
			if !reflect.DeepEqual(employeeMutationRoleIDs(t, db, user.ID), []int64{oldRole.ID}) || a.Closed.Load() || b.Closed.Load() || employeeMutationReadLogin(t, db, row.ID).RevokedAt != nil {
				t.Fatal("rollback changed membership/session/socket state")
			}
		})
	}
}

func TestEmployeeAssignRolesTransactionRoleReads(t *testing.T) {
	db, l, user, actor := employeeMutationFixture(t)
	_, newRole := employeeMutationRoles(t, db, user.ID)
	const name = "test:require_transaction_role_read"
	reads := 0
	if err := db.Callback().Query().Before("gorm:query").Register(name, func(query *gorm.DB) {
		if query.Statement.Table == "t_role" {
			reads++
			if _, ok := query.Statement.ConnPool.(gorm.TxCommitter); !ok {
				query.AddError(errors.New("role lookup escaped membership transaction"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(name) })
	if err := newUserService(l).AssignRoles(user.ID, []int64{newRole.ID}, actor); err != nil {
		t.Fatalf("transactional role assignment failed: %v", err)
	}
	if reads != 1 || !reflect.DeepEqual(employeeMutationRoleIDs(t, db, user.ID), []int64{newRole.ID}) {
		t.Fatal("role lookup did not validate new committed membership")
	}
}
