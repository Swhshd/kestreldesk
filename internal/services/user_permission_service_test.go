package services

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"gorm.io/gorm"
)

func TestEmployeeUserPermissionCRUD(t *testing.T) {
	db := lifecycleAuthTestDB(t)
	first := createAuthTestUser(t, db, "override-first", "secret")
	second := createAuthTestUser(t, db, "override-second", "secret")
	permission := models.Permission{Code: "override.test", Name: "override", Status: enums.StatusOk}
	if err := db.Create(&permission).Error; err != nil {
		t.Fatal(err)
	}
	spy := &employeeRealtimeInvalidationSpy{}
	svc := newUserPermissionService(spy)
	expiry := time.Now().Add(time.Hour)
	row := &models.UserPermission{UserID: first.ID, PermissionID: permission.ID, Effect: -1, ExpiredAt: &expiry}
	if err := svc.Create(row); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spy.ids, []int64{first.ID}) {
		t.Fatalf("create invalidations = %v", spy.ids)
	}
	spy.ids = nil
	row.UserID = second.ID
	if err := svc.Update(row); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spy.ids, []int64{first.ID, second.ID}) {
		t.Fatalf("update invalidations = %v", spy.ids)
	}
	spy.ids = nil
	if err := svc.Updates(row.ID, map[string]any{"user_id": first.ID}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spy.ids, []int64{first.ID, second.ID}) {
		t.Fatalf("updates invalidations = %v", spy.ids)
	}
	spy.ids = nil
	if err := svc.UpdateColumn(row.ID, "user_id", second.ID); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spy.ids, []int64{first.ID, second.ID}) {
		t.Fatalf("update-column invalidations = %v", spy.ids)
	}
	spy.ids = nil
	if err := svc.Delete(row.ID); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spy.ids, []int64{second.ID}) {
		t.Fatalf("delete invalidations = %v", spy.ids)
	}
}

func TestEmployeeUserPermissionWriteAndDeleteErrors(t *testing.T) {
	db := lifecycleAuthTestDB(t)
	user := createAuthTestUser(t, db, "override-errors", "secret")
	permission := models.Permission{Code: "override.errors", Name: "override", Status: enums.StatusOk}
	if err := db.Create(&permission).Error; err != nil {
		t.Fatal(err)
	}
	row := models.UserPermission{UserID: user.ID, PermissionID: permission.ID, Effect: 1}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	spy := &employeeRealtimeInvalidationSpy{}
	svc := newUserPermissionService(spy)

	readFailure := errors.New("injected override read failure")
	const readName = "test:user_permission_pre_read_failure"
	if err := db.Callback().Query().Before("gorm:query").Register(readName, func(tx *gorm.DB) {
		if tx.Statement.Table == "t_user_permission" {
			tx.AddError(readFailure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Updates(row.ID, map[string]any{"effect": -1}); !errors.Is(err, readFailure) {
		t.Fatalf("pre-read error = %v", err)
	}
	db.Callback().Query().Remove(readName)
	if len(spy.ids) != 0 || spy.allCalls != 0 {
		t.Fatal("failed pre-read invalidated employees")
	}
	var persisted models.UserPermission
	if err := db.First(&persisted, row.ID).Error; err != nil || persisted.Effect != 1 {
		t.Fatal("failed pre-read still wrote override")
	}
	updateFailure := errors.New("injected override update failure")
	const updateName = "test:user_permission_update_failure"
	if err := db.Callback().Update().Before("gorm:update").Register(updateName, func(tx *gorm.DB) {
		if tx.Statement.Table == "t_user_permission" {
			tx.AddError(updateFailure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Updates(row.ID, map[string]any{"effect": -1}); !errors.Is(err, updateFailure) {
		t.Fatalf("update error = %v", err)
	}
	db.Callback().Update().Remove(updateName)
	if len(spy.ids) != 0 || spy.allCalls != 0 {
		t.Fatal("failed update invalidated employees")
	}
	if err := db.First(&persisted, row.ID).Error; err != nil || persisted.Effect != 1 {
		t.Fatal("failed update persisted override")
	}

	writeFailure := errors.New("injected override delete failure")
	const deleteName = "test:user_permission_delete_failure"
	if err := db.Callback().Delete().Before("gorm:delete").Register(deleteName, func(tx *gorm.DB) {
		if tx.Statement.Table == "t_user_permission" {
			tx.AddError(writeFailure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(row.ID); !errors.Is(err, writeFailure) {
		t.Fatalf("delete error = %v", err)
	}
	db.Callback().Delete().Remove(deleteName)
	if len(spy.ids) != 0 || spy.allCalls != 0 {
		t.Fatal("failed delete invalidated employees")
	}
	if err := db.First(&persisted, row.ID).Error; err != nil {
		t.Fatal("failed delete removed override")
	}
}

func TestEmployeeUserPermissionPostWriteReloadFailureFailsClosed(t *testing.T) {
	db := lifecycleAuthTestDB(t)
	user := createAuthTestUser(t, db, "override-reload", "secret")
	permission := models.Permission{Code: "override.reload", Name: "override", Status: enums.StatusOk}
	if err := db.Create(&permission).Error; err != nil {
		t.Fatal(err)
	}
	row := models.UserPermission{UserID: user.ID, PermissionID: permission.ID, Effect: 1}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	spy := &employeeRealtimeInvalidationSpy{}
	svc := newUserPermissionService(spy)
	failure := errors.New("injected post-write reload failure")
	queries := 0
	const name = "test:user_permission_post_write_reload_failure"
	if err := db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "t_user_permission" {
			queries++
			if queries == 2 {
				tx.AddError(failure)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Updates(row.ID, map[string]any{"effect": -1}); err != nil {
		t.Fatalf("committed write should remain successful: %v", err)
	}
	db.Callback().Query().Remove(name)
	if queries != 2 || spy.allCalls != 1 || len(spy.ids) != 0 {
		t.Fatalf("post-write failure was not fail-closed: queries=%d ids=%v all=%d", queries, spy.ids, spy.allCalls)
	}
	if err := db.First(&row, row.ID).Error; err != nil || row.Effect != -1 {
		t.Fatal("post-write reload failure rolled back committed update")
	}
}
