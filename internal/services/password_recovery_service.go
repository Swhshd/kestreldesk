package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Swhshd/kestreldesk/internal/email"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/pkg/i18nx"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/mlogclub/simple/sqls"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	PasswordRecoverySurfaceDashboard = "dashboard"
	PasswordRecoverySurfaceSupport   = "support"

	passwordResetDefaultTTL             = 30 * time.Minute
	passwordResetTokenBytes             = 32
	passwordResetMailWorkerCount        = 2
	passwordResetMailQueueCapacity      = 64
	passwordResetRequestMinimumDuration = 250 * time.Millisecond
)

var PasswordRecoveryService = newPasswordRecoveryService(nil, employeeRealtime)

type passwordRecoveryService struct {
	mailer         email.Client
	mailerFactory  func(email.ClientConfig) email.Client
	invalidator    EmployeeRealtimeInvalidator
	passwordHasher func([]byte, int) ([]byte, error)
	now            func() time.Time
}

func newPasswordRecoveryService(mailer email.Client, invalidator EmployeeRealtimeInvalidator) *passwordRecoveryService {
	return &passwordRecoveryService{
		mailer:         mailer,
		mailerFactory:  email.NewClient,
		invalidator:    invalidator,
		passwordHasher: bcrypt.GenerateFromPassword,
		now:            time.Now,
	}
}

var (
	passwordResetMailJobs     = make(chan *passwordResetMailJob, passwordResetMailQueueCapacity)
	passwordResetMailWorkerMu = &sync.Once{}
)

type passwordResetMailJob struct {
	mailer      email.Client
	params      email.SendEmailParams
	timeout     time.Duration
	tokenHash   string
	expiresAt   time.Time
	ready       chan bool
	onDelivered func()
}

func enqueuePasswordResetMail(job *passwordResetMailJob) bool {
	passwordResetMailWorkerMu.Do(func() {
		for i := 0; i < passwordResetMailWorkerCount; i++ {
			go passwordResetMailWorker()
		}
	})
	select {
	case passwordResetMailJobs <- job:
		return true
	default:
		return false
	}
}

func passwordResetMailWorker() {
	for job := range passwordResetMailJobs {
		processPasswordResetMailJob(job)
	}
}

func processPasswordResetMailJob(job *passwordResetMailJob) {
	if job == nil || !<-job.ready {
		return
	}
	if !job.expiresAt.After(time.Now().UTC()) {
		markPasswordResetMailTokenUsed(job.tokenHash)
		return
	}
	mailCtx, cancel := context.WithTimeout(context.Background(), job.timeout)
	sendErr := job.mailer.SendEmail(mailCtx, job.params)
	cancel()
	if sendErr != nil {
		markPasswordResetMailTokenUsed(job.tokenHash)
		slog.Error("password recovery email delivery failed")
		return
	}
	if job.onDelivered != nil {
		job.onDelivered()
	}
}

func markPasswordResetMailTokenUsed(tokenHash string) {
	if db := sqls.DB(); db != nil {
		_ = repositories.PasswordResetTokenRepository.MarkUsedByHash(db, tokenHash, time.Now().UTC())
	}
}

// RequestReset returns the same result for eligible and ineligible accounts.
// The source IP is accepted for callers that apply request throttling at the
// boundary; eligibility and delivery remain independent of request headers.
func (s *passwordRecoveryService) RequestReset(ctx context.Context, identifier, surface, sourceIP string) error {
	return s.RequestResetWithDeliveryCallback(ctx, identifier, surface, sourceIP, nil)
}

// RequestResetWithDeliveryCallback keeps delivery success internal to the mail
// worker. The callback runs only after a queued SendEmail call succeeds.
func (s *passwordRecoveryService) RequestResetWithDeliveryCallback(ctx context.Context, identifier, surface, sourceIP string, onDelivered func()) error {
	var delivered func(int64)
	if onDelivered != nil {
		delivered = func(int64) { onDelivered() }
	}
	return s.requestResetWithAdmission(ctx, identifier, surface, sourceIP, nil, delivered)
}

