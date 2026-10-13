package services

// This file has been modified for the KestrelDesk project.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/request"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/pkg/utils"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mlogclub/simple/common/strs"
	"github.com/mlogclub/simple/sqls"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	authPrincipalContextKey = "authPrincipal"
)

var errSupportCustomerIneligible = errors.New("not an active support customer")

var AuthService = newAuthService()

func newAuthService() *authService {
	return &authService{}
}

type authService struct {
}

type authenticatedEmployeeSession struct {
	EmployeeID            int64
	LoginSessionID        int64
	LoginSessionExpiresAt time.Time
	Principal             *dto.AuthPrincipal
	NextAuthzChangeAt     *time.Time
}
type authenticatedEmployeeSessionContextKey struct{}

func (s *authService) GetAuthenticatedEmployeeSession(ctx *gin.Context) (*authenticatedEmployeeSession, bool) {
	if ctx == nil || ctx.Request == nil {
		return nil, false
	}
	value, ok := ctx.Request.Context().Value(authenticatedEmployeeSessionContextKey{}).(authenticatedEmployeeSession)
	if !ok {
		return nil, false
	}
	return &value, true
}

func (s *authService) setAuthenticatedEmployeeSession(ctx *gin.Context, value authenticatedEmployeeSession) {
	ctx.Request = ctx.Request.WithContext(context.WithValue(ctx.Request.Context(), authenticatedEmployeeSessionContextKey{}, value))
}

func (s *authService) GetAuthPrincipal(ctx *gin.Context) *dto.AuthPrincipal {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Get(authPrincipalContextKey)
	if principal, ok := v.(*dto.AuthPrincipal); ok {
		return principal
	}
	return nil
}

func (s *authService) setAuthPrincipal(ctx *gin.Context, user *models.User, roles, permissions []string) *dto.AuthPrincipal {
	principal := employeeAuthPrincipal(user, roles, permissions)
	ctx.Set(authPrincipalContextKey, principal)
	return principal
}

func (s *authService) RequirePermission(ctx *gin.Context, permission constants.Permission) (principal *dto.AuthPrincipal, err error) {
	if principal = s.GetAuthPrincipal(ctx); principal == nil {
		if principal, err = s.Authenticate(ctx); err != nil {
			return nil, err
		}
	}

	if principal == nil {
		return nil, errorsx.ForbiddenI18n("error.e0225")
	}

	if !s.HasPermission(ctx, permission.Code) {
		return principal, errorsx.ForbiddenI18n("error.e0225")
	}
	return principal, nil
}

func (s *authService) Login(req request.LoginRequest, authCfg config.AuthConfig, clientIP, userAgent string) (*response.LoginResponse, error) {
	return s.login(req, authCfg, clientIP, userAgent, false)
}

// LoginSupportCustomer issues a password session only for an active registered
// Support customer. The eligibility check runs after password verification but
// before any LoginSession is created.
func (s *authService) LoginSupportCustomer(req request.LoginRequest, authCfg config.AuthConfig, clientIP, userAgent string) (*response.LoginResponse, error) {
	return s.login(req, authCfg, clientIP, userAgent, true)
}

func (s *authService) login(req request.LoginRequest, authCfg config.AuthConfig, clientIP, userAgent string, requireSupportCustomer bool) (*response.LoginResponse, error) {
	username := strings.TrimSpace(req.Username)
	principal := normalizeLoginPrincipal(username)
	password := req.Password
	if username == "" || strings.TrimSpace(password) == "" {
		return nil, errorsx.InvalidParamI18n("error.e0258")
	}

	if s.isCredentialLocked(principal, clientIP, authCfg) {
		_ = s.createLoginCredentialLog(principal, 0, false, clientIP, userAgent, "credential locked")
		return nil, errorsx.CredentialLockedI18n("error.e0270")
	}

	user := UserService.GetByUsername(username)
	if user == nil || user.Status != enums.StatusOk {
		_ = s.createLoginCredentialLog(principal, 0, false, clientIP, userAgent, "user not found")
		return nil, errorsx.InvalidAccountI18n("error.e0260")
	}
	if strs.IsBlank(user.Password) || bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)) != nil {
		_ = s.createLoginCredentialLog(principal, user.ID, false, clientIP, userAgent, "password mismatch")
		return nil, errorsx.InvalidAccountI18n("error.e0260")
	}
	var ret *response.LoginResponse
	if err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		loginUser := user
		if requireSupportCustomer {
			// Re-read eligibility in the same transaction that creates the
			// LoginSession, so the issued Support session uses current server-side
			// identity and Customer state rather than a preflight snapshot.
			loginUser = repositories.UserRepository.Get(ctx.Tx, user.ID)
			if loginUser == nil || loginUser.Status != enums.StatusOk || loginUser.UserType != enums.UserTypeUser {
				return errSupportCustomerIneligible
			}
			customer := repositories.CustomerRepository.FindOne(ctx.Tx, sqls.NewCnd().Eq("user_id", loginUser.ID).Eq("status", enums.StatusOk))
			if customer == nil {
				return errSupportCustomerIneligible
			}
		}
		var dbErr error
		ret, dbErr = s.issueTokens(ctx, loginUser, clientIP, userAgent, authCfg)
		if dbErr != nil {
			return dbErr
		}
		if dbErr = repositories.UserRepository.Updates(ctx.Tx, loginUser.ID, map[string]any{
			"last_login_at":    time.Now(),
			"last_login_ip":    clientIP,
			"update_user_id":   loginUser.ID,
			"update_user_name": loginUser.Username,
			"updated_at":       time.Now(),
		}); dbErr != nil {
			return dbErr
		}
		return nil
	}); err != nil {
		if requireSupportCustomer && errors.Is(err, errSupportCustomerIneligible) {
			_ = s.createLoginCredentialLog(principal, user.ID, false, clientIP, userAgent, "not an active support customer")
			return nil, errorsx.InvalidAccountI18n("error.e0260")
		}
		return nil, err
	}

	_ = s.createLoginCredentialLog(principal, user.ID, true, clientIP, userAgent, "")
	return ret, nil
}

