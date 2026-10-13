package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"golang.org/x/crypto/bcrypt"
)

func TestLocalOperatorPasswordResetChangesPasswordAndRevokesCredentials(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	invalidator := &passwordRecoveryInvalidatorSpy{}
	service := NewLocalOperatorPasswordResetService(invalidator)
	operator := createPasswordRecoveryUser(t, db, "synthetic-operator", "", enums.UserTypeEmployee, enums.StatusOk, "synthetic-old-password")
	active := createPasswordRecoveryLoginSession(t, db, operator.ID, "synthetic-active-session")
	revoked := createPasswordRecoveryLoginSession(t, db, operator.ID, "synthetic-revoked-session")
	revokedAt := time.Now().Add(-time.Minute)
	if err := db.Model(&models.LoginSession{}).Where("id = ?", revoked.ID).Update("revoked_at", revokedAt).Error; err != nil {
		t.Fatalf("mark prior session revoked: %v", err)
	}
	other := createPasswordRecoveryUser(t, db, "synthetic-other-operator", "", enums.UserTypeEmployee, enums.StatusOk, "synthetic-other-password")
	otherSession := createPasswordRecoveryLoginSession(t, db, other.ID, "synthetic-other-session")
	resetToken := insertPasswordResetToken(t, db, operator.ID, "dashboard", "synthetic-reset-token", time.Now().Add(time.Hour), nil)
	usedAt := time.Now().Add(-time.Minute)
	usedToken := insertPasswordResetToken(t, db, operator.ID, "dashboard", "synthetic-used-token", time.Now().Add(time.Hour), &usedAt)

	if err := service.ResetPassword(context.Background(), operator.Username, "synthetic-new-password"); err != nil {
		t.Fatalf("reset local operator password: %v", err)
	}

	persisted := repositories.UserRepository.Get(db, operator.ID)
	if bcrypt.CompareHashAndPassword([]byte(persisted.Password), []byte("synthetic-old-password")) == nil {
		t.Fatal("old password remained valid")
	}
	if bcrypt.CompareHashAndPassword([]byte(persisted.Password), []byte("synthetic-new-password")) != nil {
		t.Fatal("new password was not stored with the existing bcrypt format")
	}
	if current := repositories.LoginSessionRepository.Get(db, active.ID); current == nil || current.RevokedAt == nil {
		t.Fatal("active operator session was not revoked")
	}
	if current := repositories.LoginSessionRepository.Get(db, revoked.ID); current == nil || current.RevokedAt == nil || !current.RevokedAt.Equal(revokedAt) {
		t.Fatal("previously revoked session history was changed")
	}
	if current := repositories.LoginSessionRepository.Get(db, otherSession.ID); current == nil || current.RevokedAt != nil {
		t.Fatal("another operator's session was changed")
	}
	var invalidated models.PasswordResetToken
	if err := db.First(&invalidated, resetToken.ID).Error; err != nil || invalidated.UsedAt == nil {
		t.Fatal("outstanding password recovery token was not invalidated")
	}
	var preserved models.PasswordResetToken
	if err := db.First(&preserved, usedToken.ID).Error; err != nil || preserved.UsedAt == nil || !preserved.UsedAt.Equal(usedAt) {
		t.Fatal("previously used password recovery token was changed")
	}
	if calls := invalidator.employeeCalls(); len(calls) != 1 || len(calls[0]) != 1 || calls[0][0] != operator.ID {
		t.Fatal("employee realtime state was not invalidated exactly after the account reset")
	}
}

