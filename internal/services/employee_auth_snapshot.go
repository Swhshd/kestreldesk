package services

import (
	"sort"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/repositories"

	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

func loadEmployeeAuthScope(db *gorm.DB, userID int64, now time.Time) (roles []string, permissions []string, nextAuthzChangeAt *time.Time, err error) {
	rows, err := repositories.EmployeeAuthRepository.UserRoles(db, userID)
	if err != nil {
		return nil, nil, nil, err
	}
	roles = make([]string, 0, len(rows))
	for _, r := range rows {
		roles = append(roles, r.Code)
	}
	permissions, nextAuthzChangeAt, err = loadEmployeePermissionCodes(db, userID, now)
	return
}

func loadEmployeePermissionCodes(tx *gorm.DB, userID int64, now time.Time) ([]string, *time.Time, error) {
	permissionRows := make([]struct {
		Code string
	}, 0)
	if err := repositories.EmployeeAuthRepository.RolePermissionCodesQuery(tx, userID).Scan(&permissionRows).Error; err != nil {
		return nil, nil, err
	}

	permissionCodes := make([]string, 0, len(permissionRows))
	for _, permission := range permissionRows {
		permissionCodes = append(permissionCodes, permission.Code)
	}

	overrideRows := make([]repositories.EmployeeAuthOverride, 0)
	if err := repositories.EmployeeAuthRepository.ActiveOverridesQuery(tx, userID, now).Scan(&overrideRows).Error; err != nil {
		return nil, nil, err
	}

	permissionSet := make(map[string]bool, len(permissionCodes))
	for _, code := range permissionCodes {
		permissionSet[code] = true
	}
	var nextAuthzChangeAt *time.Time
	for _, override := range overrideRows {
		if override.ExpiredAt != nil && (nextAuthzChangeAt == nil || override.ExpiredAt.Before(*nextAuthzChangeAt)) {
			expiry := *override.ExpiredAt
			nextAuthzChangeAt = &expiry
		}
		if override.Effect < 0 {
			delete(permissionSet, override.Code)
			continue
		}
		permissionSet[override.Code] = true
	}

	permissionCodes = permissionCodes[:0]
	for code := range permissionSet {
		permissionCodes = append(permissionCodes, code)
	}
	sort.Strings(permissionCodes)
	return permissionCodes, nextAuthzChangeAt, nil
}

type employeeAuthStateReader struct{}

func (employeeAuthStateReader) ReadEmployeeSession(loginSessionID int64, now time.Time) (EmployeeSessionSnapshot, error) {
	db := sqls.DB()
	session, err := repositories.EmployeeAuthRepository.LoginSessionByID(db, loginSessionID)
	if err != nil {
		return EmployeeSessionSnapshot{}, err
	}
	if session.RevokedAt != nil {
		return EmployeeSessionSnapshot{}, errorsx.InvalidTokenI18n("error.e0267")
	}
	if !now.Before(session.ExpiredAt) {
		return EmployeeSessionSnapshot{}, errorsx.InvalidTokenI18n("error.e0268")
	}
	user, err := repositories.EmployeeAuthRepository.UserByID(db, session.UserID)
	if err != nil {
		return EmployeeSessionSnapshot{}, err
	}
	if user.Status != enums.StatusOk || user.DeletedAt != nil {
		return EmployeeSessionSnapshot{}, errorsx.UnauthorizedI18n("error.e0256")
	}
	roles, permissions, next, err := loadEmployeeAuthScope(db, user.ID, now)
	if err != nil {
		return EmployeeSessionSnapshot{}, err
	}
	return EmployeeSessionSnapshot{EmployeeID: user.ID, LoginSessionID: session.ID, LoginSessionExpiresAt: session.ExpiredAt, Principal: employeeAuthPrincipal(user, roles, permissions), NextAuthzChangeAt: next}, nil
}

func employeeAuthPrincipal(user *models.User, roles, permissions []string) *dto.AuthPrincipal {
	return &dto.AuthPrincipal{UserID: user.ID, Username: user.Username, Nickname: user.Nickname, Avatar: user.Avatar, UserType: user.UserType, Status: user.Status, Roles: roles, Permissions: permissions}
}