func (s *authService) Logout(accessToken string) error {
	accessToken = s.extractBearerToken(accessToken)
	if accessToken != "" {
		if session := LoginSessionService.FindOne(sqls.NewCnd().Eq("token", accessToken)); session != nil && session.RevokedAt == nil {
			username := ""
			if user := UserService.Get(session.UserID); user != nil {
				username = user.Username
			}
			return LoginSessionService.Revoke(session.ID, session.UserID, username)
		}
	}
	return nil
}

func (s *authService) Authenticate(ctx *gin.Context) (*dto.AuthPrincipal, error) {
	if principal := s.GetAuthPrincipal(ctx); principal != nil {
		return principal, nil
	}

	token := s.extractBearerToken(ctx.GetHeader("Authorization"))
	if token == "" {
		token = strings.TrimSpace(ctx.Query("accessToken"))
	}
	if token == "" {
		return nil, errorsx.UnauthorizedI18n("error.auth.expired")
	}

	now := time.Now()
	session, err := s.validateSessionTokenAt(token, now)
	if err != nil {
		return nil, err
	}

	user, err := repositories.EmployeeAuthRepository.UserByID(sqls.DB(), session.UserID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errorsx.UnauthorizedI18n("error.e0256")
	}
	if err != nil {
		return nil, err
	}
	if user.Status != enums.StatusOk || user.DeletedAt != nil {
		return nil, errorsx.UnauthorizedI18n("error.e0256")
	}

	roles, permissions, nextAuthzChangeAt, err := loadEmployeeAuthScope(sqls.DB(), user.ID, now)
	if err != nil {
		return nil, err
	}
	principal := s.setAuthPrincipal(ctx, user, roles, permissions)
	s.setAuthenticatedEmployeeSession(ctx, authenticatedEmployeeSession{EmployeeID: user.ID, LoginSessionID: session.ID, LoginSessionExpiresAt: session.ExpiredAt, Principal: principal, NextAuthzChangeAt: nextAuthzChangeAt})
	_ = LoginSessionService.Updates(session.ID, map[string]any{
		"last_seen_at": now,
		"updated_at":   now,
	})

	return principal, nil
}

func (s *authService) HasPermission(ctx *gin.Context, permissionCode string) bool {
	principal := s.GetAuthPrincipal(ctx)
	if principal == nil {
		return false
	}
	return slices.Contains(principal.Permissions, permissionCode)
}

func (s *authService) CurrentProfile(ctx *gin.Context) (*response.LoginResponse, error) {
	principal, err := s.Authenticate(ctx)
	if err != nil {
		return nil, err
	}

	user := UserService.Get(principal.UserID)
	if user == nil || user.Status != enums.StatusOk {
		return nil, errorsx.UnauthorizedI18n("error.e0256")
	}

	return &response.LoginResponse{
		User: &response.AuthUserResponse{
			ID:       user.ID,
			Username: user.Username,
			Nickname: user.Nickname,
			Avatar:   user.Avatar,
			Email:    derefString(user.Email),
			UserType: user.UserType,
			Status:   user.Status,
			Roles:    principal.Roles,
		},
		Permissions: principal.Permissions,
		Roles:       principal.Roles,
	}, nil
}

