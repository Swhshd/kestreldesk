package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/email"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/pkg/i18nx"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/gin-gonic/gin"
	"github.com/mlogclub/simple/sqls"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type passwordRecoveryTestMailer struct {
	mu          sync.Mutex
	messages    []email.SendEmailParams
	err         error
	started     chan struct{}
	release     chan struct{}
	finished    chan struct{}
	startedOnce sync.Once
	finishOnce  sync.Once
}

func (m *passwordRecoveryTestMailer) SendEmail(ctx context.Context, params email.SendEmailParams) error {
	m.mu.Lock()
	m.messages = append(m.messages, params)
	m.mu.Unlock()

	if m.started != nil {
		m.startedOnce.Do(func() { close(m.started) })
	}
	if m.finished != nil {
		defer m.finishOnce.Do(func() { close(m.finished) })
	}
	if m.release != nil {
		select {
		case <-m.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return m.err
}

func (m *passwordRecoveryTestMailer) sent() []email.SendEmailParams {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]email.SendEmailParams(nil), m.messages...)
}

func waitForPasswordRecoveryMessages(t *testing.T, mailer *passwordRecoveryTestMailer, count int) []email.SendEmailParams {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		messages := mailer.sent()
		if len(messages) >= count {
			return messages
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("password recovery email was not queued for delivery")
	return nil
}

func waitForPasswordRecoveryTokenUsed(t *testing.T, db *gorm.DB, tokenHash string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var token models.PasswordResetToken
		if err := db.Where("token_hash = ?", tokenHash).First(&token).Error; err == nil && token.UsedAt != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("failed delivery did not invalidate its reset token")
}

type passwordRecoveryInvalidatorSpy struct {
	mu            sync.Mutex
	employees     [][]int64
	loginSessions []int64
	allCalls      int
	onEmployees   func([]int64)
}

func (s *passwordRecoveryInvalidatorSpy) InvalidateLoginSession(id int64) int {
	s.mu.Lock()
	s.loginSessions = append(s.loginSessions, id)
	s.mu.Unlock()
	return 1
}

func (s *passwordRecoveryInvalidatorSpy) InvalidateEmployees(ids []int64) int {
	copyIDs := append([]int64(nil), ids...)
	s.mu.Lock()
	s.employees = append(s.employees, copyIDs)
	callback := s.onEmployees
	s.mu.Unlock()
	if callback != nil {
		callback(copyIDs)
	}
	return len(ids)
}

func (s *passwordRecoveryInvalidatorSpy) InvalidateAllEmployees() int {
	s.mu.Lock()
	s.allCalls++
	s.mu.Unlock()
	return 1
}

func (s *passwordRecoveryInvalidatorSpy) employeeCalls() [][]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]int64(nil), s.employees...)
}

func setupPasswordRecoveryTest(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB := sqls.DB()
	previousConfig := config.GetCurrent()
	previousLocale := i18nx.DefaultLocale
	db := setupAuthServiceTestDB(t)
	if err := db.AutoMigrate(
		&models.PasswordResetToken{},
		&models.CustomerIdentity{},
		&models.Channel{},
		&models.Conversation{},
	); err != nil {
		t.Fatalf("migrate recovery tables: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get recovery database: %v", err)
	}
	sqlDB.SetMaxOpenConns(10)
	config.SetCurrent(&config.Config{
		Language: "en-US",
		Email: config.EmailConfig{
			Enabled:                 true,
			Provider:                "smtp",
			FromAddress:             "no-reply@example.test",
			FromName:                "KestrelDesk",
			PasswordResetBaseURL:    "https://trusted.example",
			PasswordResetTTLMinutes: 30,
			SMTPTimeoutSeconds:      2,
		},
		CustomerSession: config.CustomerSessionConfig{
			Secret: "synthetic-customer-session-signing-key",
		},
	})
	i18nx.SetDefaultLocale("en-US")
	t.Cleanup(func() {
		sqls.SetDB(previousDB)
		config.SetCurrent(previousConfig)
		i18nx.SetDefaultLocale(previousLocale)
		_ = sqlDB.Close()
	})
	return db
}

