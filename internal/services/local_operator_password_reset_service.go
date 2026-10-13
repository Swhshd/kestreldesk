package services

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/mlogclub/simple/sqls"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

var (
	errLocalOperatorPasswordResetRejected = errors.New("local operator password reset rejected")
	errLocalOperatorPasswordResetFailed   = errors.New("local operator password reset failed")
)

// LocalOperatorPasswordResetService resets an enabled employee account from a
// local operator command. It is intentionally not exposed through HTTP.
type LocalOperatorPasswordResetService struct {
	invalidator EmployeeRealtimeInvalidator
}

func NewLocalOperatorPasswordResetService(invalidator EmployeeRealtimeInvalidator) *LocalOperatorPasswordResetService {
	return &LocalOperatorPasswordResetService{invalidator: invalidator}
}

func (s *LocalOperatorPasswordResetService) ResetPassword(ctx context.Context, username, password string) error {
	username = strings.TrimSpace(username)
	passwordBytes := []byte(password)
	if username == "" || len(passwordBytes) < 8 || len(passwordBytes) > 72 {
		return errLocalOperatorPasswordResetRejected
	}
	if ctx == nil {
		ctx = context.Background()
	}

	err := sqls.WithTransaction(func(txctx *sqls.TxContext) error {
		if txctx == nil || txctx.Tx == nil {
			return errLocalOperatorPasswordResetFailed
		}
		tx := txctx.Tx.WithContext(ctx)
		var user models.User
		if err := tx.Where(
			"username = ? AND user_type = ? AND status = ? AND deleted_at IS NULL",
			username,
			enums.UserTypeEmployee,
			enums.StatusOk,
		).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errLocalOperatorPasswordResetRejected
			}
			return errLocalOperatorPasswordResetFailed
		}

		passwordHash, err := bcrypt.GenerateFromPassword(passwordBytes, bcrypt.DefaultCost)
		if err != nil {
			return errLocalOperatorPasswordResetRejected
		}
		now := time.Now().UTC()
		if err := repositories.UserRepository.Updates(tx, user.ID, map[string]interface{}{
			"password":         string(passwordHash),
			"password_salt":    "",
			"updated_at":       now,
			"update_user_id":   0,
			"update_user_name": "local-operator",
		}); err != nil {
			return errLocalOperatorPasswordResetFailed
		}
		if err := repositories.PasswordResetTokenRepository.InvalidateUnusedByUser(tx, user.ID, now); err != nil {
			return errLocalOperatorPasswordResetFailed
		}
		if err := repositories.LoginSessionRepository.RevokeActiveByUser(tx, user.ID, 0, "local-operator", now); err != nil {
			return errLocalOperatorPasswordResetFailed
		}
		if s != nil && s.invalidator != nil {
			txctx.RegisterCallback(func() {
				s.invalidator.InvalidateEmployees([]int64{user.ID})
			})
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errLocalOperatorPasswordResetRejected) {
			return errLocalOperatorPasswordResetRejected
		}
		return errLocalOperatorPasswordResetFailed
	}
	return nil
}
