package migration

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/mlogclub/simple/sqls"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func withBootstrapAdminTestConfig(t *testing.T, password string) {
	t.Helper()
	previous := config.GetCurrent()
	config.SetCurrent(&config.Config{Auth: config.AuthConfig{BootstrapAdminPassword: password}})
	t.Cleanup(func() { config.SetCurrent(previous) })
}

func withBootstrapOnlyMigrationRegistry(t *testing.T) {
	t.Helper()

	previousVersions := versions
	previousFuncs := migrationFuncs
	previousMigrations := migrations
	bootstrapMigration, ok := migrationFuncs[2]
	if !ok {
		t.Fatal("bootstrap migration 2 is not registered")
	}
	versions = []int64{2}
	migrationFuncs = map[int64]MigrationFunc{2: bootstrapMigration}
	migrations = make(map[int64]models.Migration)
	t.Cleanup(func() {
		versions = previousVersions
		migrationFuncs = previousFuncs
		migrations = previousMigrations
	})
}

func withBootstrapAdminMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupBootstrapAdminTestDB(t)
	previous := sqls.DB()
	sqls.SetDB(db)
	t.Cleanup(func() { sqls.SetDB(previous) })
	return db
}

func newBootstrapAdminMigrationTestValue(t *testing.T) string {
	t.Helper()
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		t.Fatalf("generating test input failed: %v", err)
	}
	return "test-bootstrap-" + hex.EncodeToString(random)
}

func TestBootstrapAdminCreationFailsClosedWithoutCredential(t *testing.T) {
	db := withBootstrapAdminMigrationDB(t)
	withBootstrapAdminTestConfig(t, "")
	withBootstrapOnlyMigrationRegistry(t)

	err := Migrate()
	if !errors.Is(err, errBootstrapAdminCredentialRequired) {
		t.Fatalf("Migrate() error did not identify the missing bootstrap configuration: %v", err)
	}

	for name, model := range map[string]any{
		"user":            &models.User{},
		"role":            &models.Role{},
		"permission":      &models.Permission{},
		"user role":       &models.UserRole{},
		"role permission": &models.RolePermission{},
	} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil {
			t.Fatalf("count %s rows: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("failed bootstrap migration left %d %s rows", count, name)
		}
	}
	var history models.Migration
	if err := db.Where("version = ?", 2).First(&history).Error; err != nil {
		t.Fatalf("load failed migration history: %v", err)
	}
	if history.Success {
		t.Fatal("missing-credential migration was recorded as successful")
	}
}

func TestBootstrapAdminCreatedFromConfiguredCredential(t *testing.T) {
	db := withBootstrapAdminMigrationDB(t)
	password := newBootstrapAdminMigrationTestValue(t)
	withBootstrapAdminTestConfig(t, password)

	if err := migrationFuncs[2].Fn(); err != nil {
		t.Fatalf("bootstrap migration failed: %v", err)
	}

	var user models.User
	if err := db.Where("username = ?", constants.BootstrapAdminUsername).First(&user).Error; err != nil {
		t.Fatalf("load bootstrap admin: %v", err)
	}
	if user.Status != enums.StatusOk || user.UserType != enums.UserTypeEmployee {
		t.Fatal("bootstrap admin was not created as an active employee")
	}
	if user.Password == password {
		t.Fatal("bootstrap credential was stored without hashing")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		t.Fatal("stored bootstrap password hash did not verify")
	}
	var role models.Role
	if err := db.Where("code = ?", constants.RoleCodeSuperAdmin).First(&role).Error; err != nil {
		t.Fatalf("load super-admin role: %v", err)
	}
	var assignments int64
	if err := db.Model(&models.UserRole{}).Where("user_id = ? AND role_id = ?", user.ID, role.ID).Count(&assignments).Error; err != nil {
		t.Fatalf("count role assignments: %v", err)
	}
	if assignments != 1 {
		t.Fatalf("super-admin role assignments = %d, want 1", assignments)
	}
}

func TestBootstrapAdminExistingWithoutCredentialPreservesHashAndReconciles(t *testing.T) {
	db := withBootstrapAdminMigrationDB(t)
	password := newBootstrapAdminMigrationTestValue(t)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("generate existing admin test hash: %v", err)
	}
	admin := models.User{
		Username: constants.BootstrapAdminUsername,
		Nickname: "Existing administrator",
		Password: string(hash),
		UserType: enums.UserTypeEmployee,
		Status:   enums.StatusDisabled,
		Remark:   "existing profile",
	}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create existing admin: %v", err)
	}
	session := models.LoginSession{
		UserID:     admin.ID,
		Token:      "test-bootstrap-session",
		ClientType: constants.ClientTypeAdminWeb,
		ExpiredAt:  time.Now().Add(time.Hour),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create active admin session: %v", err)
	}
	withBootstrapAdminTestConfig(t, "")

	if err := migrationFuncs[2].Fn(); err != nil {
		t.Fatalf("bootstrap migration failed for existing admin: %v", err)
	}
	var got models.User
	if err := db.First(&got, admin.ID).Error; err != nil {
		t.Fatalf("reload admin: %v", err)
	}
	if got.Password != admin.Password {
		t.Fatal("bootstrap reconciliation changed the existing password hash")
	}
	if got.Nickname != constants.BootstrapAdminNickname || got.Status != enums.StatusOk {
		t.Fatal("bootstrap profile/status reconciliation did not preserve existing behavior")
	}
	if got.UserType != admin.UserType || got.Remark != admin.Remark {
		t.Fatal("bootstrap reconciliation changed unrelated existing profile fields")
	}
	var role models.Role
	if err := db.Where("code = ?", constants.RoleCodeSuperAdmin).First(&role).Error; err != nil {
		t.Fatalf("load super-admin role: %v", err)
	}
	var assignments int64
	if err := db.Model(&models.UserRole{}).Where("user_id = ? AND role_id = ?", admin.ID, role.ID).Count(&assignments).Error; err != nil {
		t.Fatalf("count role assignments: %v", err)
	}
	if assignments != 1 {
		t.Fatalf("super-admin role assignments = %d, want 1", assignments)
	}
	var gotSession models.LoginSession
	if err := db.First(&gotSession, session.ID).Error; err != nil {
		t.Fatalf("reload admin session: %v", err)
	}
	if gotSession.RevokedAt != nil {
		t.Fatal("bootstrap reconciliation revoked the existing admin session")
	}
}