func createPasswordRecoveryUser(t *testing.T, db *gorm.DB, username, address string, userType enums.UserType, status enums.Status, password string) *models.User {
	t.Helper()
	var hash string
	if password != "" {
		raw, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			t.Fatalf("hash synthetic account password: %v", err)
		}
		hash = string(raw)
	}
	now := time.Now()
	user := &models.User{
		Username: username,
		Nickname: username,
		UserType: userType,
		Status:   status,
		Password: hash,
		AuditFields: models.AuditFields{
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	if address != "" {
		user.Email = &address
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create synthetic recovery account: %v", err)
	}
	return user
}

func createPasswordRecoveryCustomer(t *testing.T, db *gorm.DB, userID int64, status enums.Status) *models.Customer {
	t.Helper()
	customer := &models.Customer{
		UserID: userID,
		Name:   "Synthetic customer",
		Status: status,
	}
	if err := db.Create(customer).Error; err != nil {
		t.Fatalf("create synthetic customer relationship: %v", err)
	}
	return customer
}

func createPasswordRecoveryLoginSession(t *testing.T, db *gorm.DB, userID int64, token string) *models.LoginSession {
	t.Helper()
	now := time.Now()
	row := &models.LoginSession{
		UserID:     userID,
		Token:      token,
		ClientType: "admin_web",
		ExpiredAt:  now.Add(time.Hour),
		AuditFields: models.AuditFields{
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("create synthetic login session: %v", err)
	}
	return row
}

func syntheticResetToken(seed byte) string {
	raw := bytes.Repeat([]byte{seed}, 32)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func insertPasswordResetToken(t *testing.T, db *gorm.DB, userID int64, surface, token string, expires time.Time, usedAt *time.Time) *models.PasswordResetToken {
	t.Helper()
	hash := sha256.Sum256([]byte(token))
	now := time.Now()
	row := &models.PasswordResetToken{
		UserID:    userID,
		TokenHash: hex.EncodeToString(hash[:]),
		Surface:   surface,
		ExpiresAt: expires,
		UsedAt:    usedAt,
		AuditFields: models.AuditFields{
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("create synthetic reset token row: %v", err)
	}
	return row
}

func resetTokenFromMessage(t *testing.T, body string) (string, *url.URL) {
	t.Helper()
	for _, field := range strings.Fields(body) {
		candidate := strings.Trim(field, "()<>,")
		parsed, err := url.Parse(candidate)
		if err == nil && parsed.Query().Get("token") != "" {
			return parsed.Query().Get("token"), parsed
		}
	}
	t.Fatal("recovery email did not contain a reset link")
	return "", nil
}

func requirePasswordRecoveryI18nError(t *testing.T, err error, key string) {
	t.Helper()
	var localized *errorsx.I18nError
	if !errors.As(err, &localized) || localized.Key != key {
		t.Fatalf("recovery returned the wrong localized error category")
	}
}

func TestPasswordRecoveryRequestIsGenericAcrossAccountEligibility(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	mailer := &passwordRecoveryTestMailer{}
	service := newPasswordRecoveryService(mailer, &passwordRecoveryInvalidatorSpy{})

	employee := createPasswordRecoveryUser(t, db, "synthetic-dashboard", "dashboard@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
	customerUser := createPasswordRecoveryUser(t, db, "synthetic-support", "support@example.test", enums.UserTypeUser, enums.StatusOk, "synthetic-password")
	createPasswordRecoveryCustomer(t, db, customerUser.ID, enums.StatusOk)
	inactive := createPasswordRecoveryUser(t, db, "synthetic-inactive", "inactive@example.test", enums.UserTypeEmployee, enums.StatusDisabled, "synthetic-password")
	federated := createPasswordRecoveryUser(t, db, "synthetic-federated", "federated@example.test", enums.UserTypeEmployee, enums.StatusOk, "")
	noEmail := createPasswordRecoveryUser(t, db, "synthetic-no-email", "", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
	malformedEmail := createPasswordRecoveryUser(t, db, "synthetic-bad-email", "not-an-email", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
	unlinkedSupport := createPasswordRecoveryUser(t, db, "synthetic-unlinked-support", "unlinked@example.test", enums.UserTypeUser, enums.StatusOk, "synthetic-password")
	disabledSupport := createPasswordRecoveryUser(t, db, "synthetic-disabled-support", "disabled-support@example.test", enums.UserTypeUser, enums.StatusOk, "synthetic-password")
	createPasswordRecoveryCustomer(t, db, disabledSupport.ID, enums.StatusDisabled)
	deleted := createPasswordRecoveryUser(t, db, "synthetic-deleted", "deleted@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
	deletedAt := time.Now()
	if err := db.Model(&models.User{}).Where("id = ?", deleted.ID).Update("deleted_at", deletedAt).Error; err != nil {
		t.Fatalf("mark synthetic account deleted: %v", err)
	}

	requests := []struct {
		name       string
		identifier string
		surface    string
		wantMail   bool
	}{
		{name: "eligible dashboard employee", identifier: employee.Username, surface: "dashboard", wantMail: true},
		{name: "eligible support customer", identifier: customerUser.Username, surface: "support", wantMail: true},
		{name: "missing account", identifier: "missing@example.test", surface: "support"},
		{name: "wrong surface", identifier: employee.Username, surface: "support"},
		{name: "wrong account type", identifier: customerUser.Username, surface: "dashboard"},
		{name: "inactive employee", identifier: inactive.Username, surface: "dashboard"},
		{name: "federated employee without local password", identifier: federated.Username, surface: "dashboard"},
		{name: "employee without email", identifier: noEmail.Username, surface: "dashboard"},
		{name: "employee with unusable email", identifier: malformedEmail.Username, surface: "dashboard"},
		{name: "support account without customer", identifier: unlinkedSupport.Username, surface: "support"},
		{name: "support account with disabled customer", identifier: disabledSupport.Username, surface: "support"},
		{name: "deleted employee", identifier: deleted.Username, surface: "dashboard"},
		{name: "invalid surface", identifier: employee.Username, surface: "guest"},
	}
	for _, request := range requests {
		t.Run(request.name, func(t *testing.T) {
			before := len(mailer.sent())
			err := service.RequestReset(context.Background(), request.identifier, request.surface, "203.0.113.45")
			if err != nil {
				t.Fatal("request outcome exposed account eligibility")
			}
			after := len(mailer.sent())
			if request.wantMail {
				after = len(waitForPasswordRecoveryMessages(t, mailer, before+1))
			}
			wantDelta := 0
			if request.wantMail {
				wantDelta = 1
			}
			if after-before != wantDelta {
				t.Fatalf("email delivery count delta = %d, want %d", after-before, wantDelta)
			}
		})
	}
}

func TestPasswordRecoveryRequestUsesCommonMinimumResponseFloor(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	service := newPasswordRecoveryService(&passwordRecoveryTestMailer{}, &passwordRecoveryInvalidatorSpy{})
	eligible := createPasswordRecoveryUser(t, db, "timing-eligible", "timing-eligible@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
	ineligible := createPasswordRecoveryUser(t, db, "timing-ineligible", "timing-ineligible@example.test", enums.UserTypeEmployee, enums.StatusDisabled, "synthetic-password")
	const minimumResponseFloor = 250 * time.Millisecond

	for _, item := range []struct {
		name       string
		identifier string
	}{
		{name: "eligible", identifier: eligible.Username},
		{name: "ineligible", identifier: ineligible.Username},
		{name: "missing", identifier: "timing-missing@example.test"},
	} {
		t.Run(item.name, func(t *testing.T) {
			started := time.Now()
			if err := service.RequestReset(context.Background(), item.identifier, PasswordRecoverySurfaceDashboard, "203.0.113.45"); err != nil {
				t.Fatal("request exposed account eligibility")
			}
			if elapsed := time.Since(started); elapsed < minimumResponseFloor {
				t.Errorf("request returned in %s, below the %s common response floor", elapsed, minimumResponseFloor)
			}
		})
	}
}

func TestPasswordRecoveryRequestPreservesBlankProviderForClientInference(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	cfg := *config.GetCurrent()
	cfg.Email.Provider = ""
	cfg.Email.APIKey = "SG.synthetic-key"
	config.SetCurrent(&cfg)
	user := createPasswordRecoveryUser(t, db, "provider-inference", "provider-inference@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
	service := newPasswordRecoveryService(nil, &passwordRecoveryInvalidatorSpy{})
	var captured email.ClientConfig
	service.mailerFactory = func(clientConfig email.ClientConfig) email.Client {
		captured = clientConfig
		return &passwordRecoveryTestMailer{}
	}

	if err := service.RequestReset(context.Background(), user.Username, PasswordRecoverySurfaceDashboard, "203.0.113.45"); err != nil {
		t.Fatal("request failed")
	}
	if captured.Provider != "" {
		t.Fatal("blank provider was replaced before email.NewClient inference")
	}
	if captured.APIKey != "SG.synthetic-key" {
		t.Fatal("legacy API key was not passed to email.NewClient")
	}
}

func TestPasswordRecoveryRequestStoresHashedEntropyAndUsesTrustedSurfaceURL(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	mailer := &passwordRecoveryTestMailer{}
	service := newPasswordRecoveryService(mailer, &passwordRecoveryInvalidatorSpy{})
	user := createPasswordRecoveryUser(t, db, "synthetic-support", "support@example.test", enums.UserTypeUser, enums.StatusOk, "synthetic-password")
	createPasswordRecoveryCustomer(t, db, user.ID, enums.StatusOk)

	gin.SetMode(gin.TestMode)
	httpContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	httpContext.Request = httptest.NewRequest("POST", "http://attacker.invalid/api/support/password-reset/request", nil)
	httpContext.Request.Host = "attacker.invalid"
	if err := service.RequestReset(httpContext, user.Username, "support", "203.0.113.45"); err != nil {
		t.Fatal("eligible request failed")
	}
	messages := waitForPasswordRecoveryMessages(t, mailer, 1)
	if len(messages) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(messages))
	}
	token, link := resetTokenFromMessage(t, messages[0].BodyText)
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		t.Fatal("reset token did not contain 32 URL-safe random bytes")
	}
	if link.Scheme != "https" || link.Host != "trusted.example" || link.Path != "/support/reset-password" {
		t.Fatal("reset email link did not use the configured support URL")
	}
	if messages[0].ToEmail != "support@example.test" {
		t.Fatal("reset email recipient did not use the stored account email")
	}
	if strings.Contains(messages[0].BodyText, "attacker.invalid") {
		t.Fatal("request Host influenced the reset email link")
	}

	hash := sha256.Sum256([]byte(token))
	var rows []models.PasswordResetToken
	if err := db.Where("user_id = ?", user.ID).Find(&rows).Error; err != nil {
		t.Fatalf("read persisted reset token: %v", err)
	}
	if len(rows) != 1 || rows[0].TokenHash != hex.EncodeToString(hash[:]) || rows[0].Surface != "support" {
		t.Fatal("persisted reset token did not contain the expected hash and surface")
	}
	if strings.Contains(rows[0].TokenHash, token) || rows[0].TokenHash == token {
		t.Fatal("raw reset token was persisted")
	}
	if rows[0].ExpiresAt.Before(time.Now().Add(29*time.Minute)) || rows[0].ExpiresAt.After(time.Now().Add(31*time.Minute)) {
		t.Fatal("reset token expiry did not follow the configured short TTL")
	}

	second := &passwordRecoveryTestMailer{}
	secondService := newPasswordRecoveryService(second, &passwordRecoveryInvalidatorSpy{})
	if err := secondService.RequestReset(context.Background(), user.Username, "support", "203.0.113.45"); err != nil {
		t.Fatal("second eligible request failed")
	}
	secondMessages := waitForPasswordRecoveryMessages(t, second, 1)
	secondToken, _ := resetTokenFromMessage(t, secondMessages[0].BodyText)
	if secondToken == token {
		t.Fatal("separate requests reused reset token entropy")
	}
}

func TestPasswordRecoveryDoesNotCreateTokensForUnsafeTrustedBases(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	mailer := &passwordRecoveryTestMailer{}
	service := newPasswordRecoveryService(mailer, &passwordRecoveryInvalidatorSpy{})
	user := createPasswordRecoveryUser(t, db, "synthetic-dashboard", "dashboard@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
	unsafeBases := []string{
		"http://reset.example",
		"https://user:password@trusted.example",
		"https://trusted.example?next=https://attacker.invalid",
		"https://trusted.example#fragment",
		"file:///tmp/recovery",
	}
	for _, base := range unsafeBases {
		cfg := config.Current()
		cfg.Email.PasswordResetBaseURL = base
		config.SetCurrent(&cfg)
		before := len(mailer.sent())
		if err := service.RequestReset(context.Background(), user.Username, "dashboard", "203.0.113.45"); err != nil {
			t.Fatal("unsafe URL changed the generic request outcome")
		}
		if got := len(mailer.sent()); got != before {
			t.Fatal("unsafe configured base URL sent a reset email")
		}
		var count int64
		if err := db.Model(&models.PasswordResetToken{}).Count(&count).Error; err != nil {
			t.Fatalf("count stored reset tokens: %v", err)
		}
		if count != 0 {
			t.Fatal("unsafe configured base URL persisted a reset token")
		}
	}
	cfg := config.Current()
	cfg.Email.Enabled = false
	config.SetCurrent(&cfg)
	if err := service.RequestReset(context.Background(), user.Username, "dashboard", "203.0.113.45"); err != nil {
		t.Fatal("disabled mail changed the generic request outcome")
	}
	if len(mailer.sent()) != 0 {
		t.Fatal("disabled mail configuration sent a reset email")
	}
}

type passwordRecoveryTestLogWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (w *passwordRecoveryTestLogWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(data)
}

func (w *passwordRecoveryTestLogWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func TestPasswordRecoveryRequestDoesNotWaitForEmailDelivery(t *testing.T) {
	for _, item := range []struct {
		name    string
		sendErr error
	}{
		{name: "delivery succeeds"},
		{name: "delivery fails", sendErr: errors.New("private transport diagnostic")},
	} {
		t.Run(item.name, func(t *testing.T) {
			db := setupPasswordRecoveryTest(t)
			logWriter := &passwordRecoveryTestLogWriter{}
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(logWriter, nil)))
			t.Cleanup(func() { slog.SetDefault(previousLogger) })

			release := make(chan struct{})
			var releaseOnce sync.Once
			mailer := &passwordRecoveryTestMailer{
				err:      item.sendErr,
				started:  make(chan struct{}),
				release:  release,
				finished: make(chan struct{}),
			}
			service := newPasswordRecoveryService(mailer, &passwordRecoveryInvalidatorSpy{})
			user := createPasswordRecoveryUser(t, db, "synthetic-dashboard", "dashboard@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")

			requestCtx, cancelRequest := context.WithCancel(context.Background())
			defer cancelRequest()
			requestDone := make(chan error, 1)
			go func() {
				requestDone <- service.RequestReset(requestCtx, user.Username, PasswordRecoverySurfaceDashboard, "203.0.113.45")
			}()

			var publicErr error
			returnedBeforeRelease := false
			select {
			case publicErr = <-requestDone:
				returnedBeforeRelease = true
			case <-time.After(time.Second):
			}
			if returnedBeforeRelease {
				cancelRequest()
			}

			select {
			case <-mailer.started:
			case <-time.After(time.Second):
				releaseOnce.Do(func() { close(release) })
				if !returnedBeforeRelease {
					publicErr = <-requestDone
				}
				t.Fatal("background email worker did not start")
			}

			completedBeforeRelease := false
			select {
			case <-mailer.finished:
				completedBeforeRelease = true
			default:
			}
			releaseOnce.Do(func() { close(release) })
			if !returnedBeforeRelease {
				publicErr = <-requestDone
			}
			select {
			case <-mailer.finished:
			case <-time.After(2 * time.Second):
				t.Fatal("background email worker did not finish")
			}

			if !returnedBeforeRelease {
				t.Error("request waited for email delivery to finish")
			}
			if publicErr != nil {
				t.Error("request exposed email delivery status")
			}
			if completedBeforeRelease {
				t.Error("blocked email delivery finished before release")
			}

			if item.sendErr != nil {
				messages := waitForPasswordRecoveryMessages(t, mailer, 1)
				token, _ := resetTokenFromMessage(t, messages[0].BodyText)
				waitForPasswordRecoveryTokenUsed(t, db, passwordResetTokenHash(token))
				deadline := time.Now().Add(time.Second)
				for !strings.Contains(logWriter.String(), "password recovery email delivery failed") && time.Now().Before(deadline) {
					time.Sleep(5 * time.Millisecond)
				}
				logs := logWriter.String()
				if strings.Contains(logs, "private transport diagnostic") {
					t.Error("delivery failure log exposed the mail transport error")
				}
				if !strings.Contains(logs, "password recovery email delivery failed") {
					t.Error("delivery failure did not produce a fixed sanitized log entry")
				}
			}
		})
	}
}

func TestPasswordRecoveryExpiredQueuedMailIsSkippedAndTokenUsed(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	user := createPasswordRecoveryUser(t, db, "expired-queue-dashboard", "expired-queue@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
	token := syntheticResetToken(61)
	expiresAt := time.Now().Add(-time.Minute)
	row := insertPasswordResetToken(t, db, user.ID, PasswordRecoverySurfaceDashboard, token, expiresAt, nil)
	mailer := &passwordRecoveryTestMailer{}
	job := &passwordResetMailJob{
		mailer:    mailer,
		timeout:   time.Second,
		tokenHash: passwordResetTokenHash(token),
		expiresAt: expiresAt,
		ready:     make(chan bool, 1),
	}
	job.ready <- true

	processPasswordResetMailJob(job)
	if len(mailer.sent()) != 0 {
		t.Fatal("expired password reset email was delivered")
	}
	var persisted models.PasswordResetToken
	if err := db.First(&persisted, row.ID).Error; err != nil {
		t.Fatalf("read expired queued token: %v", err)
	}
	if persisted.UsedAt == nil {
		t.Fatal("expired queued token was not marked unusable")
	}
}

func TestPasswordRecoveryDeliveryCallbackRunsOnlyAfterSuccessfulSend(t *testing.T) {
	for _, item := range []struct {
		name     string
		sendErr  error
		wantCall bool
	}{
		{name: "successful send", wantCall: true},
		{name: "failed send", sendErr: errors.New("private transport diagnostic")},
	} {
		t.Run(item.name, func(t *testing.T) {
			db := setupPasswordRecoveryTest(t)
			mailer := &passwordRecoveryTestMailer{err: item.sendErr, finished: make(chan struct{})}
			service := newPasswordRecoveryService(mailer, &passwordRecoveryInvalidatorSpy{})
			user := createPasswordRecoveryUser(t, db, "delivery-callback-user", "delivery-callback@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
			callback := make(chan int64, 1)

			if err := service.RequestResetWithAdmissionCallback(context.Background(), user.Username, PasswordRecoverySurfaceDashboard, "203.0.113.45", func(userID int64) (func(), bool) {
				if userID != user.ID {
					t.Errorf("admission user ID = %d, want canonical ID %d", userID, user.ID)
				}
				return nil, true
			}, func(userID int64) {
				callback <- userID
			}); err != nil {
				t.Fatalf("RequestResetWithAdmissionCallback() error = %v", err)
			}

			select {
			case <-mailer.finished:
			case <-time.After(2 * time.Second):
				t.Fatal("background email worker did not finish")
			}

			if item.wantCall {
				select {
				case userID := <-callback:
					if userID != user.ID {
						t.Fatalf("delivery callback user ID = %d, want canonical ID %d", userID, user.ID)
					}
				case <-time.After(time.Second):
					t.Fatal("successful delivery did not invoke the callback")
				}
				return
			}
			select {
			case <-callback:
				t.Fatal("failed delivery invoked the success callback")
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
}

func TestPasswordRecoveryDeliveryCallbackDoesNotRunForIneligibleOrUnqueuedRequests(t *testing.T) {
	t.Run("ineligible account", func(t *testing.T) {
		setupPasswordRecoveryTest(t)
		mailer := &passwordRecoveryTestMailer{}
		service := newPasswordRecoveryService(mailer, &passwordRecoveryInvalidatorSpy{})
		admissionCalls := 0
		reservationReleases := 0

		if err := service.RequestResetWithAdmissionCallback(context.Background(), "missing-account", PasswordRecoverySurfaceDashboard, "203.0.113.45", func(int64) (func(), bool) {
			admissionCalls++
			return func() { reservationReleases++ }, true
		}, nil); err != nil {
			t.Fatalf("RequestResetWithAdmissionCallback() error = %v", err)
		}
		if admissionCalls != 0 || reservationReleases != 0 || len(mailer.sent()) != 0 {
			t.Fatalf("ineligible request admission calls=%d releases=%d mail count=%d", admissionCalls, reservationReleases, len(mailer.sent()))
		}
	})

	t.Run("token persistence failure", func(t *testing.T) {
		db := setupPasswordRecoveryTest(t)
		mailer := &passwordRecoveryTestMailer{}
		service := newPasswordRecoveryService(mailer, &passwordRecoveryInvalidatorSpy{})
		user := createPasswordRecoveryUser(t, db, "callback-persist-user", "callback-persist@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
		reservationReleases := 0
		const callbackName = "test:fail-password-reset-token-create"
		if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Name == "PasswordResetToken" {
				tx.AddError(errors.New("synthetic persistence failure"))
			}
		}); err != nil {
			t.Fatalf("register persistence failure callback: %v", err)
		}
		t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

		if err := service.RequestResetWithAdmissionCallback(context.Background(), user.Username, PasswordRecoverySurfaceDashboard, "203.0.113.45", func(int64) (func(), bool) {
			return func() { reservationReleases++ }, true
		}, nil); err != nil {
			t.Fatalf("RequestResetWithAdmissionCallback() error = %v", err)
		}
		if reservationReleases != 1 || len(mailer.sent()) != 0 {
			t.Fatalf("persistence failure releases=%d mail count=%d; want reservation released and no mail", reservationReleases, len(mailer.sent()))
		}
	})

	t.Run("full mail queue", func(t *testing.T) {
		db := setupPasswordRecoveryTest(t)
		mailer := &passwordRecoveryTestMailer{}
		service := newPasswordRecoveryService(mailer, &passwordRecoveryInvalidatorSpy{})
		user := createPasswordRecoveryUser(t, db, "callback-queue-user", "callback-queue@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
		reservationReleases := 0

		previousJobs := passwordResetMailJobs
		previousWorkerOnce := passwordResetMailWorkerMu
		passwordResetMailJobs = make(chan *passwordResetMailJob, 1)
		passwordResetMailWorkerMu = &sync.Once{}
		passwordResetMailWorkerMu.Do(func() {})
		passwordResetMailJobs <- &passwordResetMailJob{}
		t.Cleanup(func() {
			passwordResetMailJobs = previousJobs
			passwordResetMailWorkerMu = previousWorkerOnce
		})

		if err := service.RequestResetWithAdmissionCallback(context.Background(), user.Username, PasswordRecoverySurfaceDashboard, "203.0.113.45", func(int64) (func(), bool) {
			return func() { reservationReleases++ }, true
		}, nil); err != nil {
			t.Fatalf("RequestResetWithAdmissionCallback() error = %v", err)
		}
		if reservationReleases != 1 || len(mailer.sent()) != 0 {
			t.Fatalf("queue-full reservation releases=%d mail count=%d; want reservation released and no mail", reservationReleases, len(mailer.sent()))
		}
	})
}

func TestPasswordRecoveryAdmissionCallbackUsesCanonicalUserAndPreventsTokenCreation(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	mailer := &passwordRecoveryTestMailer{}
	service := newPasswordRecoveryService(mailer, &passwordRecoveryInvalidatorSpy{})
	user := createPasswordRecoveryUser(t, db, "admission-alias-user", "admission-alias@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
	const mobile = "+15555550123"
	if err := db.Model(&models.User{}).Where("id = ?", user.ID).Update("mobile", mobile).Error; err != nil {
		t.Fatalf("set synthetic account mobile: %v", err)
	}

	var resolvedIDs []int64
	for _, identifier := range []string{user.Username, "admission-alias@example.test", mobile} {
		if err := service.RequestResetWithAdmissionCallback(context.Background(), identifier, PasswordRecoverySurfaceDashboard, "203.0.113.45", func(userID int64) (func(), bool) {
			resolvedIDs = append(resolvedIDs, userID)
			return nil, false
		}, nil); err != nil {
			t.Fatalf("RequestResetWithAdmissionCallback(%q) exposed an error", identifier)
		}
	}
	if len(resolvedIDs) != 3 {
		t.Fatalf("admission callback calls=%d want once for each eligible username/email/mobile alias", len(resolvedIDs))
	}
	for _, resolvedID := range resolvedIDs {
		if resolvedID != user.ID {
			t.Fatalf("alias resolved to user ID %d, want canonical ID %d", resolvedID, user.ID)
		}
	}
	if len(mailer.sent()) != 0 {
		t.Fatalf("denied admission enqueued %d email jobs", len(mailer.sent()))
	}
	var tokenCount int64
	if err := db.Model(&models.PasswordResetToken{}).Count(&tokenCount).Error; err != nil {
		t.Fatalf("count reset tokens after denied admission: %v", err)
	}
	if tokenCount != 0 {
		t.Fatalf("denied admission created %d reset tokens, want none", tokenCount)
	}
}

func TestPasswordRecoveryAdmissionCallbackPreservesEligibilityAndSurfaceScope(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	service := newPasswordRecoveryService(&passwordRecoveryTestMailer{}, &passwordRecoveryInvalidatorSpy{})
	employee := createPasswordRecoveryUser(t, db, "admission-dashboard-user", "admission-dashboard@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
	noEmail := createPasswordRecoveryUser(t, db, "admission-no-email", "", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
	customer := createPasswordRecoveryUser(t, db, "admission-support-user", "admission-support@example.test", enums.UserTypeUser, enums.StatusOk, "synthetic-password")
	createPasswordRecoveryCustomer(t, db, customer.ID, enums.StatusOk)

	for _, item := range []struct {
		name       string
		identifier string
		surface    string
		wantID     int64
	}{
		{name: "wrong dashboard account on support", identifier: employee.Username, surface: PasswordRecoverySurfaceSupport},
		{name: "missing usable email", identifier: noEmail.Username, surface: PasswordRecoverySurfaceDashboard},
		{name: "wrong support account on dashboard", identifier: customer.Username, surface: PasswordRecoverySurfaceDashboard},
		{name: "eligible support customer", identifier: customer.Username, surface: PasswordRecoverySurfaceSupport, wantID: customer.ID},
	} {
		t.Run(item.name, func(t *testing.T) {
			var resolvedIDs []int64
			if err := service.RequestResetWithAdmissionCallback(context.Background(), item.identifier, item.surface, "203.0.113.45", func(userID int64) (func(), bool) {
				resolvedIDs = append(resolvedIDs, userID)
				return nil, false
			}, nil); err != nil {
				t.Fatalf("request exposed an admission or eligibility error")
			}
			if item.wantID == 0 {
				if len(resolvedIDs) != 0 {
					t.Fatalf("ineligible request invoked admission with IDs %v", resolvedIDs)
				}
			} else if len(resolvedIDs) != 1 || resolvedIDs[0] != item.wantID {
				t.Fatalf("eligible support admission IDs=%v, want [%d]", resolvedIDs, item.wantID)
			}
		})
	}
}

func TestPasswordRecoveryAdmissionReservationIsKeptAfterTokenAndQueueCommit(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	service := newPasswordRecoveryService(&passwordRecoveryTestMailer{}, &passwordRecoveryInvalidatorSpy{})
	user := createPasswordRecoveryUser(t, db, "admission-committed-user", "admission-committed@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
	reservationReleases := 0
	if err := service.RequestResetWithAdmissionCallback(context.Background(), user.Username, PasswordRecoverySurfaceDashboard, "203.0.113.45", func(userID int64) (func(), bool) {
		if userID != user.ID {
			t.Fatalf("admission user ID=%d, want %d", userID, user.ID)
		}
		return func() { reservationReleases++ }, true
	}, nil); err != nil {
		t.Fatalf("RequestResetWithAdmissionCallback() error = %v", err)
	}
	if reservationReleases != 0 {
		t.Fatalf("committed token request released its admission %d times", reservationReleases)
	}
	var tokenCount int64
	if err := db.Model(&models.PasswordResetToken{}).Where("user_id = ?", user.ID).Count(&tokenCount).Error; err != nil {
		t.Fatalf("count committed reset tokens: %v", err)
	}
	if tokenCount != 1 {
		t.Fatalf("committed request created %d tokens, want 1", tokenCount)
	}
}

func TestPasswordRecoveryTrustedBaseURLAllowsHTTPOnlyForLoopback(t *testing.T) {
	valid := []string{
		"https://trusted.example",
		"http://localhost:8083",
		"http://127.0.0.1:8083",
		"http://[::1]:8083",
	}
	for _, raw := range valid {
		if _, err := parsePasswordResetBaseURL(raw); err != nil {
			t.Fatal("valid trusted base URL was rejected")
		}
	}
	invalid := []string{
		"http://reset.example",
		"http://192.0.2.1",
		"/relative/path",
		"ftp://trusted.example",
	}
	for _, raw := range invalid {
		if _, err := parsePasswordResetBaseURL(raw); err == nil {
			t.Fatal("untrusted base URL was accepted")
		}
	}
}
func TestPasswordRecoveryResetRejectsMalformedExpiredUsedAndWrongSurfaceTokens(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	service := newPasswordRecoveryService(&passwordRecoveryTestMailer{}, &passwordRecoveryInvalidatorSpy{})
	user := createPasswordRecoveryUser(t, db, "synthetic-dashboard", "dashboard@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
	expired := syntheticResetToken(1)
	insertPasswordResetToken(t, db, user.ID, "dashboard", expired, time.Now().Add(-time.Minute), nil)
	used := syntheticResetToken(2)
	now := time.Now()
	insertPasswordResetToken(t, db, user.ID, "dashboard", used, time.Now().Add(time.Hour), &now)
	crossSurface := syntheticResetToken(3)
	insertPasswordResetToken(t, db, user.ID, "support", crossSurface, time.Now().Add(time.Hour), nil)

	cases := []struct {
		name    string
		token   string
		surface string
	}{
		{name: "malformed", token: "not-a-reset-token", surface: "dashboard"},
		{name: "missing", token: syntheticResetToken(4), surface: "dashboard"},
		{name: "expired", token: expired, surface: "dashboard"},
		{name: "used", token: used, surface: "dashboard"},
		{name: "wrong surface", token: crossSurface, surface: "dashboard"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			err := service.ResetPassword(context.Background(), item.token, item.surface, "synthetic-new-password", "synthetic-new-password")
			requirePasswordRecoveryI18nError(t, err, "error.passwordReset.invalidToken")
		})
	}
}

func TestPasswordRecoveryInvalidTokensSkipPasswordHasher(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	user := createPasswordRecoveryUser(t, db, "precheck-dashboard", "precheck@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
	expired := syntheticResetToken(51)
	insertPasswordResetToken(t, db, user.ID, PasswordRecoverySurfaceDashboard, expired, time.Now().Add(-time.Minute), nil)
	used := syntheticResetToken(52)
	now := time.Now()
	insertPasswordResetToken(t, db, user.ID, PasswordRecoverySurfaceDashboard, used, time.Now().Add(time.Hour), &now)
	wrongSurface := syntheticResetToken(53)
	insertPasswordResetToken(t, db, user.ID, PasswordRecoverySurfaceSupport, wrongSurface, time.Now().Add(time.Hour), nil)

	service := newPasswordRecoveryService(&passwordRecoveryTestMailer{}, &passwordRecoveryInvalidatorSpy{})
	hashCalls := 0
	service.passwordHasher = func([]byte, int) ([]byte, error) {
		hashCalls++
		return []byte("synthetic-hash"), nil
	}
	for _, item := range []struct {
		name    string
		token   string
		surface string
	}{
		{name: "malformed", token: "not-a-reset-token", surface: PasswordRecoverySurfaceDashboard},
		{name: "missing", token: syntheticResetToken(54), surface: PasswordRecoverySurfaceDashboard},
		{name: "expired", token: expired, surface: PasswordRecoverySurfaceDashboard},
		{name: "used", token: used, surface: PasswordRecoverySurfaceDashboard},
		{name: "wrong surface", token: wrongSurface, surface: PasswordRecoverySurfaceDashboard},
	} {
		t.Run(item.name, func(t *testing.T) {
			err := service.ResetPassword(context.Background(), item.token, item.surface, "synthetic-new-password", "synthetic-new-password")
			requirePasswordRecoveryI18nError(t, err, "error.passwordReset.invalidToken")
			if hashCalls != 0 {
				t.Fatal("invalid reset token triggered password hashing")
			}
		})
	}
}

func TestPasswordRecoveryResetEnforcesEightCharacterSupportPolicy(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	service := newPasswordRecoveryService(&passwordRecoveryTestMailer{}, &passwordRecoveryInvalidatorSpy{})
	user := createPasswordRecoveryUser(t, db, "synthetic-support", "support@example.test", enums.UserTypeUser, enums.StatusOk, "synthetic-old-password")
	createPasswordRecoveryCustomer(t, db, user.ID, enums.StatusOk)

	for _, item := range []struct {
		name         string
		password     string
		confirmation string
		key          string
	}{
		{name: "too short", password: "short", confirmation: "short", key: "error.passwordReset.passwordPolicy"},
		{name: "confirmation mismatch", password: "synthetic-new-password", confirmation: "different-password", key: "error.passwordReset.passwordMismatch"},
	} {
		t.Run(item.name, func(t *testing.T) {
			token := syntheticResetToken(byte(len(item.name) + 10))
			insertPasswordResetToken(t, db, user.ID, "support", token, time.Now().Add(time.Hour), nil)
			err := service.ResetPassword(context.Background(), token, "support", item.password, item.confirmation)
			requirePasswordRecoveryI18nError(t, err, item.key)
		})
	}

	token := syntheticResetToken(20)
	insertPasswordResetToken(t, db, user.ID, "support", token, time.Now().Add(time.Hour), nil)
	if err := service.ResetPassword(context.Background(), token, "support", "  eight chars  ", "eight chars"); err != nil {
		t.Fatal("eight-character password was rejected")
	}
	persisted := repositories.UserRepository.Get(db, user.ID)
	if bcrypt.CompareHashAndPassword([]byte(persisted.Password), []byte("eight chars")) != nil {
		t.Fatal("reset did not hash the trimmed password using bcrypt")
	}
}

func TestPasswordRecoveryResetRevokesAccountSessionsAndPreservesGuestSessionHistory(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	invalidator := &passwordRecoveryInvalidatorSpy{}
	service := newPasswordRecoveryService(&passwordRecoveryTestMailer{}, invalidator)
	user := createPasswordRecoveryUser(t, db, "synthetic-dashboard", "dashboard@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-old-password")
	ownA := createPasswordRecoveryLoginSession(t, db, user.ID, "synthetic-account-session-a")
	ownB := createPasswordRecoveryLoginSession(t, db, user.ID, "synthetic-account-session-b")
	other := createPasswordRecoveryUser(t, db, "synthetic-other", "other@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-password")
	otherSession := createPasswordRecoveryLoginSession(t, db, other.ID, "synthetic-other-session")
	tokenA := syntheticResetToken(31)
	tokenB := syntheticResetToken(32)
	rowA := insertPasswordResetToken(t, db, user.ID, "dashboard", tokenA, time.Now().Add(time.Hour), nil)
	rowB := insertPasswordResetToken(t, db, user.ID, "dashboard", tokenB, time.Now().Add(time.Hour), nil)

	channel := &models.Channel{ID: 901, ChannelID: "synthetic-guest-channel", Status: enums.StatusOk}
	guest := &models.Customer{ID: 902, Name: "Synthetic guest", Status: enums.StatusOk}
	external := openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: "synthetic-guest"}
	identity := &models.CustomerIdentity{CustomerID: guest.ID, ExternalSource: external.ExternalSource, ExternalID: external.ExternalID, Status: enums.StatusOk}
	conversation := &models.Conversation{ID: 903, CustomerID: guest.ID, CustomerName: guest.Name}
	for _, item := range []any{channel, guest, identity, conversation} {
		if err := db.Create(item).Error; err != nil {
			t.Fatalf("create synthetic Guest continuity fixture: %v", err)
		}
	}
	guestContext := signedCustomerSessionTestContext(t, channel, guest, external)
	guestToken := guestContext.GetHeader("Authorization")
	if _, err := CustomerSessionService.VerifyRequest(guestContext, channel); err != nil {
		t.Fatal("synthetic Guest session was invalid before reset")
	}
	var guestHistoryBefore int64
	if err := db.Model(&models.Conversation{}).Where("customer_id = ?", guest.ID).Count(&guestHistoryBefore).Error; err != nil {
		t.Fatalf("count Guest history before reset: %v", err)
	}

	invalidator.onEmployees = func(ids []int64) {
		if len(ids) != 1 || ids[0] != user.ID {
			t.Fatal("employee realtime invalidation targeted the wrong account")
		}
		current := repositories.UserRepository.Get(db, user.ID)
		if current == nil || bcrypt.CompareHashAndPassword([]byte(current.Password), []byte("synthetic-new-password")) != nil {
			t.Fatal("employee realtime session invalidation ran before the committed password update")
		}
		for _, sessionID := range []int64{ownA.ID, ownB.ID} {
			currentSession := repositories.LoginSessionRepository.Get(db, sessionID)
			if currentSession == nil || currentSession.RevokedAt == nil {
				t.Fatal("employee realtime session invalidation ran before committed session revocation")
			}
		}
	}
	if err := service.ResetPassword(context.Background(), tokenA, "dashboard", "synthetic-new-password", "synthetic-new-password"); err != nil {
		t.Fatal("valid reset failed")
	}

	persisted := repositories.UserRepository.Get(db, user.ID)
	if bcrypt.CompareHashAndPassword([]byte(persisted.Password), []byte("synthetic-new-password")) != nil {
		t.Fatal("new password was not stored as a bcrypt hash")
	}
	if bcrypt.CompareHashAndPassword([]byte(persisted.Password), []byte("synthetic-old-password")) == nil {
		t.Fatal("old password remained valid")
	}
	for _, sessionID := range []int64{ownA.ID, ownB.ID} {
		current := repositories.LoginSessionRepository.Get(db, sessionID)
		if current == nil || current.RevokedAt == nil {
			t.Fatal("an account login session was not revoked")
		}
	}
	if current := repositories.LoginSessionRepository.Get(db, otherSession.ID); current == nil || current.RevokedAt != nil {
		t.Fatal("reset revoked another account's login session")
	}
	var usedRow, siblingRow models.PasswordResetToken
	if err := db.First(&usedRow, rowA.ID).Error; err != nil {
		t.Fatalf("read consumed token row: %v", err)
	}
	if usedRow.UsedAt == nil {
		t.Fatal("successful reset did not consume its token")
	}
	if err := db.First(&siblingRow, rowB.ID).Error; err != nil {
		t.Fatalf("read sibling token row: %v", err)
	}
	if siblingRow.UsedAt == nil {
		t.Fatal("successful reset did not invalidate the user's sibling token")
	}
	if calls := invalidator.employeeCalls(); len(calls) != 1 {
		t.Fatal("employee realtime invalidation did not run exactly once after commit")
	}
	if got := guestContext.GetHeader("Authorization"); got != guestToken {
		t.Fatal("password recovery changed the Guest CustomerSessionToken")
	}
	if _, err := CustomerSessionService.VerifyRequest(guestContext, channel); err != nil {
		t.Fatal("password recovery invalidated the Guest CustomerSessionToken")
	}
	var guestHistoryAfter int64
	if err := db.Model(&models.Conversation{}).Where("customer_id = ?", guest.ID).Count(&guestHistoryAfter).Error; err != nil {
		t.Fatalf("count Guest history after reset: %v", err)
	}
	if guestHistoryAfter != guestHistoryBefore {
		t.Fatal("password recovery changed Guest history")
	}
}

func TestPasswordRecoveryConcurrentTokenConsumptionHasExactlyOneWinner(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	invalidator := &passwordRecoveryInvalidatorSpy{}
	service := newPasswordRecoveryService(&passwordRecoveryTestMailer{}, invalidator)
	user := createPasswordRecoveryUser(t, db, "synthetic-dashboard", "dashboard@example.test", enums.UserTypeEmployee, enums.StatusOk, "synthetic-old-password")
	session := createPasswordRecoveryLoginSession(t, db, user.ID, "synthetic-concurrent-session")
	token := syntheticResetToken(44)
	insertPasswordResetToken(t, db, user.ID, "dashboard", token, time.Now().Add(time.Hour), nil)

	start := make(chan struct{})
	type resetResult struct {
		password string
		err      error
	}
	results := make(chan resetResult, 2)
	passwords := []string{"synthetic-winner-password-a", "synthetic-winner-password-b"}
	var ready sync.WaitGroup
	ready.Add(2)
	for _, password := range passwords {
		password := password
		go func() {
			ready.Done()
			<-start
			results <- resetResult{password: password, err: service.ResetPassword(context.Background(), token, "dashboard", password, password)}
		}()
	}
	ready.Wait()
	close(start)
	first, second := <-results, <-results
	successes := 0
	if first.err == nil {
		successes++
	}
	if second.err == nil {
		successes++
	}
	if successes != 1 {
		t.Fatalf("concurrent reset success count = %d, want exactly one", successes)
	}

	successful := first
	if successful.err != nil {
		successful = second
	}
	persisted := repositories.UserRepository.Get(db, user.ID)
	if persisted == nil || bcrypt.CompareHashAndPassword([]byte(persisted.Password), []byte(successful.password)) != nil {
		t.Fatal("final password does not belong to the successful request")
	}
	winner := 0
	for _, password := range passwords {
		if bcrypt.CompareHashAndPassword([]byte(persisted.Password), []byte(password)) == nil {
			winner++
		}
	}
	if winner != 1 {
		t.Fatal("final password did not match exactly one concurrent winner")
	}
	var stored models.PasswordResetToken
	if err := db.Where("token_hash = ?", passwordResetTokenHash(token)).First(&stored).Error; err != nil {
		t.Fatalf("read final reset token state: %v", err)
	}
	if stored.UsedAt == nil {
		t.Fatal("concurrent reset did not persist token consumption")
	}
	currentSession := repositories.LoginSessionRepository.Get(db, session.ID)
	if currentSession == nil || currentSession.RevokedAt == nil {
		t.Fatal("winning reset did not revoke the account session")
	}
	if calls := len(invalidator.employeeCalls()); calls != 1 {
		t.Fatalf("employee realtime invalidation calls = %d, want 1", calls)
	}
}

func passwordResetTokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
