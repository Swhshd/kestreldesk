package repositories

import (
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"

	"gorm.io/gorm"
)

type EmployeeAuthOverride struct {
	Code      string
	Effect    int
	ExpiredAt *time.Time
}
type employeeAuthRepository struct{}

var EmployeeAuthRepository = newEmployeeAuthRepository()

func newEmployeeAuthRepository() *employeeAuthRepository { return &employeeAuthRepository{} }

func (r *employeeAuthRepository) LoginSessionByID(db *gorm.DB, id int64) (*models.LoginSession, error) {
	var row models.LoginSession
	if err := db.First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *employeeAuthRepository) EmployeeLoginSessionActive(db *gorm.DB, employeeID, loginSessionID int64, now time.Time) (bool, error) {
	var row struct {
		UserID    int64
		ExpiredAt time.Time
		RevokedAt *time.Time
	}
	if err := db.Model(&models.LoginSession{}).
		Select("user_id", "expired_at", "revoked_at").
		First(&row, loginSessionID).Error; err != nil {
		return false, err
	}
	return row.UserID == employeeID && row.RevokedAt == nil && now.Before(row.ExpiredAt), nil
}

func (r *employeeAuthRepository) UserByID(db *gorm.DB, id int64) (*models.User, error) {
	var row models.User
	if err := db.First(&row, id).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *employeeAuthRepository) UserRoles(tx *gorm.DB, userID int64) ([]models.Role, error) {
	roles := make([]models.Role, 0)
	if err := tx.
		Table("t_role AS r").
		Select("r.*").
		Joins("JOIN t_user_role AS ur ON ur.role_id = r.id").
		Where("ur.user_id = ? AND r.status = ?", userID, enums.StatusOk).
		Order("r.sort_no ASC, r.id ASC").
		Scan(&roles).Error; err != nil {
		return nil, err
	}

	return roles, nil
}

func (r *employeeAuthRepository) RolePermissionCodesQuery(tx *gorm.DB, userID int64) *gorm.DB {
	return tx.Table("t_permission AS p").
		Select("DISTINCT p.code").
		Joins("JOIN t_role_permission AS rp ON rp.permission_id = p.id").
		Joins("JOIN t_user_role AS ur ON ur.role_id = rp.role_id").
		Joins("JOIN t_role AS r ON r.id = rp.role_id").
		Where("ur.user_id = ?", userID).
		Where("p.status = ?", enums.StatusOk).
		Where("r.status = ?", enums.StatusOk)
}

func (r *employeeAuthRepository) ActiveOverridesQuery(tx *gorm.DB, userID int64, now time.Time) *gorm.DB {
	return tx.Table("t_user_permission AS up").
		Select("p.code, up.effect, up.expired_at").
		Joins("JOIN t_permission AS p ON p.id = up.permission_id").
		Where("up.user_id = ? AND (up.expired_at IS NULL OR up.expired_at > ?)", userID, now).
		Where("p.status = ?", enums.StatusOk)
}

// CurrentRoleMemberIDs reads the relationship after the caller's authorization
// mutation has committed. It intentionally does not filter role/user status:
// the mutation itself may have disabled the role or changed its membership.
func (r *employeeAuthRepository) CurrentRoleMemberIDs(db *gorm.DB, roleIDs []int64) ([]int64, error) {
	if len(roleIDs) == 0 {
		return []int64{}, nil
	}
	ids := make([]int64, 0)
	err := db.Model(&models.UserRole{}).Distinct("user_id").Where("role_id IN ?", roleIDs).Order("user_id ASC").Pluck("user_id", &ids).Error
	return ids, err
}

// CurrentPermissionRoleIDs returns current role links regardless of role or
// permission status so disabling a permission still finds its previous holders.
func (r *employeeAuthRepository) CurrentPermissionRoleIDs(db *gorm.DB, permissionIDs []int64) ([]int64, error) {
	if len(permissionIDs) == 0 {
		return []int64{}, nil
	}
	ids := make([]int64, 0)
	err := db.Model(&models.RolePermission{}).Distinct("role_id").Where("permission_id IN ?", permissionIDs).Order("role_id ASC").Pluck("role_id", &ids).Error
	return ids, err
}

// CurrentDirectOverrideUserIDs returns current direct owners regardless of
// permission status or override expiry. A permission disable must invalidate
// users whose direct override is no longer effective as well.
func (r *employeeAuthRepository) CurrentDirectOverrideUserIDs(db *gorm.DB, permissionIDs []int64) ([]int64, error) {
	if len(permissionIDs) == 0 {
		return []int64{}, nil
	}
	ids := make([]int64, 0)
	err := db.Model(&models.UserPermission{}).Distinct("user_id").Where("permission_id IN ?", permissionIDs).Order("user_id ASC").Pluck("user_id", &ids).Error
	return ids, err
}
