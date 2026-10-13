package services

// This file has been modified for the KestrelDesk project.

import (
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"time"

	"github.com/Swhshd/kestreldesk/internal/pkg/httpx/params"

	"github.com/mlogclub/simple/sqls"
)

var LoginSessionService = newLoginSessionService(employeeRealtime)

func newLoginSessionService(invalidator EmployeeRealtimeInvalidator) *loginSessionService {
	return &loginSessionService{invalidator: invalidator}
}

type loginSessionService struct {
	invalidator EmployeeRealtimeInvalidator
}

func (s *loginSessionService) Get(id int64) *models.LoginSession {
	return repositories.LoginSessionRepository.Get(sqls.DB(), id)
}

func (s *loginSessionService) Take(where ...interface{}) *models.LoginSession {
	return repositories.LoginSessionRepository.Take(sqls.DB(), where...)
}

func (s *loginSessionService) Find(cnd *sqls.Cnd) []models.LoginSession {
	return repositories.LoginSessionRepository.Find(sqls.DB(), cnd)
}

func (s *loginSessionService) FindOne(cnd *sqls.Cnd) *models.LoginSession {
	return repositories.LoginSessionRepository.FindOne(sqls.DB(), cnd)
}

func (s *loginSessionService) FindPageByParams(params *params.QueryParams) (list []models.LoginSession, paging *sqls.Paging) {
	return repositories.LoginSessionRepository.FindPageByParams(sqls.DB(), params)
}

func (s *loginSessionService) FindPageByCnd(cnd *sqls.Cnd) (list []models.LoginSession, paging *sqls.Paging) {
	return repositories.LoginSessionRepository.FindPageByCnd(sqls.DB(), cnd)
}

func (s *loginSessionService) Count(cnd *sqls.Cnd) int64 {
	return repositories.LoginSessionRepository.Count(sqls.DB(), cnd)
}

func (s *loginSessionService) Create(t *models.LoginSession) error {
	return repositories.LoginSessionRepository.Create(sqls.DB(), t)
}

func (s *loginSessionService) Update(t *models.LoginSession) error {
	return repositories.LoginSessionRepository.Update(sqls.DB(), t)
}

func (s *loginSessionService) Updates(id int64, columns map[string]interface{}) error {
	return repositories.LoginSessionRepository.Updates(sqls.DB(), id, columns)
}

func (s *loginSessionService) UpdateColumn(id int64, name string, value interface{}) error {
	return repositories.LoginSessionRepository.UpdateColumn(sqls.DB(), id, name, value)
}

func (s *loginSessionService) Delete(id int64) {
	repositories.LoginSessionRepository.Delete(sqls.DB(), id)
}

func (s *loginSessionService) Revoke(id int64, operatorID int64, operatorName string) error {
	session := s.Get(id)
	if session == nil {
		return errorsx.InvalidParamI18n("error.e0116")
	}
	now := time.Now()
	if err := s.Updates(id, map[string]any{
		"revoked_at":       now,
		"update_user_id":   operatorID,
		"update_user_name": operatorName,
		"updated_at":       now,
	}); err != nil {
		return err
	}
	s.invalidator.InvalidateLoginSession(id)
	return nil
}

func (s *loginSessionService) RevokeByUser(userID int64, operatorID int64, operatorName string) error {
	now := time.Now()
	if err := sqls.DB().Model(&models.LoginSession{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Updates(map[string]any{
			"revoked_at":       now,
			"update_user_id":   operatorID,
			"update_user_name": operatorName,
			"updated_at":       now,
		}).Error; err != nil {
		return err
	}
	s.invalidator.InvalidateEmployees([]int64{userID})
	return nil
}
