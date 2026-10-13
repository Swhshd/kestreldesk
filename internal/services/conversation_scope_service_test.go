package services_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/Swhshd/kestreldesk/internal/services"
	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func scopeFixture(t *testing.T) (*gorm.DB, *dto.AuthPrincipal) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "scope.db")), &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "t_", SingularTable: true}, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw, _ := db.DB(); _ = raw.Close() })
	if err := db.AutoMigrate(&models.User{}, &models.AgentProfile{}, &models.AgentTeam{}, &models.Conversation{}); err != nil {
		t.Fatal(err)
	}
	for _, teamID := range []int64{10, 20} {
		if err := db.Create(&models.AgentTeam{ID: teamID, Name: fmt.Sprint("synthetic-team-", teamID), Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ user, team int64 }{{101, 10}, {102, 10}, {201, 20}} {
		if err := db.Create(&models.User{ID: item.user, Username: fmt.Sprint("synthetic-employee-", item.user), UserType: enums.UserTypeEmployee, Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.AgentProfile{UserID: item.user, TeamID: item.team, AgentCode: fmt.Sprint("synthetic-", item.user), Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
	}
	conversations := []models.Conversation{
		{ID: 1, Status: enums.IMConversationStatusActive, CurrentAssigneeID: 101, CurrentTeamID: 10},
		{ID: 2, Status: enums.IMConversationStatusClosed, CurrentAssigneeID: 101, CurrentTeamID: 10},
		{ID: 3, Status: enums.IMConversationStatusActive, CurrentAssigneeID: 102, CurrentTeamID: 10},
		{ID: 4, Status: enums.IMConversationStatusPending, CurrentTeamID: 10},
		{ID: 5, Status: enums.IMConversationStatusPending, CurrentTeamID: 20},
		{ID: 6, Status: enums.IMConversationStatusAIServing, CurrentTeamID: 10},
		{ID: 7, Status: enums.IMConversationStatusActive, CurrentAssigneeID: 102, CurrentTeamID: 10},
		{ID: 8, Status: enums.IMConversationStatusPending, CurrentTeamID: 10, ServiceMode: enums.IMConversationServiceModeAIOnly},
		{ID: 9, Status: enums.IMConversationStatusPending},
	}
	if err := db.Create(&conversations).Error; err != nil {
		t.Fatal(err)
	}
	return db, &dto.AuthPrincipal{UserID: 101, UserType: enums.UserTypeEmployee, Status: enums.StatusOk, Roles: []string{constants.RoleCodeCsUser}, Permissions: []string{constants.PermissionConversationView.Code}}
}

func TestConversationScopeAgentReadAndActiveWriteMatrix(t *testing.T) {
	db, principal := scopeFixture(t)
	for _, item := range []struct {
		id                 int64
		read, write, claim bool
	}{{1, true, true, false}, {2, true, false, false}, {3, false, false, false}, {4, true, false, true}, {5, false, false, false}, {6, false, false, false}, {7, false, false, false}, {8, false, false, false}, {9, false, false, false}, {999, false, false, false}} {
		for _, check := range []struct {
			action services.ConversationScopeAction
			want   bool
		}{{services.ConversationScopeRead, item.read}, {services.ConversationScopeActiveWrite, item.write}, {services.ConversationScopeClaim, item.claim}} {
			t.Run(fmt.Sprintf("%d/%d", item.id, check.action), func(t *testing.T) {
				_, err := services.ConversationScopeService.Authorize(db, principal, item.id, check.action)
				if (err == nil) != check.want {
					t.Fatalf("allowed=%v, want=%v, err=%v", err == nil, check.want, err)
				}
				if err != nil {
					var denied *errorsx.I18nError
					if !errors.As(err, &denied) || denied.Code != errorsx.CodeAuthForbidden || denied.Key != "error.e0222" {
						t.Fatalf("expected stable localized forbidden, got %v", err)
					}
				}
			})
		}
	}
}

func TestConversationScopeAuthorizeForUpdateRechecksCurrentConversation(t *testing.T) {
	db, principal := scopeFixture(t)
	if err := db.Model(&models.Conversation{}).Where("id = ?", 1).UpdateColumns(map[string]any{"current_assignee_id": 102}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := services.ConversationScopeService.AuthorizeForUpdate(db, principal, 1, services.ConversationScopeActiveWrite); err == nil {
		t.Fatal("stale employee scope authorized a conversation after reassignment")
	}
}

func TestConversationForUpdateEmitsMySQLRowLock(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "scope_lock_dry_run@tcp(127.0.0.1:1)/scope?parseTime=true", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	var statement string
	callback := "test:capture_conversation_for_update_sql"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "conversations" {
			statement = tx.Statement.SQL.String()
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	if _, err := repositories.EmployeeConversationScopeRepository.ConversationForUpdate(db, 1); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToUpper(statement), "FOR UPDATE") {
		t.Fatalf("MySQL conversation lock query missing FOR UPDATE: %q", statement)
	}
}

func TestEmployeeConversationScopeRepositoryReadsRetainRequestedRowLocks(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: "scope_lock_dry_run@tcp(127.0.0.1:1)/scope?parseTime=true", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	var statements []string
	callback := "test:capture_employee_scope_lock_sql"
	if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		statements = append(statements, tx.Statement.SQL.String())
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })
	lockingDB := func() *gorm.DB { return db.Clauses(clause.Locking{Strength: "UPDATE"}) }
	assertLastQueryLocked := func(label string, query func() error) {
		t.Helper()
		before := len(statements)
		if err := query(); err != nil {
			t.Fatalf("%s query: %v", label, err)
		}
		if len(statements) != before+1 {
			t.Fatalf("%s query count changed: before=%d after=%d", label, before, len(statements))
		}
		statement := statements[len(statements)-1]
		t.Logf("%s SQL: %s", label, statement)
		if !strings.Contains(strings.ToUpper(statement), "FOR UPDATE") {
			t.Errorf("%s SQL missing FOR UPDATE: %q", label, statement)
		}
	}
	assertLastQueryLocked("direct user", func() error {
		var item models.User
		return lockingDB().First(&item, "id = ?", 101).Error
	})
	assertLastQueryLocked("repository user", func() error {
		_, err := repositories.EmployeeConversationScopeRepository.User(lockingDB(), 101)
		return err
	})
	assertLastQueryLocked("direct profile", func() error {
		var item models.AgentProfile
		return lockingDB().Take(&item, "user_id = ?", 101).Error
	})
	assertLastQueryLocked("repository profile", func() error {
		_, err := repositories.EmployeeConversationScopeRepository.Profile(lockingDB(), 101)
		return err
	})
	assertLastQueryLocked("direct team", func() error {
		var item models.AgentTeam
		return lockingDB().First(&item, "id = ?", 10).Error
	})
	assertLastQueryLocked("repository team", func() error {
		_, err := repositories.EmployeeConversationScopeRepository.Team(lockingDB(), 10)
		return err
	})
}

func TestConversationScopeCloseAllowsLegacyStatesOnlyForGlobalAdmin(t *testing.T) {
	for _, item := range []struct {
		role string
		id   int64
		want bool
	}{
		{constants.RoleCodeSuperAdmin, 1, true},
		{constants.RoleCodeSuperAdmin, 4, true},
		{constants.RoleCodeSuperAdmin, 6, true},
		{constants.RoleCodeSuperAdmin, 2, false},
		{constants.RoleCodeCsUser, 1, true},
		{constants.RoleCodeCsUser, 4, false},
		{constants.RoleCodeCsUser, 3, false},
		{constants.RoleCodeCsTeamLeader, 3, true},
		{constants.RoleCodeCsTeamLeader, 4, false},
		{constants.RoleCodeCsTeamLeader, 5, false},
	} {
		t.Run(fmt.Sprintf("%s/conversation-%d", item.role, item.id), func(t *testing.T) {
			db, principal := scopeFixture(t)
			principal.Roles = []string{item.role}
			_, err := services.ConversationScopeService.AuthorizeForUpdate(db, principal, item.id, services.ConversationScopeClose)
			if (err == nil) != item.want {
				t.Fatalf("close allowed=%v want=%v err=%v", err == nil, item.want, err)
			}
		})
	}
}

func TestConversationScopeRolesAndListMatch(t *testing.T) {
	for _, item := range []struct {
		role string
		ids  []int64
	}{{constants.RoleCodeCsUser, []int64{1, 2, 4}}, {constants.RoleCodeAdmin, []int64{1, 2, 4}}, {constants.RoleCodeCsTeamLeader, []int64{1, 2, 3, 4, 6, 7, 8}}, {constants.RoleCodeSuperAdmin, []int64{1, 2, 3, 4, 5, 6, 7, 8, 9}}} {
		t.Run(item.role, func(t *testing.T) {
			db, p := scopeFixture(t)
			p.Roles = []string{item.role}
			scope, err := services.ConversationScopeService.Resolve(db, p)
			if err != nil {
				t.Fatal(err)
			}
			cnd := scope.Restrict(sqls.NewCnd().Asc("id"))
			rows := repositories.ConversationRepository.Find(db, cnd)
			if len(rows) != len(item.ids) {
				t.Fatalf("list count=%d, want=%d", len(rows), len(item.ids))
			}
			for i, row := range rows {
				if row.ID != item.ids[i] {
					t.Fatalf("list ID=%d, want=%d", row.ID, item.ids[i])
				}
				if !scope.Allows(&row, services.ConversationScopeRead) {
					t.Fatal("list and detail diverged")
				}
			}
			_, err = services.ConversationScopeService.Authorize(db, p, 3, services.ConversationScopeActiveWrite)
			if (err == nil) != (item.role == constants.RoleCodeCsTeamLeader || item.role == constants.RoleCodeSuperAdmin) {
				t.Fatalf("role active write: %v", err)
			}
		})
	}
}

func TestConversationScopeInvalidIdentityFailsClosed(t *testing.T) {
	for _, kind := range []string{"nil", "zero-user", "customer-principal", "disabled-principal", "missing-user", "disabled-user", "customer-user", "missing-profile", "disabled-profile", "zero-team", "missing-team", "disabled-team", "db-error"} {
		t.Run(kind, func(t *testing.T) {
			db, p := scopeFixture(t)
			var err error
			switch kind {
			case "nil":
				p = nil
			case "zero-user":
				p.UserID = 0
			case "customer-principal":
				p.UserType = enums.UserTypeUser
			case "disabled-principal":
				p.Status = enums.StatusDisabled
			case "missing-user":
				err = db.Delete(&models.User{}, 101).Error
			case "disabled-user":
				err = db.Model(&models.User{}).Where("id = ?", 101).Update("status", enums.StatusDisabled).Error
			case "customer-user":
				err = db.Model(&models.User{}).Where("id = ?", 101).Update("user_type", enums.UserTypeUser).Error
			case "missing-profile":
				err = db.Where("user_id = ?", 101).Delete(&models.AgentProfile{}).Error
			case "disabled-profile":
				err = db.Model(&models.AgentProfile{}).Where("user_id = ?", 101).Update("status", enums.StatusDisabled).Error
			case "zero-team":
				err = db.Model(&models.AgentProfile{}).Where("user_id = ?", 101).Update("team_id", 0).Error
			case "missing-team":
				err = db.Delete(&models.AgentTeam{}, 10).Error
			case "disabled-team":
				err = db.Model(&models.AgentTeam{}).Where("id = ?", 10).Update("status", enums.StatusDisabled).Error
			case "db-error":
				raw, _ := db.DB()
				err = raw.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := services.ConversationScopeService.Resolve(db, p); err == nil {
				t.Fatal("invalid identity authorized")
			}
			if _, err := services.ConversationScopeService.Authorize(db, p, 1, services.ConversationScopeRead); err == nil {
				t.Fatal("invalid identity read authorized")
			}
		})
	}
}

func TestConversationScopeFreshAssignmentAndTeamResolution(t *testing.T) {
	db, p := scopeFixture(t)
	if _, err := services.ConversationScopeService.Authorize(db, p, 1, services.ConversationScopeRead); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Conversation{}).Where("id = ?", 1).Update("current_assignee_id", 102).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := services.ConversationScopeService.Authorize(db, p, 1, services.ConversationScopeRead); err == nil {
		t.Fatal("past assignee retained access")
	}
	if err := db.Model(&models.AgentProfile{}).Where("user_id = ?", 101).Update("team_id", 20).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := services.ConversationScopeService.Authorize(db, p, 4, services.ConversationScopeRead); err == nil {
		t.Fatal("old team retained queue access")
	}
	if _, err := services.ConversationScopeService.Authorize(db, p, 5, services.ConversationScopeRead); err != nil {
		t.Fatal(err)
	}
}

func TestConversationScopeSuperAdminAndUnknownActions(t *testing.T) {
	db, p := scopeFixture(t)
	p.Roles = []string{constants.RoleCodeSuperAdmin}
	if err := db.Where("user_id = ?", 101).Delete(&models.AgentProfile{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := services.ConversationScopeService.Authorize(db, p, 5, services.ConversationScopeRead); err != nil {
		t.Fatal(err)
	}
	if _, err := services.ConversationScopeService.Authorize(db, p, 999, services.ConversationScopeRead); err == nil {
		t.Fatal("missing conversation allowed")
	}
	if _, err := services.ConversationScopeService.Authorize(db, p, 1, services.ConversationScopeAction(255)); err == nil {
		t.Fatal("unknown action allowed")
	}
	if _, err := services.ConversationScopeService.Resolve(nil, p); err == nil {
		t.Fatal("nil DB allowed")
	}
}

func TestConversationScopeRestrictionCannotBroadenCallerFilters(t *testing.T) {
	db, p := scopeFixture(t)
	scope, err := services.ConversationScopeService.Resolve(db, p)
	if err != nil {
		t.Fatal(err)
	}
	rows := repositories.ConversationRepository.Find(db, scope.Restrict(sqls.NewCnd().Eq("current_team_id", 20)))
	if len(rows) != 0 {
		t.Fatal("scope broadened caller team filter")
	}
	var missing *services.EmployeeConversationScope
	if missing.Allows(&models.Conversation{ID: 1, CurrentAssigneeID: 101}, services.ConversationScopeRead) {
		t.Fatal("nil scope authorized")
	}
	if len(repositories.ConversationRepository.Find(db, missing.Restrict(nil))) != 0 {
		t.Fatal("nil scope returned rows")
	}
	if err := db.Migrator().DropTable(&models.Conversation{}); err != nil {
		t.Fatal(err)
	}
	if _, err := services.ConversationScopeService.Authorize(db, p, 1, services.ConversationScopeRead); err == nil {
		t.Fatal("conversation lookup error authorized")
	}
}
