package services

// This file has been modified for the KestrelDesk project.

import (
	"errors"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/request"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"

	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"github.com/mlogclub/simple/web"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestExtractBearerToken(t *testing.T) {
	svc := newAuthService()

	if got := svc.extractBearerToken("Bearer token_123"); got != "token_123" {
		t.Fatalf("expected bearer token to be extracted, got %q", got)
	}

	if got := svc.extractBearerToken("token_123"); got != "" {
		t.Fatalf("expected raw token to be rejected by bearer extractor, got %q", got)
	}
}

func TestAuthServiceLoginCreatesSingleAccessSession(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	user := createAuthTestUser(t, db, "admin", "secret")
	svc := newAuthService()

	ret, err := svc.Login(request.LoginRequest{
		Username: " admin ",
		Password: "secret",
	}, config.AuthConfig{TokenTTLHours: 2, MaxFailedAttempts: 5, CredentialLockMinute: 15}, "127.0.0.1", "go-test")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	if ret.AccessToken == "" || !strings.HasPrefix(ret.AccessToken, "ak_") {
		t.Fatalf("expected ak_ access token, got %q", ret.AccessToken)
	}
	if ret.ExpiresAt == "" {
		t.Fatal("expected expiresAt to be returned")
	}

	var sessions []models.LoginSession
	if err := db.Find(&sessions).Error; err != nil {
		t.Fatalf("query login sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected exactly one session, got %d", len(sessions))
	}
	if sessions[0].Token != ret.AccessToken {
		t.Fatalf("expected session token %q, got %q", ret.AccessToken, sessions[0].Token)
	}
	if sessions[0].UserID != user.ID {
		t.Fatalf("expected session user %d, got %d", user.ID, sessions[0].UserID)
	}
	if sessions[0].ClientType != "admin_web" {
		t.Fatalf("expected admin_web client type, got %q", sessions[0].ClientType)
	}

	logs := findCredentialLogs(t, db)
	if len(logs) != 1 {
		t.Fatalf("expected one credential log, got %d", len(logs))
	}
	if !logs[0].Success || logs[0].Principal != "admin" || logs[0].UserID != user.ID {
		t.Fatalf("unexpected success credential log: %+v", logs[0])
	}
}

func TestAuthServiceSupportLoginRequiresActiveCustomerBeforeIssuingSession(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	svc := newAuthService()
	authCfg := config.AuthConfig{TokenTTLHours: 2, MaxFailedAttempts: 5, CredentialLockMinute: 15}

	employee := createAuthTestUser(t, db, "support-login-employee", "synthetic-password")
	if err := db.Model(employee).Update("user_type", enums.UserTypeEmployee).Error; err != nil {
		t.Fatalf("mark employee user: %v", err)
	}
	if err := db.Create(&models.Customer{UserID: employee.ID, Status: enums.StatusOk}).Error; err != nil {
		t.Fatalf("create employee-associated customer: %v", err)
	}
	superAdmin := createAuthTestUser(t, db, "support-login-super-admin", "synthetic-password")
	if err := db.Model(superAdmin).Update("user_type", enums.UserTypeEmployee).Error; err != nil {
		t.Fatalf("mark super-admin user as employee: %v", err)
	}
	if err := db.Create(&models.Customer{UserID: superAdmin.ID, Status: enums.StatusOk}).Error; err != nil {
		t.Fatalf("create super-admin-associated customer: %v", err)
	}
	role := &models.Role{Code: constants.RoleCodeSuperAdmin, Name: "Synthetic super admin", Status: enums.StatusOk}
	if err := db.Create(role).Error; err != nil {
		t.Fatalf("create synthetic super-admin role: %v", err)
	}
	if err := db.Create(&models.UserRole{UserID: superAdmin.ID, RoleID: role.ID}).Error; err != nil {
		t.Fatalf("assign synthetic super-admin role: %v", err)
	}
	missingCustomer := createAuthTestUser(t, db, "support-login-no-customer", "synthetic-password")
	missingCustomer.UserType = enums.UserTypeUser
	if err := db.Model(missingCustomer).Update("user_type", enums.UserTypeUser).Error; err != nil {
		t.Fatalf("mark customer user: %v", err)
	}
	disabledCustomer := createAuthTestUser(t, db, "support-login-disabled-customer", "synthetic-password")
	if err := db.Model(disabledCustomer).Update("user_type", enums.UserTypeUser).Error; err != nil {
		t.Fatalf("mark disabled customer user: %v", err)
	}
	if err := db.Create(&models.Customer{UserID: disabledCustomer.ID, Status: enums.StatusDisabled}).Error; err != nil {
		t.Fatalf("create disabled customer: %v", err)
	}
	disabledUser := createAuthTestUser(t, db, "support-login-disabled-user", "synthetic-password")
	if err := db.Model(disabledUser).Updates(map[string]any{"user_type": enums.UserTypeUser, "status": enums.StatusDisabled}).Error; err != nil {
		t.Fatalf("disable user with active customer: %v", err)
	}
	if err := db.Create(&models.Customer{UserID: disabledUser.ID, Status: enums.StatusOk}).Error; err != nil {
		t.Fatalf("create active customer for disabled user: %v", err)
	}

	for _, user := range []*models.User{employee, superAdmin, missingCustomer, disabledCustomer, disabledUser} {
		if _, err := svc.LoginSupportCustomer(request.LoginRequest{Username: user.Username, Password: "synthetic-password"}, authCfg, "127.0.0.1", "go-test"); err == nil {
			t.Errorf("support login for user type %q / customer status must fail", user.UserType)
		}
		var sessionCount int64
		if err := db.Model(&models.LoginSession{}).Count(&sessionCount).Error; err != nil {
			t.Fatalf("count login sessions after denied support login: %v", err)
		}
		if sessionCount != 0 {
			t.Fatalf("denied support login issued %d LoginSession rows", sessionCount)
		}
	}

	customer := createAuthTestUser(t, db, "support-login-active-customer", "synthetic-password")
	if err := db.Model(customer).Update("user_type", enums.UserTypeUser).Error; err != nil {
		t.Fatalf("mark active customer user: %v", err)
	}
	if err := db.Create(&models.Customer{UserID: customer.ID, Status: enums.StatusOk}).Error; err != nil {
		t.Fatalf("create active customer: %v", err)
	}
	ret, err := svc.LoginSupportCustomer(request.LoginRequest{Username: customer.Username, Password: "synthetic-password"}, authCfg, "127.0.0.1", "go-test")
	if err != nil || ret == nil || ret.AccessToken == "" {
		t.Fatalf("active Support customer login should succeed, response present=%t, error=%v", ret != nil, err)
	}
	var sessionCount int64
	if err := db.Model(&models.LoginSession{}).Count(&sessionCount).Error; err != nil {
		t.Fatalf("count successful login sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Fatalf("successful Support customer login should issue one LoginSession, got %d", sessionCount)
	}
}

func TestAuthServiceGenericLoginStillAcceptsEmployeeAccount(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	employee := createAuthTestUser(t, db, "generic-login-employee", "synthetic-password")
	if err := db.Model(employee).Update("user_type", enums.UserTypeEmployee).Error; err != nil {
		t.Fatalf("mark employee user: %v", err)
	}
	ret, err := newAuthService().Login(request.LoginRequest{Username: employee.Username, Password: "synthetic-password"}, config.AuthConfig{TokenTTLHours: 2}, "127.0.0.1", "go-test")
	if err != nil || ret == nil || ret.AccessToken == "" {
		t.Fatalf("generic login should remain available to employee accounts, response present=%t, error=%v", ret != nil, err)
	}
	var sessions int64
	if err := db.Model(&models.LoginSession{}).Where("user_id = ?", employee.ID).Count(&sessions).Error; err != nil {
		t.Fatalf("count generic employee login sessions: %v", err)
	}
	if sessions != 1 {
		t.Fatalf("generic employee login should issue one LoginSession, got %d", sessions)
	}
}

func TestAuthServiceLoginFailureWritesCredentialLogs(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	createAuthTestUser(t, db, "admin", "secret")
	svc := newAuthService()
	authCfg := config.AuthConfig{TokenTTLHours: 2, MaxFailedAttempts: 5, CredentialLockMinute: 15}

	if _, err := svc.Login(request.LoginRequest{Username: "missing", Password: "secret"}, authCfg, "127.0.0.1", "go-test"); !hasCode(err, errorsx.CodeAuthInvalidAccount) {
		t.Fatalf("expected invalid account for missing user, got %v", err)
	}
	if _, err := svc.Login(request.LoginRequest{Username: "admin", Password: "wrong"}, authCfg, "127.0.0.1", "go-test"); !hasCode(err, errorsx.CodeAuthInvalidAccount) {
		t.Fatalf("expected invalid account for password mismatch, got %v", err)
	}

	logs := findCredentialLogs(t, db)
	if len(logs) != 2 {
		t.Fatalf("expected two credential logs, got %d", len(logs))
	}
	if logs[0].Reason != "user not found" || logs[0].Success {
		t.Fatalf("unexpected missing-user log: %+v", logs[0])
	}
	if logs[1].Reason != "password mismatch" || logs[1].Success {
		t.Fatalf("unexpected password-mismatch log: %+v", logs[1])
	}
}

func TestAuthServiceLoginCredentialLockout(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	user := createAuthTestUser(t, db, "admin", "secret")
	now := time.Now()
	for i := 0; i < 2; i++ {
		if err := db.Create(&models.LoginCredentialLog{
			Principal: "admin",
			UserID:    user.ID,
			Success:   false,
			ClientIP:  "127.0.0.1",
			Reason:    "password mismatch",
			CreatedAt: now.Add(-time.Duration(i+1) * time.Minute),
		}).Error; err != nil {
			t.Fatalf("seed credential log: %v", err)
		}
	}
	if err := db.Create(&models.LoginCredentialLog{
		Principal: "admin",
		UserID:    user.ID,
		Success:   false,
		ClientIP:  "127.0.0.1",
		Reason:    "password mismatch",
		CreatedAt: now.Add(-30 * time.Minute),
	}).Error; err != nil {
		t.Fatalf("seed old credential log: %v", err)
	}

	svc := newAuthService()
	_, err := svc.Login(request.LoginRequest{Username: "admin", Password: "secret"}, config.AuthConfig{
		TokenTTLHours:        2,
		MaxFailedAttempts:    2,
		CredentialLockMinute: 15,
	}, "127.0.0.1", "go-test")
	if !hasCode(err, errorsx.CodeAuthCredentialLocked) {
		t.Fatalf("expected credential locked error, got %v", err)
	}

	var lockedLog models.LoginCredentialLog
	if err := db.Order("id DESC").Take(&lockedLog).Error; err != nil {
		t.Fatalf("query latest credential log: %v", err)
	}
	if lockedLog.Reason != "credential locked" || lockedLog.Success {
		t.Fatalf("unexpected locked credential log: %+v", lockedLog)
	}

	var sessionCount int64
	if err := db.Model(&models.LoginSession{}).Count(&sessionCount).Error; err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 0 {
		t.Fatalf("expected no session while credential locked, got %d", sessionCount)
	}
}

func TestAuthServiceCredentialLockoutDoesNotExtendWhileLocked(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	createAuthTestUser(t, db, "admin", "secret")
	now := time.Now()
	entries := []models.LoginCredentialLog{
		{
			Principal: "admin",
			UserID:    1,
			Success:   false,
			ClientIP:  "127.0.0.1",
			Reason:    "password mismatch",
			CreatedAt: now.Add(-2 * time.Minute),
		},
		{
			Principal: "admin",
			UserID:    0,
			Success:   false,
			ClientIP:  "127.0.0.1",
			Reason:    "credential locked",
			CreatedAt: now.Add(-1 * time.Minute),
		},
	}
	if err := db.Create(&entries).Error; err != nil {
		t.Fatalf("seed credential logs: %v", err)
	}

	ret, err := newAuthService().Login(request.LoginRequest{Username: "admin", Password: "secret"}, config.AuthConfig{
		TokenTTLHours:        2,
		MaxFailedAttempts:    2,
		CredentialLockMinute: 15,
	}, "127.0.0.1", "go-test")
	if err != nil {
		t.Fatalf("expected locked attempt logs not to extend lockout, got %v", err)
	}
	if ret == nil || !strings.HasPrefix(ret.AccessToken, "ak_") {
		t.Fatalf("expected login response with ak_ token, got %+v", ret)
	}
}

func TestAuthServiceCredentialLockoutNormalizesPrincipalCase(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	createAuthTestUser(t, db, "admin", "secret")
	if err := db.Create(&models.LoginCredentialLog{
		Principal: "admin",
		UserID:    1,
		Success:   false,
		ClientIP:  "127.0.0.1",
		Reason:    "password mismatch",
		CreatedAt: time.Now().Add(-time.Minute),
	}).Error; err != nil {
		t.Fatalf("seed credential log: %v", err)
	}

	_, err := newAuthService().Login(request.LoginRequest{Username: "ADMIN", Password: "secret"}, config.AuthConfig{
		TokenTTLHours:        2,
		MaxFailedAttempts:    1,
		CredentialLockMinute: 15,
	}, "127.0.0.1", "go-test")
	if !hasCode(err, errorsx.CodeAuthCredentialLocked) {
		t.Fatalf("expected normalized principal to be locked, got %v", err)
	}

	var lockedLog models.LoginCredentialLog
	if err := db.Order("id DESC").Take(&lockedLog).Error; err != nil {
		t.Fatalf("query latest credential log: %v", err)
	}
	if lockedLog.Principal != "admin" || lockedLog.Reason != "credential locked" {
		t.Fatalf("unexpected locked log: %+v", lockedLog)
	}
}

// TestAuthServiceCredentialLockoutIsScopedToTheClientAddress is the regression
// test for the denial of service. Keyed on the username alone, anybody who knew a
// username could lock the real account out for the whole window, from anywhere,
// as often as they liked.
func TestAuthServiceCredentialLockoutIsScopedToTheClientAddress(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	user := createAuthTestUser(t, db, "admin", "secret")
	now := time.Now()
	for i := 0; i < 5; i++ {
		if err := db.Create(&models.LoginCredentialLog{
			Principal: "admin",
			UserID:    user.ID,
			Success:   false,
			ClientIP:  "203.0.113.66",
			Reason:    "password mismatch",
			CreatedAt: now.Add(-time.Duration(i+1) * time.Minute),
		}).Error; err != nil {
			t.Fatalf("seed credential log: %v", err)
		}
	}

	authCfg := config.AuthConfig{TokenTTLHours: 2, MaxFailedAttempts: 3, CredentialLockMinute: 15}

	if _, err := newAuthService().Login(request.LoginRequest{Username: "admin", Password: "secret"}, authCfg, "203.0.113.66", "go-test"); !hasCode(err, errorsx.CodeAuthCredentialLocked) {
		t.Fatalf("expected the attacking address to be locked, got %v", err)
	}

	ret, err := newAuthService().Login(request.LoginRequest{Username: "admin", Password: "secret"}, authCfg, "198.51.100.7", "go-test")
	if err != nil {
		t.Fatalf("the legitimate owner was locked out by somebody else's failures: %v", err)
	}
	if ret == nil || ret.AccessToken == "" {
		t.Fatalf("expected a session for the legitimate owner, got %+v", ret)
	}
}

// TestAuthServiceCredentialLockoutByAddressAcrossPrincipals covers the window that
// replaces the removed account-wide lock: one address spraying many usernames.
func TestAuthServiceCredentialLockoutByAddressAcrossPrincipals(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	createAuthTestUser(t, db, "admin", "secret")
	now := time.Now()
	for i, principal := range []string{"admin", "root", "operator", "support"} {
		if err := db.Create(&models.LoginCredentialLog{
			Principal: principal,
			UserID:    0,
			Success:   false,
			ClientIP:  "203.0.113.66",
			Reason:    "user not found",
			CreatedAt: now.Add(-time.Duration(i+1) * time.Minute),
		}).Error; err != nil {
			t.Fatalf("seed credential log: %v", err)
		}
	}

	authCfg := config.AuthConfig{
		TokenTTLHours:          2,
		MaxFailedAttempts:      10,
		MaxFailedAttemptsPerIP: 4,
		CredentialLockMinute:   15,
	}

	// No single username has reached MaxFailedAttempts, so only the per-address
	// window can catch this.
	if _, err := newAuthService().Login(request.LoginRequest{Username: "admin", Password: "secret"}, authCfg, "203.0.113.66", "go-test"); !hasCode(err, errorsx.CodeAuthCredentialLocked) {
		t.Fatalf("expected credential stuffing from one address to be locked, got %v", err)
	}

	if _, err := newAuthService().Login(request.LoginRequest{Username: "admin", Password: "secret"}, authCfg, "198.51.100.7", "go-test"); err != nil {
		t.Fatalf("an unrelated address was locked by another address's failures: %v", err)
	}
}

func TestAuthServiceCredentialLockoutDisabledWhenMaxAttemptsNonPositive(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	user := createAuthTestUser(t, db, "admin", "secret")
	now := time.Now()
	for i := 0; i < 3; i++ {
		if err := db.Create(&models.LoginCredentialLog{
			Principal: "admin",
			UserID:    user.ID,
			Success:   false,
			ClientIP:  "127.0.0.1",
			Reason:    "credential locked",
			CreatedAt: now.Add(-time.Duration(i+1) * time.Minute),
		}).Error; err != nil {
			t.Fatalf("seed credential log: %v", err)
		}
	}

	ret, err := newAuthService().Login(request.LoginRequest{Username: "admin", Password: "secret"}, config.AuthConfig{
		TokenTTLHours:        2,
		MaxFailedAttempts:    0,
		CredentialLockMinute: 15,
	}, "127.0.0.1", "go-test")
	if err != nil {
		t.Fatalf("expected lockout to be disabled, got %v", err)
	}
	if ret == nil || ret.AccessToken == "" {
		t.Fatalf("expected login response with access token, got %+v", ret)
	}
}

func TestValidateSessionTokenStates(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	svc := newAuthService()
	now := time.Now()

	if _, err := svc.validateSessionToken("  "); !hasCode(err, errorsx.CodeAuthUnauthorized) {
		t.Fatalf("expected unauthorized for empty token, got %v", err)
	}
	if _, err := svc.validateSessionToken("missing"); !hasCode(err, errorsx.CodeAuthInvalidToken) {
		t.Fatalf("expected invalid token for missing session, got %v", err)
	}

	revokedAt := now
	if err := db.Create(&models.LoginSession{
		UserID:     1,
		Token:      "ak_revoked",
		ClientType: "admin_web",
		ExpiredAt:  now.Add(time.Hour),
		RevokedAt:  &revokedAt,
		AuditFields: models.AuditFields{
			CreatedAt: now,
			UpdatedAt: now,
		},
	}).Error; err != nil {
		t.Fatalf("seed revoked session: %v", err)
	}
	if _, err := svc.validateSessionToken("ak_revoked"); !hasCode(err, errorsx.CodeAuthInvalidToken) {
		t.Fatalf("expected invalid token for revoked session, got %v", err)
	}

	if err := db.Create(&models.LoginSession{
		UserID:     1,
		Token:      "ak_expired",
		ClientType: "admin_web",
		ExpiredAt:  now.Add(-time.Hour),
		AuditFields: models.AuditFields{
			CreatedAt: now,
			UpdatedAt: now,
		},
	}).Error; err != nil {
		t.Fatalf("seed expired session: %v", err)
	}
	if _, err := svc.validateSessionToken("ak_expired"); !hasCode(err, errorsx.CodeAuthInvalidToken) {
		t.Fatalf("expected invalid token for expired session, got %v", err)
	}

	if err := db.Create(&models.LoginSession{
		UserID:     1,
		Token:      "ak_valid",
		ClientType: "admin_web",
		ExpiredAt:  now.Add(time.Hour),
		AuditFields: models.AuditFields{
			CreatedAt: now,
			UpdatedAt: now,
		},
	}).Error; err != nil {
		t.Fatalf("seed valid session: %v", err)
	}
	session, err := svc.validateSessionToken("ak_valid")
	if err != nil {
		t.Fatalf("expected valid session token, got %v", err)
	}
	if session.Token != "ak_valid" {
		t.Fatalf("expected valid session token ak_valid, got %q", session.Token)
	}
}

func TestAuthServiceLogoutRevokesCurrentTokenOnly(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	now := time.Now()
	sessions := []models.LoginSession{
		{
			UserID:     1,
			Token:      "ak_current",
			ClientType: "admin_web",
			ExpiredAt:  now.Add(time.Hour),
			AuditFields: models.AuditFields{
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
		{
			UserID:     1,
			Token:      "ak_other",
			ClientType: "admin_web",
			ExpiredAt:  now.Add(time.Hour),
			AuditFields: models.AuditFields{
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
	}
	if err := db.Create(&sessions).Error; err != nil {
		t.Fatalf("seed sessions: %v", err)
	}

	if err := newAuthService().Logout("Bearer ak_current"); err != nil {
		t.Fatalf("logout failed: %v", err)
	}

	var current models.LoginSession
	if err := db.Take(&current, "token = ?", "ak_current").Error; err != nil {
		t.Fatalf("query current session: %v", err)
	}
	if current.RevokedAt == nil {
		t.Fatal("expected current session to be revoked")
	}
	var other models.LoginSession
	if err := db.Take(&other, "token = ?", "ak_other").Error; err != nil {
		t.Fatalf("query other session: %v", err)
	}
	if other.RevokedAt != nil {
		t.Fatal("expected other session to remain active")
	}
}

func TestEmployeeLogoutInvalidatesExactSession(t *testing.T) {
	for _, mode := range []string{"success", "empty", "missing", "already revoked", "write failure"} {
		t.Run(mode, func(t *testing.T) {
			db, l, user, _ := employeeMutationFixture(t)
			row := employeeMutationLogin(t, db, user.ID, "logout-current")
			other := employeeMutationLogin(t, db, user.ID, "logout-other")
			a1, a2 := employeeMutationSockets(t, l, user.ID, row.ID, "a")
			b1, b2 := employeeMutationSockets(t, l, user.ID, other.ID, "b")
			token := "Bearer " + row.Token
			switch mode {
			case "empty":
				token = ""
			case "missing":
				token = "Bearer missing"
			case "already revoked":
				if err := LoginSessionService.Updates(row.ID, map[string]any{"revoked_at": time.Now()}); err != nil {
					t.Fatal(err)
				}
			case "write failure":
				employeeMutationFailUpdate(t, db, "t_login_session", errors.New("injected logout write failure"))
			}
			calls := 0
			l.onLogin = func(id int64) {
				calls++
				persisted := employeeMutationReadLogin(t, db, id)
				if id != row.ID || persisted.RevokedAt == nil || persisted.UpdateUserID != user.ID || persisted.UpdateUserName != user.Username {
					t.Fatal("logout invalidation preceded persistence or used wrong server actor")
				}
			}
			l.onEmployees = func([]int64) { t.Fatal("logout invalidated all employee sessions") }
			err := newAuthService().Logout(token)
			if (err != nil) != (mode == "write failure") {
				t.Fatalf("logout error = %v", err)
			}
			wantClosed := mode == "success"
			wantCalls := 0
			if wantClosed {
				wantCalls = 1
			}
			if calls != wantCalls || a1.Closed.Load() != wantClosed || a2.Closed.Load() != wantClosed || b1.Closed.Load() || b2.Closed.Load() || employeeMutationReadLogin(t, db, other.ID).RevokedAt != nil {
				t.Fatal("logout exact/no-op/error socket isolation failed")
			}
		})
	}
}

func setupAuthServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			TablePrefix:   "t_",
			SingularTable: true,
		},
	})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	if err := db.AutoMigrate(
		&models.Organization{},
		&models.OrganizationMember{},
		&models.User{},
		&models.Customer{},
		&models.UserIdentity{},
		&models.Role{},
		&models.Permission{},
		&models.UserRole{},
		&models.RolePermission{},
		&models.UserPermission{},
		&models.LoginSession{},
		&models.LoginCredentialLog{},
	); err != nil {
		t.Fatalf("migrate auth tables: %v", err)
	}
	sqls.SetDB(db)
	return db
}

func createAuthTestUser(t *testing.T, db *gorm.DB, username, password string) *models.User {
	t.Helper()
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	now := time.Now()
	user := &models.User{
		Username: username,
		Nickname: username,
		Password: string(passwordHash),
		Status:   enums.StatusOk,
		AuditFields: models.AuditFields{
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create auth test user: %v", err)
	}
	return user
}

func findCredentialLogs(t *testing.T, db *gorm.DB) []models.LoginCredentialLog {
	t.Helper()
	var logs []models.LoginCredentialLog
	if err := db.Order("id ASC").Find(&logs).Error; err != nil {
		t.Fatalf("query credential logs: %v", err)
	}
	return logs
}

func hasCode(err error, code int) bool {
	if err == nil {
		return false
	}
	var codeErr *web.CodeError
	if errors.As(err, &codeErr) {
		return codeErr.Code == code
	}
	return false
}

type rbacGrantFixture struct {
	UserID, RoleID, PermissionID int64
}

func createRBACTestGrant(t *testing.T, db *gorm.DB, permissionCode string, roleStatus, permissionStatus enums.Status, includeRole bool, effect *int, expiredAt *time.Time) rbacGrantFixture {
	t.Helper()
	unique := t.Name() + ":" + permissionCode
	user := createAuthTestUser(t, db, unique, "synthetic-rbac-password")
	if err := db.Model(user).Update("user_type", enums.UserTypeEmployee).Error; err != nil {
		t.Fatalf("set employee type: %v", err)
	}
	role := &models.Role{Code: "rbac:" + unique, Name: "RBAC fixture", Status: roleStatus}
	permission := &models.Permission{Code: permissionCode, Status: permissionStatus}
	for _, row := range []any{role, permission} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("create RBAC definition: %v", err)
		}
	}
	if includeRole {
		if err := db.Create(&models.UserRole{UserID: user.ID, RoleID: role.ID}).Error; err != nil {
			t.Fatalf("create user role: %v", err)
		}
	}
	if err := db.Create(&models.RolePermission{RoleID: role.ID, PermissionID: permission.ID}).Error; err != nil {
		t.Fatalf("create role permission: %v", err)
	}
	if effect != nil {
		override := &models.UserPermission{UserID: user.ID, PermissionID: permission.ID, Effect: *effect, ExpiredAt: expiredAt}
		if err := db.Create(override).Error; err != nil {
			t.Fatalf("create direct override: %v", err)
		}
		// GORM applies default:1 to zero values during Create.
		if *effect == 0 {
			if err := db.Model(override).Update("effect", 0).Error; err != nil {
				t.Fatalf("preserve zero effect: %v", err)
			}
		}
	}
	return rbacGrantFixture{UserID: user.ID, RoleID: role.ID, PermissionID: permission.ID}
}

func TestRBACEffectivePermissionFiltering(t *testing.T) {
	allow, deny, negative, zero := 1, -1, -2, 0
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	cases := []struct {
		name                         string
		roleStatus, permissionStatus enums.Status
		includeRole                  bool
		effect                       *int
		expiredAt                    *time.Time
		want                         []string
	}{
		{"enabled role and permission", enums.StatusOk, enums.StatusOk, true, nil, nil, []string{"conversation.view"}},
		{"disabled role", enums.StatusDisabled, enums.StatusOk, true, nil, nil, []string{}},
		{"deleted role", enums.StatusDeleted, enums.StatusOk, true, nil, nil, []string{}},
		{"disabled role permission", enums.StatusOk, enums.StatusDisabled, true, nil, nil, []string{}},
		{"deleted role permission", enums.StatusOk, enums.StatusDeleted, true, nil, nil, []string{}},
		{"direct allow without role", enums.StatusDisabled, enums.StatusOk, false, &allow, nil, []string{"conversation.view"}},
		{"disabled direct allow", enums.StatusOk, enums.StatusDisabled, false, &allow, nil, []string{}},
		{"deleted direct allow", enums.StatusOk, enums.StatusDeleted, false, &allow, nil, []string{}},
		{"past expiry", enums.StatusOk, enums.StatusOk, false, &allow, &past, []string{}},
		{"future expiry", enums.StatusOk, enums.StatusOk, false, &allow, &future, []string{"conversation.view"}},
		{"null expiry", enums.StatusOk, enums.StatusOk, false, &allow, nil, []string{"conversation.view"}},
		{"role and direct deny", enums.StatusOk, enums.StatusOk, true, &deny, nil, []string{}},
		{"negative effect", enums.StatusOk, enums.StatusOk, true, &negative, nil, []string{}},
		{"zero effect", enums.StatusOk, enums.StatusOk, false, &zero, nil, []string{"conversation.view"}},
		{"positive effect deduplicates role", enums.StatusOk, enums.StatusOk, true, &allow, nil, []string{"conversation.view"}},
		{"expired deny preserves role", enums.StatusOk, enums.StatusOk, true, &deny, &past, []string{"conversation.view"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupAuthServiceTestDB(t)
			fixture := createRBACTestGrant(t, db, "conversation.view", tc.roleStatus, tc.permissionStatus, tc.includeRole, tc.effect, tc.expiredAt)
			got, err := newAuthService().loadUserPermissionCodes(db, fixture.UserID)
			if err != nil {
				t.Fatalf("effective-permission query failed: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("permissions=%v want=%v", got, tc.want)
			}
		})
	}
	t.Run("sorted union and dedup", func(t *testing.T) {
		db := setupAuthServiceTestDB(t)
		fixture := createRBACTestGrant(t, db, "conversation.view", enums.StatusOk, enums.StatusOk, true, &allow, nil)
		second := createRBACTestGrant(t, db, "aaa.synthetic", enums.StatusOk, enums.StatusOk, true, nil, nil)
		if err := db.Create(&models.UserRole{UserID: fixture.UserID, RoleID: second.RoleID}).Error; err != nil {
			t.Fatal(err)
		}
		// A second role grants the same code to exercise DISTINCT and final dedup.
		if err := db.Create(&models.RolePermission{RoleID: second.RoleID, PermissionID: fixture.PermissionID}).Error; err != nil {
			t.Fatal(err)
		}
		got, err := newAuthService().loadUserPermissionCodes(db, fixture.UserID)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"aaa.synthetic", "conversation.view"}; !slices.Equal(got, want) {
			t.Fatalf("permissions=%v want=%v", got, want)
		}
	})
}

func TestRBACActiveOverrideSelection(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	now := time.Date(2026, 10, 4, 4, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Second), now.Add(time.Second)
	allow, deny, zero := 1, -1, 0
	cases := []struct {
		code   string
		status enums.Status
		effect *int
		expiry *time.Time
	}{
		{"pr2.disabled.allow", enums.StatusDisabled, &allow, nil},
		{"pr2.disabled.deny", enums.StatusDisabled, &deny, nil},
		{"pr2.deleted.allow", enums.StatusDeleted, &allow, nil},
		{"pr2.enabled.deny", enums.StatusOk, &deny, nil},
		{"pr2.enabled.zero", enums.StatusOk, &zero, nil},
		{"pr2.enabled.null", enums.StatusOk, &allow, nil},
		{"pr2.enabled.future", enums.StatusOk, &allow, &future},
		{"pr2.enabled.equal", enums.StatusOk, &allow, &now},
		{"pr2.enabled.past", enums.StatusOk, &allow, &past},
	}
	var userID int64
	for _, tc := range cases {
		fixture := createRBACTestGrant(t, db, tc.code, enums.StatusOk, tc.status, false, tc.effect, tc.expiry)
		if userID == 0 {
			userID = fixture.UserID
		}
		if err := db.Model(&models.UserPermission{}).Where("user_id = ?", fixture.UserID).Update("user_id", userID).Error; err != nil {
			t.Fatal(err)
		}
	}
	var rows []userPermissionOverride
	if err := activeUserPermissionOverridesQuery(db, userID, now).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	got := make(map[string]int)
	for _, row := range rows {
		if _, exists := got[row.Code]; exists {
			t.Fatalf("duplicate override %q", row.Code)
		}
		got[row.Code] = row.Effect
	}
	want := map[string]int{"pr2.enabled.deny": -1, "pr2.enabled.zero": 0, "pr2.enabled.null": 1, "pr2.enabled.future": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected overrides=%v want=%v", got, want)
	}
}

func TestRBACSQLDialectCompatibility(t *testing.T) {
	now := time.Date(2026, 10, 4, 4, 0, 0, 0, time.UTC)
	const userID int64 = 42
	for _, tc := range []struct {
		name      string
		dialector gorm.Dialector
	}{
		{"sqlite", sqlite.Open(":memory:")},
		{"mysql", mysql.New(mysql.Config{DSN: "rbac_dry_run@tcp(127.0.0.1:1)/rbac?parseTime=true", SkipInitializeWithVersion: true})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(tc.dialector, &gorm.Config{DryRun: true, DisableAutomaticPing: true})
			if err != nil {
				t.Fatalf("open dry-run dialect: %v", err)
			}
			role := rolePermissionCodesQuery(db, userID).Find(&[]struct{ Code string }{})
			if role.Error != nil {
				t.Fatal(role.Error)
			}
			roleSQL := role.Statement.SQL.String()
			for _, fragment := range []string{"DISTINCT p.code", "JOIN t_role_permission AS rp ON rp.permission_id = p.id", "JOIN t_user_role AS ur ON ur.role_id = rp.role_id", "JOIN t_role AS r ON r.id = rp.role_id", "ur.user_id = ?", "p.status = ?", "r.status = ?"} {
				if !strings.Contains(roleSQL, fragment) {
					t.Fatalf("role SQL missing %q: %s", fragment, roleSQL)
				}
			}
			if want := []any{userID, enums.StatusOk, enums.StatusOk}; !reflect.DeepEqual(role.Statement.Vars, want) {
				t.Fatalf("role vars=%v want=%v", role.Statement.Vars, want)
			}
			direct := activeUserPermissionOverridesQuery(db, userID, now).Find(&[]userPermissionOverride{})
			if direct.Error != nil {
				t.Fatal(direct.Error)
			}
			directSQL := direct.Statement.SQL.String()
			for _, fragment := range []string{"p.code, up.effect", "JOIN t_permission AS p ON p.id = up.permission_id", "up.user_id = ?", "up.expired_at IS NULL OR up.expired_at > ?", "p.status = ?"} {
				if !strings.Contains(directSQL, fragment) {
					t.Fatalf("direct SQL missing %q: %s", fragment, directSQL)
				}
			}
			if want := []any{userID, now, enums.StatusOk}; !reflect.DeepEqual(direct.Statement.Vars, want) {
				t.Fatalf("direct vars=%v want=%v", direct.Statement.Vars, want)
			}
		})
	}
}