// RequestResetWithAdmissionCallback invokes admit with the canonical user ID
// only after eligibility and usable email checks, but before token creation or
// queueing. The delivery callback receives that same ID only after SendEmail
// succeeds in the asynchronous worker.
func (s *passwordRecoveryService) RequestResetWithAdmissionCallback(ctx context.Context, identifier, surface, sourceIP string, admit func(int64) (func(), bool), onDelivered func(int64)) error {
	return s.requestResetWithAdmission(ctx, identifier, surface, sourceIP, admit, onDelivered)
}

func (s *passwordRecoveryService) requestResetWithAdmission(ctx context.Context, identifier, surface, sourceIP string, admit func(int64) (func(), bool), onDelivered func(int64)) error {
	_ = sourceIP
	requestStarted := time.Now()
	defer waitForPasswordRecoveryRequestFloor(requestStarted)
	passwordRecoveryDummyWork()

	cfg := config.GetCurrent()
	if cfg == nil || !cfg.Email.Enabled || !validPasswordResetSurface(surface) {
		return nil
	}
	baseURL, err := parsePasswordResetBaseURL(cfg.Email.PasswordResetBaseURL)
	if err != nil || strings.TrimSpace(cfg.Email.FromAddress) == "" {
		return nil
	}

	db := sqls.DB()
	if db == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	db = db.WithContext(ctx)

	user := findPasswordRecoveryUser(db, identifier)
	if !passwordRecoveryUserEligible(db, user, surface) {
		return nil
	}
	recipient, recipientName, ok := usablePasswordRecoveryAddress(user.Email)
	if !ok {
		return nil
	}
	var releaseAdmission func()
	admissionCommitted := false
	if admit != nil {
		admitted := false
		releaseAdmission, admitted = admit(user.ID)
		if !admitted {
			if releaseAdmission != nil {
				releaseAdmission()
			}
			return nil
		}
	}
	if releaseAdmission != nil {
		defer func() {
			if !admissionCommitted {
				releaseAdmission()
			}
		}()
	}

	rawToken := make([]byte, passwordResetTokenBytes)
	if _, err := rand.Read(rawToken); err != nil {
		slog.Error("password recovery request preparation failed")
		return nil
	}
	token := base64.RawURLEncoding.EncodeToString(rawToken)
	tokenHash := hashPasswordResetToken(token)
	now := s.currentTime()
	ttl := time.Duration(cfg.Email.PasswordResetTTLMinutes) * time.Minute
	if ttl <= 0 {
		ttl = passwordResetDefaultTTL
	}

	link := *baseURL
	link.Path = strings.TrimRight(link.Path, "/") + "/" + surface + "/reset-password"
	link.RawPath = ""
	query := link.Query()
	query.Set("token", token)
	link.RawQuery = query.Encode()
	link.ForceQuery = false

	mailer := s.mailer
	if mailer == nil {
		factory := s.mailerFactory
		if factory == nil {
			factory = email.NewClient
		}
		mailer = factory(email.ClientConfig{
			Provider:     email.DeliveryProvider(strings.TrimSpace(cfg.Email.Provider)),
			APIKey:       cfg.Email.APIKey,
			SMTPHost:     cfg.Email.SMTPHost,
			SMTPPort:     cfg.Email.SMTPPort,
			SMTPUser:     cfg.Email.SMTPUser,
			SMTPPassword: cfg.Email.SMTPPassword,
			SMTPUseTLS:   cfg.Email.SMTPUseTLS,
			SMTPSecurity: cfg.Email.SMTPSecurity,
			SMTPTimeout:  cfg.Email.SMTPTimeout(),
		})
	}
	params := email.SendEmailParams{
		FromEmail: strings.TrimSpace(cfg.Email.FromAddress),
		FromName:  strings.TrimSpace(cfg.Email.FromName),
		ToEmail:   recipient,
		ToName:    recipientName,
		Subject:   i18nx.Getf(i18nx.DefaultLocale, "email.passwordReset.subject"),
		BodyText: i18nx.Getf(
			i18nx.DefaultLocale,
			"email.passwordReset.body",
			link.String(),
			int(ttl/time.Minute),
		),
	}
	var deliveryCallback func()
	if onDelivered != nil {
		canonicalUserID := user.ID
		deliveryCallback = func() { onDelivered(canonicalUserID) }
	}
	job := &passwordResetMailJob{
		mailer:      mailer,
		params:      params,
		timeout:     cfg.Email.SMTPTimeout(),
		tokenHash:   tokenHash,
		expiresAt:   now.Add(ttl),
		ready:       make(chan bool, 1),
		onDelivered: deliveryCallback,
	}
	if !enqueuePasswordResetMail(job) {
		slog.Warn("password recovery mail queue is full")
		return nil
	}

	row := &models.PasswordResetToken{
		UserID:    user.ID,
		TokenHash: tokenHash,
		Surface:   surface,
		ExpiresAt: job.expiresAt,
		AuditFields: models.AuditFields{
			CreatedAt:      now,
			CreateUserID:   0,
			CreateUserName: "system",
			UpdatedAt:      now,
			UpdateUserID:   0,
			UpdateUserName: "system",
		},
	}
	if err := repositories.PasswordResetTokenRepository.Create(db, row); err != nil {
		job.ready <- false
		slog.Error("password recovery request preparation failed")
		return nil
	}
	job.ready <- true
	admissionCommitted = true
	return nil
}

