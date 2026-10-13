package services

// This file has been modified for the KestrelDesk project.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"gorm.io/gorm"
)

func TestOIDCLoginAutoCreatesSystemUser(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	svc := newOIDCLoginService(&employeeRealtimeInvalidationSpy{})

	ret, err := svc.loginWithOIDCProfile(&oidcLoginProfile{
		Subject:           "sub-123",
		Email:             "ada@example.com",
		PreferredUsername: "ada",
		Name:              "Ada Lovelace",
		Picture:           "https://example.com/ada.png",
		RawProfile:        `{"sub":"sub-123"}`,
	}, config.AuthConfig{TokenTTLHours: 2}, "127.0.0.1", "go-test")
	if err != nil {
		t.Fatalf("loginWithOIDCProfile() error = %v", err)
	}
	if ret == nil || !strings.HasPrefix(ret.AccessToken, "ak_") {
		t.Fatalf("expected ak_ access token, got %+v", ret)
	}

	var user models.User
	if err := db.Take(&user, "username = ?", "ada").Error; err != nil {
		t.Fatalf("expected OIDC user to be created: %v", err)
	}
	if user.Nickname != "Ada Lovelace" || user.Avatar != "https://example.com/ada.png" {
		t.Fatalf("unexpected created user profile: %+v", user)
	}
	if user.Email == nil || *user.Email != "ada@example.com" {
		t.Fatalf("expected email to be stored, got %+v", user.Email)
	}
	if user.Password != "" {
		t.Fatalf("expected OIDC-created user password to be empty, got %q", user.Password)
	}

	var identity models.UserIdentity
	if err := db.Take(&identity, "provider = ? AND provider_user_id = ?", enums.ThirdProviderOIDC, "sub-123").Error; err != nil {
		t.Fatalf("expected OIDC identity to be created: %v", err)
	}
	if identity.UserID != user.ID || identity.ProviderName != "OIDC" || identity.Status != enums.StatusOk {
		t.Fatalf("unexpected OIDC identity: %+v", identity)
	}

	var sessions []models.LoginSession
	if err := db.Find(&sessions).Error; err != nil {
		t.Fatalf("query login sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].UserID != user.ID || sessions[0].Token != ret.AccessToken {
		t.Fatalf("unexpected login sessions: %+v", sessions)
	}
}

func TestEmployeeOIDCDefaultRoleCommitRollback(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	if err := db.Create(&models.Role{Code: constants.RoleCodeCsUser, Name: "Support Agent", Status: enums.StatusOk}).Error; err != nil {
		t.Fatal(err)
	}
	spy := &employeeRealtimeInvalidationSpy{}
	failure := errors.New("injected OIDC token persistence failure")
	const name = "test:oidc_session_create_failure"
	if err := db.Callback().Create().Before("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "t_login_session" {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Create().Remove(name)
	_, err := newOIDCLoginService(spy).loginWithOIDCProfile(&oidcLoginProfile{Subject: "oidc-rollback", PreferredUsername: "oidc-rollback", RawProfile: `{}`}, config.AuthConfig{TokenTTLHours: 1}, "127.0.0.1", "test")
	if !errors.Is(err, failure) {
		t.Fatalf("OIDC transaction error = %v", err)
	}
	var users, memberships int64
	if err := db.Model(&models.User{}).Count(&users).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.UserRole{}).Count(&memberships).Error; err != nil {
		t.Fatal(err)
	}
	if users != 0 || memberships != 0 || len(spy.ids) != 0 {
		t.Fatalf("OIDC rollback persisted or invalidated: users=%d memberships=%d invalidations=%v", users, memberships, spy.ids)
	}
}

func TestOIDCLoginReusesExistingIdentity(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	user := createAuthTestUser(t, db, "existing", "secret")
	if err := db.Create(&models.UserIdentity{
		UserID:         user.ID,
		Provider:       enums.ThirdProviderOIDC,
		ProviderUserID: "sub-123",
		ProviderName:   "OIDC",
		Status:         enums.StatusOk,
	}).Error; err != nil {
		t.Fatalf("seed OIDC identity: %v", err)
	}

	ret, err := newOIDCLoginService(&employeeRealtimeInvalidationSpy{}).loginWithOIDCProfile(&oidcLoginProfile{
		Subject:           "sub-123",
		PreferredUsername: "ignored",
		Name:              "Updated Name",
		Picture:           "https://example.com/updated.png",
		RawProfile:        `{"sub":"sub-123"}`,
	}, config.AuthConfig{TokenTTLHours: 2}, "127.0.0.1", "go-test")
	if err != nil {
		t.Fatalf("loginWithOIDCProfile() error = %v", err)
	}
	if ret == nil || ret.User == nil || ret.User.ID != user.ID {
		t.Fatalf("expected existing user login response, got %+v", ret)
	}

	var count int64
	if err := db.Model(&models.User{}).Count(&count).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected existing identity to reuse user, got %d users", count)
	}
}