func TestBootstrapAdminExistingWithDifferentCredentialPreservesHash(t *testing.T) {
	db := withBootstrapAdminMigrationDB(t)
	existingPassword := newBootstrapAdminMigrationTestValue(t)
	configuredPassword := newBootstrapAdminMigrationTestValue(t)
	hash, err := bcrypt.GenerateFromPassword([]byte(existingPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("generate existing admin test hash: %v", err)
	}
	admin := models.User{Username: constants.BootstrapAdminUsername, Password: string(hash), UserType: enums.UserTypeEmployee, Status: enums.StatusOk}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("create existing admin: %v", err)
	}
	withBootstrapAdminTestConfig(t, configuredPassword)

	if err := migrationFuncs[2].Fn(); err != nil {
		t.Fatalf("bootstrap migration failed for existing admin: %v", err)
	}
	var got models.User
	if err := db.First(&got, admin.ID).Error; err != nil {
		t.Fatalf("reload admin: %v", err)
	}
	if got.Password != admin.Password {
		t.Fatal("configured bootstrap credential replaced the existing hash")
	}
}

func TestSuccessfulBootstrapMigrationSkipsWithoutCredential(t *testing.T) {
	db := withBootstrapAdminMigrationDB(t)
	withBootstrapAdminTestConfig(t, "")
	withBootstrapOnlyMigrationRegistry(t)
	history := models.Migration{Version: 2, Remark: "already initialized", Success: true}
	if err := db.Create(&history).Error; err != nil {
		t.Fatalf("create successful migration history: %v", err)
	}

	if err := Migrate(); err != nil {
		t.Fatalf("successful migration did not skip: %v", err)
	}
	var users int64
	if err := db.Model(&models.User{}).Count(&users).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 0 {
		t.Fatal("successful migration skip attempted bootstrap admin creation")
	}
	var got models.Migration
	if err := db.Where("version = ?", 2).First(&got).Error; err != nil {
		t.Fatalf("reload migration history: %v", err)
	}
	if !got.Success || got.RetryCount != 0 {
		t.Fatal("successful migration skip changed its history")
	}
}

func TestBootstrapMigrationRetriesAfterCredentialProvided(t *testing.T) {
	db := withBootstrapAdminMigrationDB(t)
	withBootstrapAdminTestConfig(t, "")
	withBootstrapOnlyMigrationRegistry(t)
	if err := Migrate(); !errors.Is(err, errBootstrapAdminCredentialRequired) {
		t.Fatal("first migration attempt did not fail for missing bootstrap configuration")
	}

	config.SetCurrent(&config.Config{Auth: config.AuthConfig{BootstrapAdminPassword: newBootstrapAdminMigrationTestValue(t)}})
	if err := Migrate(); err != nil {
		t.Fatalf("migration did not retry after configuration was supplied: %v", err)
	}
	var users int64
	if err := db.Model(&models.User{}).Where("username = ?", constants.BootstrapAdminUsername).Count(&users).Error; err != nil {
		t.Fatalf("count bootstrap admins: %v", err)
	}
	if users != 1 {
		t.Fatalf("bootstrap admins after retry = %d, want 1", users)
	}
	var history models.Migration
	if err := db.Where("version = ?", 2).First(&history).Error; err != nil {
		t.Fatalf("load migration history: %v", err)
	}
	if !history.Success || history.RetryCount != 2 {
		t.Fatal("retry did not record one failed and one successful attempt")
	}
}

func TestBootstrapCredentialDoesNotReachMigrationLogs(t *testing.T) {
	withBootstrapAdminMigrationDB(t)
	password := newBootstrapAdminMigrationTestValue(t)
	withBootstrapAdminTestConfig(t, password)
	withBootstrapOnlyMigrationRegistry(t)
	var output bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	err := Migrate()
	if err != nil && bytes.Contains([]byte(err.Error()), []byte(password)) {
		t.Fatal("bootstrap credential appeared in a migration error")
	}
	if bytes.Contains(output.Bytes(), []byte(password)) {
		t.Fatal("bootstrap credential appeared in migration logs")
	}
}