// ResetPassword consumes the surface-bound token and commits its use,
// password replacement, sibling token invalidation, and session revocation in
// one database transaction.
func (s *passwordRecoveryService) ResetPassword(ctx context.Context, token, surface, password, confirmation string) error {
	if !validPasswordResetToken(token) || !validPasswordResetSurface(surface) {
		return errorsx.InvalidTokenI18n("error.passwordReset.invalidToken")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	db := sqls.DB()
	if db == nil {
		return errors.New("password recovery failed")
	}
	tokenHash := hashPasswordResetToken(token)
	precheckNow := s.currentTime()
	precheck, precheckErr := repositories.PasswordResetTokenRepository.GetByHash(db.WithContext(ctx), tokenHash)
	if errors.Is(precheckErr, gorm.ErrRecordNotFound) {
		return errorsx.InvalidTokenI18n("error.passwordReset.invalidToken")
	}
	if precheckErr != nil {
		return errors.New("password recovery failed")
	}
	if precheck.Surface != surface || precheck.UsedAt != nil || !precheck.ExpiresAt.After(precheckNow) {
		return errorsx.InvalidTokenI18n("error.passwordReset.invalidToken")
	}

	password = strings.TrimSpace(password)
	confirmation = strings.TrimSpace(confirmation)
	if password != confirmation {
		return errorsx.InvalidParamI18n("error.passwordReset.passwordMismatch")
	}
	if len([]byte(password)) < 8 || len([]byte(password)) > 72 {
		return errorsx.InvalidParamI18n("error.passwordReset.passwordPolicy")
	}

	hasher := s.passwordHasher
	if hasher == nil {
		hasher = bcrypt.GenerateFromPassword
	}
	passwordHash, err := hasher([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return errorsx.InvalidParamI18n("error.passwordReset.passwordPolicy")
	}
	err = sqls.WithTransaction(func(tx *sqls.TxContext) error {
		if tx == nil || tx.Tx == nil {
			return errors.New("password recovery transaction unavailable")
		}
		tokenRow, findErr := repositories.PasswordResetTokenRepository.GetByHash(tx.Tx, tokenHash)
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			return errorsx.InvalidTokenI18n("error.passwordReset.invalidToken")
		}
		if findErr != nil {
			return findErr
		}
		now := s.currentTime()
		if tokenRow.Surface != surface || tokenRow.UsedAt != nil || !tokenRow.ExpiresAt.After(now) {
			return errorsx.InvalidTokenI18n("error.passwordReset.invalidToken")
		}
		consumed, consumeErr := repositories.PasswordResetTokenRepository.Consume(tx.Tx, tokenHash, surface, now)
		if consumeErr != nil {
			return consumeErr
		}
		if consumed != 1 {
			return errorsx.InvalidTokenI18n("error.passwordReset.invalidToken")
		}

		user := repositories.UserRepository.Get(tx.Tx, tokenRow.UserID)
		if !passwordRecoveryUserEligible(tx.Tx, user, surface) {
			return errorsx.InvalidTokenI18n("error.passwordReset.invalidToken")
		}
		if err := repositories.UserRepository.Updates(tx.Tx, user.ID, map[string]interface{}{
			"password":         string(passwordHash),
			"password_salt":    "",
			"updated_at":       now,
			"update_user_id":   0,
			"update_user_name": "system",
		}); err != nil {
			return err
		}
		if err := repositories.PasswordResetTokenRepository.InvalidateUnusedByUser(tx.Tx, user.ID, now); err != nil {
			return err
		}
		if err := repositories.LoginSessionRepository.RevokeActiveByUser(tx.Tx, user.ID, 0, "system", now); err != nil {
			return err
		}
		if user.UserType == enums.UserTypeEmployee && s.invalidator != nil {
			tx.RegisterCallback(func() {
				s.invalidator.InvalidateEmployees([]int64{user.ID})
			})
		}
		return nil
	})

	if err != nil {
		var localized *errorsx.I18nError
		if errors.As(err, &localized) {
			return err
		}
		return errors.New("password recovery failed")
	}
	return nil
}
func findPasswordRecoveryUser(db *gorm.DB, identifier string) *models.User {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return nil
	}
	if user := repositories.UserRepository.GetByUsername(db, identifier); user != nil {
		return user
	}
	if user := repositories.UserRepository.GetByEmail(db, identifier); user != nil {
		return user
	}
	if user := repositories.UserRepository.GetByMobile(db, identifier); user != nil {
		return user
	}
	lowered := strings.ToLower(identifier)
	user := &models.User{}
	if err := db.Where("LOWER(username) = ? OR LOWER(email) = ?", lowered, lowered).First(user).Error; err != nil {
		return nil
	}
	return user
}

