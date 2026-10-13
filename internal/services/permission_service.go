package services

// This file has been modified for the KestrelDesk project.

import (
	"errors"
	"fmt"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"slices"
	"time"

	"github.com/Swhshd/kestreldesk/internal/pkg/httpx/params"

	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

var PermissionService = newPermissionService(employeeRealtime)

func newPermissionService(invalidator EmployeeRealtimeInvalidator) *permissionService {
	return &permissionService{invalidator: invalidator}
}

type permissionService struct {
	invalidator EmployeeRealtimeInvalidator
}

func (s *permissionService) Get(id int64) *models.Permission {
	return repositories.PermissionRepository.Get(sqls.DB(), id)
}

func (s *permissionService) Take(where ...interface{}) *models.Permission {
	return repositories.PermissionRepository.Take(sqls.DB(), where...)
}

func (s *permissionService) Find(cnd *sqls.Cnd) []models.Permission {
	return repositories.PermissionRepository.Find(sqls.DB(), cnd)
}

func (s *permissionService) FindOne(cnd *sqls.Cnd) *models.Permission {
	return repositories.PermissionRepository.FindOne(sqls.DB(), cnd)
}

func (s *permissionService) FindPageByParams(params *params.QueryParams) (list []models.Permission, paging *sqls.Paging) {
	return repositories.PermissionRepository.FindPageByParams(sqls.DB(), params)
}

func (s *permissionService) FindPageByCnd(cnd *sqls.Cnd) (list []models.Permission, paging *sqls.Paging) {
	return repositories.PermissionRepository.FindPageByCnd(sqls.DB(), cnd)
}

func (s *permissionService) Count(cnd *sqls.Cnd) int64 {
	return repositories.PermissionRepository.Count(sqls.DB(), cnd)
}

func (s *permissionService) Create(t *models.Permission) error {
	return repositories.PermissionRepository.Create(sqls.DB(), t)
}

func (s *permissionService) Update(t *models.Permission) error {
	return repositories.PermissionRepository.Update(sqls.DB(), t)
}

func (s *permissionService) Updates(id int64, columns map[string]interface{}) error {
	return repositories.PermissionRepository.Updates(sqls.DB(), id, columns)
}

func (s *permissionService) UpdateColumn(id int64, name string, value interface{}) error {
	return repositories.PermissionRepository.UpdateColumn(sqls.DB(), id, name, value)
}

func (s *permissionService) Delete(id int64) {
	repositories.PermissionRepository.Delete(sqls.DB(), id)
}

func (s *permissionService) SyncBuiltinPermissions() (*response.PermissionSyncResponse, error) {
	result := &response.PermissionSyncResponse{}
	err := sqls.WithTransaction(func(ctx *sqls.TxContext) error {
		permissions := make(map[string]*models.Permission, len(constants.Permissions))
		changedPermissionIDs := make([]int64, 0)
		changedRoleIDs := make([]int64, 0)
		now := time.Now()

		for _, spec := range constants.Permissions {
			var permission models.Permission
			lookupErr := ctx.Tx.Where("code = ?", spec.Code).First(&permission).Error
			if errors.Is(lookupErr, gorm.ErrRecordNotFound) {
				permission = models.Permission{
					Name: spec.Name, Code: spec.Code, Type: spec.Type, GroupName: spec.GroupName,
					Method: spec.Method, APIPath: spec.APIPath, SortNo: spec.SortNo,
					Status: enums.StatusOk, IsBuiltin: true,
					AuditFields: systemPermissionAuditFields(now),
				}
				if err := repositories.PermissionRepository.Create(ctx.Tx, &permission); err != nil {
					return err
				}
				result.Created++
			} else {
				if lookupErr != nil {
					return lookupErr
				}
				if permission.Status != enums.StatusOk {
					changedPermissionIDs = append(changedPermissionIDs, permission.ID)
				}
				if err := repositories.PermissionRepository.Updates(ctx.Tx, permission.ID, map[string]any{
					"name": spec.Name, "type": spec.Type, "group_name": spec.GroupName,
					"method": spec.Method, "api_path": spec.APIPath, "sort_no": spec.SortNo,
					"status": enums.StatusOk, "is_builtin": true,
					"update_user_id":   constants.SystemAuditUserID,
					"update_user_name": constants.SystemAuditUserName, "updated_at": now,
				}); err != nil {
					return err
				}
				if err := ctx.Tx.First(&permission, permission.ID).Error; err != nil {
					return err
				}
				result.Updated++
			}
			permissions[spec.Code] = &permission
		}

		for roleCode, specs := range constants.RolePermissions {
			var role models.Role
			if err := ctx.Tx.Where("code = ?", roleCode).First(&role).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return fmt.Errorf("builtin role not found: %s", roleCode)
				}
				return err
			}
			for _, spec := range specs {
				permission := permissions[spec.Code]
				if permission == nil {
					return fmt.Errorf("builtin permission not found: %s", spec.Code)
				}
				var relation models.RolePermission
				lookupErr := ctx.Tx.Where("role_id = ? AND permission_id = ?", role.ID, permission.ID).First(&relation).Error
				if lookupErr == nil {
					continue
				}
				if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
					return lookupErr
				}
				if err := repositories.RolePermissionRepository.Create(ctx.Tx, &models.RolePermission{
					RoleID: role.ID, PermissionID: permission.ID,
					AuditFields: systemPermissionAuditFields(now),
				}); err != nil {
					return err
				}
				changedRoleIDs = append(changedRoleIDs, role.ID)
				result.RolePermissionsAdded++
			}
		}

		// Permission sync historically only added grants. Remove only the
		// obsolete ticket.assign link from the system-owned cs_user role; custom
		// roles and every other role grant remain untouched.
		var csUserRole models.Role
		if err := ctx.Tx.Where("code = ?", constants.RoleCodeCsUser).First(&csUserRole).Error; err != nil {
			return err
		}
		if csUserRole.IsSystem {
			assignPermission := permissions[constants.PermissionTicketAssign.Code]
			if assignPermission == nil {
				return fmt.Errorf("builtin permission not found: %s", constants.PermissionTicketAssign.Code)
			}
			deleted := ctx.Tx.Where("role_id = ? AND permission_id = ?", csUserRole.ID, assignPermission.ID).
				Delete(&models.RolePermission{})
			if deleted.Error != nil {
				return deleted.Error
			}
			if deleted.RowsAffected > 0 {
				changedRoleIDs = append(changedRoleIDs, csUserRole.ID)
				result.RolePermissionsRemoved += int(deleted.RowsAffected)
			}
		}
		if len(changedPermissionIDs)+len(changedRoleIDs) > 0 {
			slices.Sort(changedPermissionIDs)
			changedPermissionIDs = slices.Compact(changedPermissionIDs)
			slices.Sort(changedRoleIDs)
			changedRoleIDs = slices.Compact(changedRoleIDs)
			ctx.RegisterCallback(func() {
				invalidateCommittedEmployeeAuthzImpact(s.invalidator, employeeAuthzImpact{RoleIDs: changedRoleIDs, PermissionIDs: changedPermissionIDs}, "builtin permission sync")
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func systemPermissionAuditFields(now time.Time) models.AuditFields {
	return models.AuditFields{
		CreatedAt: now, CreateUserID: constants.SystemAuditUserID, CreateUserName: constants.SystemAuditUserName,
		UpdatedAt: now, UpdateUserID: constants.SystemAuditUserID, UpdateUserName: constants.SystemAuditUserName,
	}
}