func (s *authService) UpdateProfile(ctx *gin.Context, req request.UpdateProfileRequest) (*response.LoginResponse, error) {
	principal, err := s.Authenticate(ctx)
	if err != nil {
		return nil, err
	}
	user := UserService.Get(principal.UserID)
	if user == nil || user.DeletedAt != nil || user.Status != enums.StatusOk {
		return nil, errorsx.UnauthorizedI18n("error.e0256")
	}

	nickname := strings.TrimSpace(req.Nickname)
	if nickname == "" {
		return nil, errorsx.InvalidParamI18n("error.profile.nicknameRequired")
	}
	if len([]rune(nickname)) > 100 {
		return nil, errorsx.InvalidParamI18n("error.profile.nicknameTooLong")
	}
	avatar := strings.TrimSpace(req.Avatar)
	if len(avatar) > 255 {
		return nil, errorsx.InvalidParamI18n("error.profile.avatarTooLong")
	}
	email := utils.NormalizeNullableString(req.Email)
	if email != nil {
		if _, parseErr := mail.ParseAddress(*email); parseErr != nil {
			return nil, errorsx.InvalidParamI18n("error.profile.emailInvalid")
		}
		if existed := UserService.GetByEmail(*email); existed != nil && existed.ID != user.ID {
			return nil, errorsx.InvalidParamI18n("error.e0338")
		}
	}

	if err := repositories.UserRepository.Updates(sqls.DB(), user.ID, map[string]any{
		"nickname":         nickname,
		"avatar":           avatar,
		"email":            email,
		"update_user_id":   user.ID,
		"update_user_name": user.Username,
		"updated_at":       time.Now(),
	}); err != nil {
		return nil, err
	}
	return s.CurrentProfile(ctx)
}

func (s *authService) GetUserRoles(userID int64) ([]models.Role, error) {
	return s.loadUserRoles(sqls.DB(), userID)
}

func (s *authService) GetUserPermissions(userID int64) ([]string, error) {
	return s.loadUserPermissionCodes(sqls.DB(), userID)
}

func (s *authService) issueTokens(ctx *sqls.TxContext, user *models.User, clientIP, userAgent string, authCfg config.AuthConfig) (*response.LoginResponse, error) {
	roles, permissions, err := s.loadUserAuthScope(ctx.Tx, user.ID)
	if err != nil {
		return nil, err
	}

	tokenTTL := s.resolveTokenTTL(authCfg)
	accessToken, err := randomToken(constants.AuthTokenPrefix)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	if err := repositories.LoginSessionRepository.Create(ctx.Tx, &models.LoginSession{
		UserID:     user.ID,
		Token:      accessToken,
		ClientType: constants.ClientTypeAdminWeb,
		ClientIP:   clientIP,
		UserAgent:  userAgent,
		ExpiredAt:  now.Add(tokenTTL),
		LastSeenAt: &now,
		AuditFields: models.AuditFields{
			CreatedAt:      now,
			CreateUserID:   user.ID,
			CreateUserName: user.Username,
			UpdatedAt:      now,
			UpdateUserID:   user.ID,
			UpdateUserName: user.Username,
		},
	}); err != nil {
		return nil, err
	}

	return &response.LoginResponse{
		AccessToken: accessToken,
		ExpiresAt:   now.Add(tokenTTL).Format(time.DateTime),
		User: &response.AuthUserResponse{
			ID:       user.ID,
			Username: user.Username,
			Nickname: user.Nickname,
			Avatar:   user.Avatar,
			Email:    derefString(user.Email),
			UserType: user.UserType,
			Status:   user.Status,
			Roles:    roles,
		},
		Permissions: permissions,
		Roles:       roles,
	}, nil
}

func (s *authService) resolveTokenTTL(authCfg config.AuthConfig) time.Duration {
	tokenTTL := 12 * time.Hour
	if authCfg.TokenTTLHours > 0 {
		tokenTTL = time.Duration(authCfg.TokenTTLHours) * time.Hour
	}
	return tokenTTL
}

func (s *authService) validateSessionToken(token string) (*models.LoginSession, error) {
	return s.validateSessionTokenAt(token, time.Now())
}

func (s *authService) validateSessionTokenAt(token string, now time.Time) (*models.LoginSession, error) {
	if strings.TrimSpace(token) == "" {
		return nil, errorsx.UnauthorizedI18n("error.auth.expired")
	}
	session := LoginSessionService.FindOne(sqls.NewCnd().Eq("token", token))
	if session == nil {
		return nil, errorsx.InvalidTokenI18n("error.e0269")
	}
	if session.RevokedAt != nil {
		return nil, errorsx.InvalidTokenI18n("error.e0267")
	}
	if !now.Before(session.ExpiredAt) {
		return nil, errorsx.InvalidTokenI18n("error.e0268")
	}
	return session, nil
}

