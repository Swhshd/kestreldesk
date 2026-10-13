package services

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"

	"gorm.io/gorm"
)

func seedEmployeeSnapshotSession(t *testing.T, db *gorm.DB, userID int64, now time.Time) models.LoginSession {
	t.Helper()
	last := now.Add(-time.Hour)
	row := models.LoginSession{UserID: userID, Token: "synthetic-snapshot", ExpiredAt: now.Add(time.Hour), LastSeenAt: &last}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func TestEmployeeAuthSnapshotCurrentState(t *testing.T) {
	for _, name := range []string{"valid", "missing session", "revoked session", "expired session", "equal session", "missing employee", "disabled employee", "deleted employee", "session query error", "employee query error", "role query error", "permission query error", "override query error"} {
		t.Run(name, func(t *testing.T) {
			db := setupAuthServiceTestDB(t)
			grant := createRBACTestGrant(t, db, "conversation.view", enums.StatusOk, enums.StatusOk, true, nil, nil)
			now := time.Now()
			row := seedEmployeeSnapshotSession(t, db, grant.UserID, now)
			var err error
			switch name {
			case "missing session":
				err = db.Delete(&row).Error
			case "revoked session":
				err = db.Model(&row).Update("revoked_at", now).Error
			case "expired session":
				err = db.Model(&row).Update("expired_at", now.Add(-time.Second)).Error
			case "equal session":
				err = db.Model(&row).Update("expired_at", now).Error
			case "missing employee":
				err = db.Delete(&models.User{}, grant.UserID).Error
			case "disabled employee":
				err = db.Model(&models.User{}).Where("id = ?", grant.UserID).Update("status", enums.StatusDisabled).Error
			case "deleted employee":
				err = db.Model(&models.User{}).Where("id = ?", grant.UserID).Update("deleted_at", now).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			queryTables := map[string]string{"session query error": "t_login_session", "employee query error": "t_user", "role query error": "t_role AS r", "permission query error": "t_permission AS p", "override query error": "t_user_permission AS up"}
			injected := errors.New("synthetic read failure")
			if table, ok := queryTables[name]; ok {
				if err := db.Callback().Query().Before("gorm:query").Register("snapshot-query-error", func(tx *gorm.DB) {
					if tx.Statement.Table == table || (tx.Statement.TableExpr != nil && tx.Statement.TableExpr.SQL == table) {
						tx.AddError(injected)
					}
				}); err != nil {
					t.Fatal(err)
				}
				if err := db.Callback().Row().Before("gorm:row").Register("snapshot-row-error", func(tx *gorm.DB) {
					if tx.Statement.Table == table || (tx.Statement.TableExpr != nil && tx.Statement.TableExpr.SQL == table) {
						tx.AddError(injected)
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := (employeeAuthStateReader{}).ReadEmployeeSession(row.ID, now)
			if name != "valid" {
				if err == nil {
					t.Fatal("invalid current state accepted")
				}
				if _, ok := queryTables[name]; ok && !errors.Is(err, injected) {
					t.Fatalf("query failure lost: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.EmployeeID != grant.UserID || got.LoginSessionID != row.ID || !got.LoginSessionExpiresAt.Equal(row.ExpiredAt) || got.Principal.UserID != grant.UserID || !slices.Equal(got.Principal.Permissions, []string{"conversation.view"}) || len(got.Principal.Roles) != 1 {
				t.Fatal("wrong authoritative snapshot")
			}
			if err := db.Model(&models.Role{}).Where("id = ?", grant.RoleID).Update("status", enums.StatusDisabled).Error; err != nil {
				t.Fatal(err)
			}
			got, err = (employeeAuthStateReader{}).ReadEmployeeSession(row.ID, now)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Principal.Roles) != 0 || len(got.Principal.Permissions) != 0 {
				t.Fatal("stale authorization snapshot")
			}
		})
	}
}

func TestEmployeeAuthOverrideReduction(t *testing.T) {
	now := time.Now()
	past, equal, future := now.Add(-time.Hour), now, now.Add(time.Hour)
	allow, deny, zero := 1, -1, 0
	for _, tc := range []struct {
		name   string
		effect int
		expiry *time.Time
		status enums.Status
		role   bool
		want   []string
	}{
		{"allow", allow, nil, enums.StatusOk, false, []string{"conversation.view"}},
		{"deny", deny, nil, enums.StatusOk, true, []string{}},
		{"zero", zero, nil, enums.StatusOk, false, []string{"conversation.view"}},
		{"past", allow, &past, enums.StatusOk, false, []string{}},
		{"equal", allow, &equal, enums.StatusOk, false, []string{}},
		{"future", allow, &future, enums.StatusOk, false, []string{"conversation.view"}},
		{"disabled", allow, &future, enums.StatusDisabled, true, []string{}},
		{"redundant", allow, &future, enums.StatusOk, true, []string{"conversation.view"}},
		{"expired deny", deny, &past, enums.StatusOk, true, []string{"conversation.view"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := setupAuthServiceTestDB(t)
			grant := createRBACTestGrant(t, db, "conversation.view", enums.StatusOk, tc.status, tc.role, &tc.effect, tc.expiry)
			row := seedEmployeeSnapshotSession(t, db, grant.UserID, now)
			got, err := (employeeAuthStateReader{}).ReadEmployeeSession(row.ID, now)
			if err != nil {
				t.Fatal(err)
			}
			wantCodes := tc.want
			if !slices.Equal(got.Principal.Permissions, wantCodes) {
				t.Fatal("effect/status/expiry semantics changed")
			}
		})
	}
}

func TestEmployeeAuthOverrideDeadline(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	now := time.Now()
	earliestEnabledFutureExpiry := now.Add(time.Minute)
	later := now.Add(time.Hour)
	past := now.Add(-time.Minute)
	equal := now
	disabled := now.Add(time.Second)
	allow := 1
	grant := createRBACTestGrant(t, db, "redundant", enums.StatusOk, enums.StatusOk, true, &allow, &earliestEnabledFutureExpiry)
	for _, tc := range []struct {
		code   string
		effect int
		expiry *time.Time
		status enums.Status
	}{{"deny", -1, &later, enums.StatusOk},
		{"zero", 0, &later, enums.StatusOk},
		{"nil", 1, nil, enums.StatusOk},
		{"past", 1, &past, enums.StatusOk},
		{"equal", 1, &equal, enums.StatusOk},
		{"disabled", 1, &disabled, enums.StatusDisabled},
	} {
		p := models.Permission{Code: tc.code, Status: tc.status}
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
		up := models.UserPermission{UserID: grant.UserID, PermissionID: p.ID, Effect: tc.effect, ExpiredAt: tc.expiry}
		if err := db.Create(&up).Error; err != nil {
			t.Fatal(err)
		}
		if tc.effect == 0 {
			if err := db.Model(&up).Update("effect", 0).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	row := seedEmployeeSnapshotSession(t, db, grant.UserID, now)
	got, err := (employeeAuthStateReader{}).ReadEmployeeSession(row.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.NextAuthzChangeAt == nil || !got.NextAuthzChangeAt.Equal(earliestEnabledFutureExpiry) {
		t.Fatal("wrong next authorization deadline")
	}
	if err := db.Model(&models.UserPermission{}).Where("user_id = ? AND permission_id = ?", grant.UserID, grant.PermissionID).Update("expired_at", nil).Error; err != nil {
		t.Fatal(err)
	}
	got, err = (employeeAuthStateReader{}).ReadEmployeeSession(row.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.NextAuthzChangeAt == nil || !got.NextAuthzChangeAt.Equal(later) {
		t.Fatal("deny/zero deadline missing")
	}
}

func TestEmployeeAuthRevalidationDoesNotTouchLastSeen(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	grant := createRBACTestGrant(t, db, "conversation.view", enums.StatusOk, enums.StatusOk, true, nil, nil)
	now := time.Now()
	before := seedEmployeeSnapshotSession(t, db, grant.UserID, now)
	if err := db.First(&before, before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := (employeeAuthStateReader{}).ReadEmployeeSession(before.ID, now); err != nil {
		t.Fatal(err)
	}
	var after models.LoginSession
	if err := db.First(&after, before.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.LastSeenAt, after.LastSeenAt) {
		t.Fatal("revalidation wrote last-seen state")
	}
	if !before.UpdatedAt.Equal(after.UpdatedAt) {
		t.Fatal("revalidation wrote audit state")
	}
}

func TestEmployeeAuthMetadataHasNoTokenOrClientIdentity(t *testing.T) {
	for _, value := range []any{EmployeeSessionSnapshot{}, authenticatedEmployeeSession{}} {
		typ := reflect.TypeOf(value)
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			if strings.Contains(name, "token") || strings.Contains(name, "client") {
				t.Fatal("untrusted or confidential field in auth metadata")
			}
		}
	}
}