// A first-time OIDC user is a stranger the provider vouched for, nothing
// more: they must land on the lowest staff role, never on an administrative
// one. Seeds every candidate role so the assignment cannot pass by accident
// of a missing row.
func TestOIDCLoginFirstUserGetsLowestStaffRole(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	now := time.Now()
	for _, role := range []struct{ name, code string }{
		{"Super Admin", constants.RoleCodeSuperAdmin},
		{"Admin", constants.RoleCodeAdmin},
		{"Support Agent", constants.RoleCodeCsUser},
	} {
		if err := db.Create(&models.Role{
			Name:   role.name,
			Code:   role.code,
			Status: enums.StatusOk,
			AuditFields: models.AuditFields{
				CreatedAt: now,
				UpdatedAt: now,
			},
		}).Error; err != nil {
			t.Fatalf("seed role %s: %v", role.code, err)
		}
	}

	spy := &employeeRealtimeInvalidationSpy{}
	if _, err := newOIDCLoginService(spy).loginWithOIDCProfile(&oidcLoginProfile{
		Subject:           "sub-777",
		Email:             "stranger@example.com",
		PreferredUsername: "stranger",
		Name:              "Stranger",
		RawProfile:        `{"sub":"sub-777"}`,
	}, config.AuthConfig{TokenTTLHours: 2}, "127.0.0.1", "go-test"); err != nil {
		t.Fatalf("loginWithOIDCProfile() error = %v", err)
	}

	var user models.User
	if err := db.Take(&user, "username = ?", "stranger").Error; err != nil {
		t.Fatalf("expected OIDC user to be created: %v", err)
	}

	var roles []models.Role
	if err := db.
		Joins("JOIN t_user_role ON t_user_role.role_id = t_role.id").
		Where("t_user_role.user_id = ?", user.ID).
		Find(&roles).Error; err != nil {
		t.Fatalf("query user roles: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("expected exactly one role for a first-time OIDC user, got %d", len(roles))
	}
	if roles[0].Code != constants.RoleCodeCsUser {
		t.Fatalf("first-time OIDC user role = %q, want %q", roles[0].Code, constants.RoleCodeCsUser)
	}
	if len(spy.ids) != 1 || spy.ids[0] != user.ID {
		t.Fatalf("default role insertion invalidations = %v", spy.ids)
	}
	if _, err := newOIDCLoginService(spy).loginWithOIDCProfile(&oidcLoginProfile{Subject: "sub-777", PreferredUsername: "stranger", RawProfile: `{}`}, config.AuthConfig{TokenTTLHours: 2}, "127.0.0.1", "go-test"); err != nil {
		t.Fatalf("existing-role OIDC login failed: %v", err)
	}
	if len(spy.ids) != 1 {
		t.Fatalf("existing default role caused another invalidation: %v", spy.ids)
	}
}
