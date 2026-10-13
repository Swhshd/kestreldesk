package services

// This file has been modified for the KestrelDesk project.

import (
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestPermissionServiceSyncBuiltinPermissions(t *testing.T) {
	db := setupPermissionServiceTestDB(t)
	now := time.Now()
	for _, spec := range constants.Roles {
		if err := db.Create(&models.Role{
			Name: spec.Name, Code: spec.Code, Status: enums.StatusOk, IsSystem: true, SortNo: spec.SortNo,
			AuditFields: models.AuditFields{CreatedAt: now, UpdatedAt: now},
		}).Error; err != nil {
			t.Fatalf("create role %s: %v", spec.Code, err)
		}
	}
	customPermission := &models.Permission{
		Name: "Custom permission", Code: "custom.keep", Type: "api", GroupName: "custom",
		Status: enums.StatusOk, AuditFields: models.AuditFields{CreatedAt: now, UpdatedAt: now},
	}
	if err := db.Create(customPermission).Error; err != nil {
		t.Fatalf("create custom permission: %v", err)
	}
	superAdmin := &models.Role{}
	if err := db.First(superAdmin, "code = ?", constants.RoleCodeSuperAdmin).Error; err != nil {
		t.Fatalf("find super admin role: %v", err)
	}
	if err := db.Create(&models.RolePermission{
		RoleID: superAdmin.ID, PermissionID: customPermission.ID,
		AuditFields: models.AuditFields{CreatedAt: now, UpdatedAt: now},
	}).Error; err != nil {
		t.Fatalf("create custom role permission: %v", err)
	}

	first, err := PermissionService.SyncBuiltinPermissions()
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if first.Created != len(constants.Permissions) || first.Updated != 0 {
		t.Fatalf("unexpected first sync result: %+v", first)
	}

	wantRolePermissions := 0
	for _, permissions := range constants.RolePermissions {
		wantRolePermissions += len(permissions)
	}
	if first.RolePermissionsAdded != wantRolePermissions {
		t.Fatalf("role permissions added=%d want=%d", first.RolePermissionsAdded, wantRolePermissions)
	}

	second, err := PermissionService.SyncBuiltinPermissions()
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if second.Created != 0 || second.Updated != len(constants.Permissions) || second.RolePermissionsAdded != 0 {
		t.Fatalf("sync is not idempotent: %+v", second)
	}

	var permissionCount int64
	if err := db.Model(&models.Permission{}).Count(&permissionCount).Error; err != nil {
		t.Fatalf("count permissions: %v", err)
	}
	if permissionCount != int64(len(constants.Permissions)+1) {
		t.Fatalf("permission count=%d want=%d", permissionCount, len(constants.Permissions)+1)
	}
	var customRolePermissionCount int64
	if err := db.Model(&models.RolePermission{}).
		Where("role_id = ? AND permission_id = ?", superAdmin.ID, customPermission.ID).
		Count(&customRolePermissionCount).Error; err != nil {
		t.Fatalf("count custom role permission: %v", err)
	}
	if customRolePermissionCount != 1 {
		t.Fatalf("custom role permission was removed")
	}
}

func TestPermissionSyncRemovesOnlyObsoleteSystemCsUserTicketAssign(t *testing.T) {
	db := setupPermissionServiceTestDB(t)
	now := time.Now()
	for _, spec := range constants.Roles {
		if err := db.Create(&models.Role{
			Name: spec.Name, Code: spec.Code, Status: enums.StatusOk, IsSystem: true,
			AuditFields: models.AuditFields{CreatedAt: now, UpdatedAt: now},
		}).Error; err != nil {
			t.Fatalf("create role %s: %v", spec.Code, err)
		}
	}
	customRole := &models.Role{Name: "Custom Support", Code: "custom-support", Status: enums.StatusOk, IsSystem: false}
	if err := db.Create(customRole).Error; err != nil {
		t.Fatalf("create custom role: %v", err)
	}
	invalidator := &employeeRealtimeInvalidationSpy{}
	svc := newPermissionService(invalidator)
	if _, err := svc.SyncBuiltinPermissions(); err != nil {
		t.Fatalf("initial permission sync: %v", err)
	}

	var csUser, teamLeader, admin, superAdmin models.Role
	for _, item := range []*models.Role{&csUser, &teamLeader, &admin, &superAdmin} {
		code := map[*models.Role]string{
			&csUser: constants.RoleCodeCsUser, &teamLeader: constants.RoleCodeCsTeamLeader,
			&admin: constants.RoleCodeAdmin, &superAdmin: constants.RoleCodeSuperAdmin,
		}[item]
		if err := db.Where("code = ?", code).First(item).Error; err != nil {
			t.Fatalf("find role %s: %v", code, err)
		}
	}
	var assignPermission models.Permission
	if err := db.Where("code = ?", constants.PermissionTicketAssign.Code).First(&assignPermission).Error; err != nil {
		t.Fatalf("find ticket.assign: %v", err)
	}
	ensureGrant := func(roleID int64) {
		t.Helper()
		var count int64
		if err := db.Model(&models.RolePermission{}).Where("role_id = ? AND permission_id = ?", roleID, assignPermission.ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			if err := db.Create(&models.RolePermission{RoleID: roleID, PermissionID: assignPermission.ID}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	ensureGrant(csUser.ID) // Simulate a grant persisted by the older additive sync.
	ensureGrant(customRole.ID)
	user := &models.User{
		Username: "permission-sync-cs-agent", UserType: enums.UserTypeEmployee, Status: enums.StatusOk,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create employee for cache invalidation: %v", err)
	}
	if err := db.Create(&models.UserRole{UserID: user.ID, RoleID: csUser.ID}).Error; err != nil {
		t.Fatalf("create role membership for invalidation: %v", err)
	}
	invalidator.ids = nil
	invalidator.employeeCalls = 0

	result, err := svc.SyncBuiltinPermissions()
	if err != nil {
		t.Fatalf("reconcile obsolete grant: %v", err)
	}
	if result.RolePermissionsRemoved != 1 {
		t.Fatalf("removed role permissions=%d, want 1", result.RolePermissionsRemoved)
	}
	if invalidator.employeeCalls != 1 || len(invalidator.ids) != 1 || invalidator.ids[0] != user.ID {
		t.Fatalf("revoked grant did not invalidate its employee session: %+v", invalidator)
	}
	grantCount := func(roleID int64) int64 {
		t.Helper()
		var count int64
		if err := db.Model(&models.RolePermission{}).Where("role_id = ? AND permission_id = ?", roleID, assignPermission.ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		return count
	}
	if grantCount(csUser.ID) != 0 {
		t.Fatal("obsolete ticket.assign remains on system cs_user")
	}
	if grantCount(customRole.ID) != 1 {
		t.Fatal("sync modified a custom role's ticket.assign grant")
	}
	for name, role := range map[string]models.Role{"team leader": teamLeader, "admin": admin, "super admin": superAdmin} {
		if grantCount(role.ID) != 1 {
			t.Errorf("sync removed ticket.assign from %s", name)
		}
	}
}

func TestPermissionSyncPreservesTicketAssignOnNonSystemCsUserRole(t *testing.T) {
	db := setupPermissionServiceTestDB(t)
	for _, spec := range constants.Roles {
		if err := db.Create(&models.Role{
			Name: spec.Name, Code: spec.Code, Status: enums.StatusOk,
			IsSystem: spec.Code != constants.RoleCodeCsUser,
		}).Error; err != nil {
			t.Fatalf("create role %s: %v", spec.Code, err)
		}
	}
	svc := newPermissionService(&employeeRealtimeInvalidationSpy{})
	if _, err := svc.SyncBuiltinPermissions(); err != nil {
		t.Fatalf("initial permission sync: %v", err)
	}
	var role models.Role
	if err := db.Where("code = ?", constants.RoleCodeCsUser).First(&role).Error; err != nil {
		t.Fatal(err)
	}
	var permission models.Permission
	if err := db.Where("code = ?", constants.PermissionTicketAssign.Code).First(&permission).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.RolePermission{RoleID: role.ID, PermissionID: permission.ID}).Error; err != nil {
		t.Fatal(err)
	}
	result, err := svc.SyncBuiltinPermissions()
	if err != nil {
		t.Fatalf("sync non-system role: %v", err)
	}
	if result.RolePermissionsRemoved != 0 {
		t.Fatalf("removed grants from non-system role: %+v", result)
	}
	var count int64
	if err := db.Model(&models.RolePermission{}).Where("role_id = ? AND permission_id = ?", role.ID, permission.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("sync removed ticket.assign from a non-system cs_user role")
	}
}

func setupPermissionServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{TablePrefix: "t_", SingularTable: true},
	})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	if err := db.AutoMigrate(&models.Role{}, &models.Permission{}, &models.RolePermission{}, &models.User{}, &models.UserRole{}); err != nil {
		t.Fatalf("migrate permission tables: %v", err)
	}
	sqls.SetDB(db)
	return db
}