func passwordRecoveryUserEligible(db *gorm.DB, user *models.User, surface string) bool {
	if user == nil || user.Status != enums.StatusOk || user.DeletedAt != nil || strings.TrimSpace(user.Password) == "" {
		return false
	}
	switch surface {
	case PasswordRecoverySurfaceDashboard:
		return user.UserType == enums.UserTypeEmployee
	case PasswordRecoverySurfaceSupport:
		if user.UserType != enums.UserTypeUser {
			return false
		}
		customer := repositories.CustomerRepository.FindOne(db, sqls.NewCnd().Eq("user_id", user.ID).Eq("status", enums.StatusOk))
		return customer != nil
	default:
		return false
	}
}

func usablePasswordRecoveryAddress(address *string) (emailAddress, displayName string, ok bool) {
	if address == nil || strings.TrimSpace(*address) == "" {
		return "", "", false
	}
	parsed, err := mail.ParseAddress(strings.TrimSpace(*address))
	if err != nil || !strings.Contains(parsed.Address, "@") || strings.ContainsAny(parsed.Address, "\r\n") {
		return "", "", false
	}
	return parsed.Address, parsed.Name, true
}

func parsePasswordResetBaseURL(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) != raw || raw == "" || strings.Contains(raw, "#") {
		return nil, errors.New("invalid password reset base URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return nil, errors.New("invalid password reset base URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return nil, errors.New("invalid password reset base URL")
	}
	if scheme == "http" {
		host := strings.ToLower(parsed.Hostname())
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, errors.New("insecure password reset base URL")
		}
	}
	parsed.Scheme = scheme
	return parsed, nil
}

func validPasswordResetSurface(surface string) bool {
	return surface == PasswordRecoverySurfaceDashboard || surface == PasswordRecoverySurfaceSupport
}

func validPasswordResetToken(token string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(raw) == passwordResetTokenBytes && base64.RawURLEncoding.EncodeToString(raw) == token
}

func hashPasswordResetToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func passwordRecoveryDummyWork() {
	_, _ = bcrypt.GenerateFromPassword([]byte("password-recovery-timing-work"), bcrypt.DefaultCost)
}

func waitForPasswordRecoveryRequestFloor(started time.Time) {
	if remaining := passwordResetRequestMinimumDuration - time.Since(started); remaining > 0 {
		time.Sleep(remaining)
	}
}

func (s *passwordRecoveryService) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}