func TestLocalOperatorPasswordResetRejectsUnknownAndCustomerAccountsWithoutMutation(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	service := NewLocalOperatorPasswordResetService(&passwordRecoveryInvalidatorSpy{})
	operator := createPasswordRecoveryUser(t, db, "synthetic-operator", "", enums.UserTypeEmployee, enums.StatusOk, "synthetic-old-password")
	active := createPasswordRecoveryLoginSession(t, db, operator.ID, "synthetic-active-session")
	customer := createPasswordRecoveryUser(t, db, "synthetic-customer", "", enums.UserTypeUser, enums.StatusOk, "synthetic-customer-password")

	if err := service.ResetPassword(context.Background(), "missing-operator", "synthetic-new-password"); err == nil {
		t.Fatal("unknown account was accepted")
	}
	if err := service.ResetPassword(context.Background(), customer.Username, "synthetic-new-password"); err == nil {
		t.Fatal("customer account was accepted as a local operator")
	}

	unchangedOperator := repositories.UserRepository.Get(db, operator.ID)
	if bcrypt.CompareHashAndPassword([]byte(unchangedOperator.Password), []byte("synthetic-old-password")) != nil {
		t.Fatal("rejected reset changed the unrelated operator credential")
	}
	unchangedCustomer := repositories.UserRepository.Get(db, customer.ID)
	if bcrypt.CompareHashAndPassword([]byte(unchangedCustomer.Password), []byte("synthetic-customer-password")) != nil {
		t.Fatal("rejected reset changed customer credentials")
	}
	if current := repositories.LoginSessionRepository.Get(db, active.ID); current == nil || current.RevokedAt != nil {
		t.Fatal("rejected reset mutated an operator session")
	}
}

func TestLocalOperatorPasswordResetRejectsEmptyPasswordWithoutMutation(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	service := NewLocalOperatorPasswordResetService(&passwordRecoveryInvalidatorSpy{})
	operator := createPasswordRecoveryUser(t, db, "synthetic-operator", "", enums.UserTypeEmployee, enums.StatusOk, "synthetic-old-password")
	active := createPasswordRecoveryLoginSession(t, db, operator.ID, "synthetic-active-session")

	if err := service.ResetPassword(context.Background(), operator.Username, ""); err == nil {
		t.Fatal("empty password was accepted")
	}
	unchanged := repositories.UserRepository.Get(db, operator.ID)
	if bcrypt.CompareHashAndPassword([]byte(unchanged.Password), []byte("synthetic-old-password")) != nil {
		t.Fatal("empty-password rejection changed the password hash")
	}
	if current := repositories.LoginSessionRepository.Get(db, active.ID); current == nil || current.RevokedAt != nil {
		t.Fatal("empty-password rejection mutated an operator session")
	}
}

func TestLocalOperatorPasswordResetRollsBackOnResetTokenFailure(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	invalidator := &passwordRecoveryInvalidatorSpy{}
	service := NewLocalOperatorPasswordResetService(invalidator)
	operator := createPasswordRecoveryUser(t, db, "synthetic-operator", "", enums.UserTypeEmployee, enums.StatusOk, "synthetic-old-password")
	active := createPasswordRecoveryLoginSession(t, db, operator.ID, "synthetic-active-session")
	resetToken := insertPasswordResetToken(t, db, operator.ID, "dashboard", "synthetic-reset-token", time.Now().Add(time.Hour), nil)
	if err := db.Exec("CREATE TRIGGER reject_local_reset_token_update BEFORE UPDATE ON t_password_reset_token BEGIN SELECT RAISE(ABORT, 'synthetic failure'); END").Error; err != nil {
		t.Fatalf("install rollback fixture: %v", err)
	}

	err := service.ResetPassword(context.Background(), operator.Username, "synthetic-new-password")
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatal("reset-token write failure did not abort the transaction")
	}
	unchanged := repositories.UserRepository.Get(db, operator.ID)
	if bcrypt.CompareHashAndPassword([]byte(unchanged.Password), []byte("synthetic-old-password")) != nil {
		t.Fatal("failed transaction changed the password")
	}
	if current := repositories.LoginSessionRepository.Get(db, active.ID); current == nil || current.RevokedAt != nil {
		t.Fatal("failed transaction revoked the active session")
	}
	var unchangedToken models.PasswordResetToken
	if err := db.First(&unchangedToken, resetToken.ID).Error; err != nil || unchangedToken.UsedAt != nil {
		t.Fatal("failed transaction invalidated the outstanding reset token")
	}
	if calls := invalidator.employeeCalls(); len(calls) != 0 {
		t.Fatal("failed transaction emitted realtime invalidation")
	}
}
