package services

// This file has been modified for the KestrelDesk project.

import (
	"errors"
	"log/slog"
	"slices"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/repositories"

	"github.com/Swhshd/kestreldesk/internal/pkg/httpx/params"

	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
)

var UserPermissionService = newUserPermissionService(employeeRealtime)

func newUserPermissionService(invalidator EmployeeRealtimeInvalidator) *userPermissionService {
	return &userPermissionService{invalidator: invalidator}
}

type userPermissionService struct {
	invalidator EmployeeRealtimeInvalidator
}

func (s *userPermissionService) Get(id int64) *models.UserPermission {
	return repositories.UserPermissionRepository.Get(sqls.DB(), id)
}

func (s *userPermissionService) Take(where ...interface{}) *models.UserPermission {
	return repositories.UserPermissionRepository.Take(sqls.DB(), where...)
}

func (s *userPermissionService) Find(cnd *sqls.Cnd) []models.UserPermission {
	return repositories.UserPermissionRepository.Find(sqls.DB(), cnd)
}

func (s *userPermissionService) FindOne(cnd *sqls.Cnd) *models.UserPermission {
	return repositories.UserPermissionRepository.FindOne(sqls.DB(), cnd)
}

func (s *userPermissionService) FindPageByParams(params *params.QueryParams) (list []models.UserPermission, paging *sqls.Paging) {
	return repositories.UserPermissionRepository.FindPageByParams(sqls.DB(), params)
}

func (s *userPermissionService) FindPageByCnd(cnd *sqls.Cnd) (list []models.UserPermission, paging *sqls.Paging) {
	return repositories.UserPermissionRepository.FindPageByCnd(sqls.DB(), cnd)
}

func (s *userPermissionService) Count(cnd *sqls.Cnd) int64 {
	return repositories.UserPermissionRepository.Count(sqls.DB(), cnd)
}

func (s *userPermissionService) Create(t *models.UserPermission) error {
	if err := repositories.UserPermissionRepository.Create(sqls.DB(), t); err != nil {
		return err
	}
	if t != nil && t.ID > 0 {
		s.invalidateOwners(t.UserID)
	}
	return nil
}

func (s *userPermissionService) Update(t *models.UserPermission) error {
	db := sqls.DB()
	var old *models.UserPermission
	if t != nil && t.ID > 0 {
		var err error
		old, err = checkedUserPermission(db, t.ID)
		if err != nil {
			return err
		}
	}
	if err := repositories.UserPermissionRepository.Update(db, t); err != nil {
		return err
	}
	ids := make([]int64, 0, 2)
	if old != nil {
		ids = append(ids, old.UserID)
	}
	if t != nil && t.ID > 0 {
		updated, err := repositories.UserPermissionRepository.GetChecked(db, t.ID)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			s.invalidateAllAfterReloadError("user permission update", err)
			return nil
		}
		if updated != nil {
			ids = append(ids, updated.UserID)
		}
	}
	s.invalidateOwners(ids...)
	return nil
}

func (s *userPermissionService) Updates(id int64, columns map[string]interface{}) error {
	db := sqls.DB()
	old, err := checkedUserPermission(db, id)
	if err != nil {
		return err
	}
	if err := repositories.UserPermissionRepository.Updates(db, id, columns); err != nil {
		return err
	}
	if old == nil {
		return nil
	}
	updated, err := repositories.UserPermissionRepository.GetChecked(db, id)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		s.invalidateAllAfterReloadError("user permission updates", err)
		return nil
	}
	ids := []int64{old.UserID}
	if updated != nil {
		ids = append(ids, updated.UserID)
	}
	s.invalidateOwners(ids...)
	return nil
}

func (s *userPermissionService) UpdateColumn(id int64, name string, value interface{}) error {
	db := sqls.DB()
	old, err := checkedUserPermission(db, id)
	if err != nil {
		return err
	}
	if err := repositories.UserPermissionRepository.UpdateColumn(db, id, name, value); err != nil {
		return err
	}
	if old == nil {
		return nil
	}
	updated, err := repositories.UserPermissionRepository.GetChecked(db, id)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		s.invalidateAllAfterReloadError("user permission update column", err)
		return nil
	}
	ids := []int64{old.UserID}
	if updated != nil {
		ids = append(ids, updated.UserID)
	}
	s.invalidateOwners(ids...)
	return nil
}

func (s *userPermissionService) Delete(id int64) error {
	db := sqls.DB()
	old, err := checkedUserPermission(db, id)
	if err != nil {
		return err
	}
	if err := repositories.UserPermissionRepository.Delete(db, id); err != nil {
		return err
	}
	if old != nil {
		s.invalidateOwners(old.UserID)
	}
	return nil
}

func checkedUserPermission(db *gorm.DB, id int64) (*models.UserPermission, error) {
	row, err := repositories.UserPermissionRepository.GetChecked(db, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return row, err
}

func (s *userPermissionService) invalidateOwners(ids ...int64) {
	if s.invalidator == nil {
		panic("employee realtime invalidator is required")
	}
	ids = slices.DeleteFunc(ids, func(id int64) bool { return id <= 0 })
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) > 0 {
		s.invalidator.InvalidateEmployees(ids)
	}
}

func (s *userPermissionService) invalidateAllAfterReloadError(mutation string, err error) {
	slog.Error("employee authorization impact resolution failed after user permission write", "mutation", mutation, "error", err)
	if s.invalidator != nil {
		s.invalidator.InvalidateAllEmployees()
		return
	}
	panic("employee realtime invalidator is required")
}