func TestRBACFreshAuthScopeAfterRoleDisable(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	fixture := createRBACTestGrant(t, db, "conversation.view", enums.StatusOk, enums.StatusOk, true, nil, nil)
	svc := newAuthService()
	roles, permissions, err := svc.loadUserAuthScope(db, fixture.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 1 || !slices.Equal(permissions, []string{"conversation.view"}) {
		t.Fatalf("initial scope roles=%v permissions=%v", roles, permissions)
	}
	snapshot := &dto.AuthPrincipal{UserID: fixture.UserID, Roles: slices.Clone(roles), Permissions: slices.Clone(permissions)}
	if err := RoleService.UpdateStatus(fixture.RoleID, enums.StatusDisabled, &dto.AuthPrincipal{UserID: fixture.UserID, Username: "synthetic-operator"}); err != nil {
		t.Fatalf("disable role: %v", err)
	}
	freshRoles, freshPermissions, err := svc.loadUserAuthScope(db, fixture.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if len(freshRoles) != 0 || len(freshPermissions) != 0 {
		t.Fatalf("fresh scope roles=%v permissions=%v", freshRoles, freshPermissions)
	}
	if !slices.Equal(snapshot.Roles, roles) || !slices.Equal(snapshot.Permissions, []string{"conversation.view"}) {
		t.Fatalf("existing snapshot mutated: roles=%v permissions=%v", snapshot.Roles, snapshot.Permissions)
	}
}

func TestEmployeeAuthTrustedSessionMetadata(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	grant := createRBACTestGrant(t, db, "conversation.view", enums.StatusOk, enums.StatusOk, true, nil, nil)
	svc := newAuthService()
	for i, token := range []string{"synthetic-session-a", "synthetic-session-b"} {
		row := models.LoginSession{UserID: grant.UserID, Token: token, ExpiredAt: time.Now().Add(time.Duration(i+1) * time.Hour)}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = httptest.NewRequest("GET", "/", nil)
		ctx.Request.Header.Set("Authorization", "Bearer "+token)
		if _, err := svc.Authenticate(ctx); err != nil {
			t.Fatal(err)
		}
		value, ok := svc.GetAuthenticatedEmployeeSession(ctx)
		if !ok || value.LoginSessionID != row.ID || value.EmployeeID != row.UserID || !value.LoginSessionExpiresAt.Equal(row.ExpiredAt) {
			t.Fatal("trusted binding missing or wrong")
		}
	}
}
func TestEmployeeAuthExpiryEqualityRejected(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	grant := createRBACTestGrant(t, db, "conversation.view", enums.StatusOk, enums.StatusOk, true, nil, nil)
	token := "synthetic-equality"
	row := models.LoginSession{UserID: grant.UserID, Token: token, ExpiredAt: time.Now().Add(time.Hour)}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	svc := newAuthService()
	if _, err := svc.validateSessionTokenAt(token, row.ExpiredAt); err == nil {
		t.Fatal("equal expiry accepted")
	}
}

func TestEmployeeAuthClientIdentityIgnored(t *testing.T) {
	db := setupAuthServiceTestDB(t)
	grant := createRBACTestGrant(t, db, "conversation.view", enums.StatusOk, enums.StatusOk, true, nil, nil)
	serverRow := models.LoginSession{UserID: grant.UserID, Token: "synthetic-server-binding", ExpiredAt: time.Now().Add(time.Hour)}
	if err := db.Create(&serverRow).Error; err != nil {
		t.Fatal(err)
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("GET", "/?employeeId=987&loginSessionId=654&expiresAt=2099-01-01", nil)
	ctx.Request.Header.Set("Authorization", "Bearer "+serverRow.Token)
	ctx.Set("authenticatedEmployeeSession", authenticatedEmployeeSession{LoginSessionID: 654})
	ctx.Request.Header.Set("X-Login-Session-ID", "654")
	svc := newAuthService()
	if _, err := svc.Authenticate(ctx); err != nil {
		t.Fatal(err)
	}
	value, ok := svc.GetAuthenticatedEmployeeSession(ctx)
	if !ok {
		t.Fatal("missing metadata")
	}
	if value.LoginSessionID != serverRow.ID {
		t.Fatal("client identity overrode server binding")
	}
}
func TestEmployeeAuthInjectedPrincipalHasNoMetadata(t *testing.T) {
	ctxWithPrincipalOnly, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctxWithPrincipalOnly.Request = httptest.NewRequest("GET", "/", nil)
	ctxWithPrincipalOnly.Set(authPrincipalContextKey, &dto.AuthPrincipal{UserID: 123})
	svc := newAuthService()
	if _, err := svc.Authenticate(ctxWithPrincipalOnly); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.GetAuthenticatedEmployeeSession(ctxWithPrincipalOnly); ok {
		t.Fatal("untrusted metadata manufactured")
	}
}