func (s *authService) loadUserAuthScope(tx *gorm.DB, userID int64) ([]string, []string, error) {
	roles, permissions, _, err := loadEmployeeAuthScope(tx, userID, time.Now())
	return roles, permissions, err
}

func (s *authService) loadUserRoleCodes(tx *gorm.DB, userID int64) ([]string, error) {
	roles, err := s.loadUserRoles(tx, userID)
	if err != nil {
		return nil, err
	}
	roleCodes := make([]string, 0, len(roles))
	for _, role := range roles {
		roleCodes = append(roleCodes, role.Code)
	}
	return roleCodes, nil
}

func (s *authService) loadUserRoles(tx *gorm.DB, userID int64) ([]models.Role, error) {
	return repositories.EmployeeAuthRepository.UserRoles(tx, userID)
}

type userPermissionOverride = repositories.EmployeeAuthOverride

func rolePermissionCodesQuery(tx *gorm.DB, userID int64) *gorm.DB {
	return repositories.EmployeeAuthRepository.RolePermissionCodesQuery(tx, userID)
}

func activeUserPermissionOverridesQuery(tx *gorm.DB, userID int64, now time.Time) *gorm.DB {
	return repositories.EmployeeAuthRepository.ActiveOverridesQuery(tx, userID, now)
}

func (s *authService) loadUserPermissionCodes(tx *gorm.DB, userID int64) ([]string, error) {
	codes, _, err := loadEmployeePermissionCodes(tx, userID, time.Now())
	return codes, err
}

func (s *authService) extractBearerToken(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func (s *authService) createLoginCredentialLog(principal string, userID int64, success bool, clientIP, userAgent, reason string) error {
	return LoginCredentialLogService.Create(&models.LoginCredentialLog{
		Principal: principal,
		UserID:    userID,
		Success:   success,
		ClientIP:  normalizeClientIP(clientIP),
		UserAgent: userAgent,
		Reason:    reason,
		CreatedAt: time.Now(),
	})
}

// isCredentialLocked reports whether this attempt should be refused before the
// password is even checked.
//
// Both windows are keyed on the client address as well as the username. Keying on
// the username alone - which is what this used to do - means anyone who knows a
// username can lock the real account out for the whole window, from anywhere, as
// often as they like: that is a denial of service dressed up as a protection. The
// principal-and-address window still stops one source grinding on one account, and
// the address-only window across all principals still stops credential stuffing,
// but a legitimate user arriving from a different address is no longer locked out
// by somebody else's failures.
//
// This is only sound because the address itself is trustworthy; see the trusted
// proxy configuration in bootstrap.NewServer.
func (s *authService) isCredentialLocked(principal, clientIP string, authCfg config.AuthConfig) bool {
	lockMinute := authCfg.CredentialLockMinute
	if lockMinute <= 0 {
		lockMinute = 15
	}
	since := time.Now().Add(-time.Duration(lockMinute) * time.Minute)
	ip := normalizeClientIP(clientIP)

	base := func() *sqls.Cnd {
		return sqls.NewCnd().
			Eq("client_ip", ip).
			Eq("success", false).
			NotEq("reason", "credential locked").
			Where("created_at >= ?", since)
	}

	if maxFailedAttempts := authCfg.MaxFailedAttempts; maxFailedAttempts > 0 {
		count := LoginCredentialLogService.Count(base().Eq("principal", normalizeLoginPrincipal(principal)))
		if count >= int64(maxFailedAttempts) {
			return true
		}
	}

	// An undetermined address would otherwise pool every such caller into one
	// bucket and lock them all out together.
	if maxPerIP := authCfg.MaxFailedAttemptsPerIPOrDefault(); maxPerIP > 0 && ip != unknownClientIP {
		if count := LoginCredentialLogService.Count(base()); count >= int64(maxPerIP) {
			return true
		}
	}

	return false
}

// unknownClientIP stands in for an address the server could not determine, so
// those attempts share one bucket on the principal window instead of matching
// nothing at all.
const unknownClientIP = "unknown"

func normalizeClientIP(clientIP string) string {
	if clientIP = strings.TrimSpace(clientIP); clientIP == "" {
		return unknownClientIP
	}
	return clientIP
}

func normalizeLoginPrincipal(principal string) string {
	return strings.ToLower(strings.TrimSpace(principal))
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func randomToken(prefix string) (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf), nil
}
