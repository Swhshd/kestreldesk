package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/email"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/i18nx"
)

const (
	mailpitIntegrationEnabledEnv = "PASSWORD_RECOVERY_MAILPIT_INTEGRATION"
	mailpitAPIURLEnv             = "PASSWORD_RECOVERY_MAILPIT_API_URL"
	mailpitSMTPHostEnv           = "PASSWORD_RECOVERY_MAILPIT_SMTP_HOST"
	mailpitTrustedBaseURLEnv     = "PASSWORD_RECOVERY_MAILPIT_TRUSTED_BASE_URL"
	mailpitTestRecipient         = "password-recovery-mailpit@example.test"
	mailpitTestTrustedBaseURL    = "http://127.0.0.1:8083"
)

func passwordRecoveryEphemeralTestPassword(t *testing.T) string {
	t.Helper()
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		t.Fatalf("could not generate ephemeral test account credential: %v", err)
	}
	return hex.EncodeToString(randomBytes)
}

type mailpitMessageList struct {
	Messages []mailpitMessageSummary `json:"messages"`
}

type mailpitMessageSummary struct {
	ID      string `json:"ID"`
	Subject string `json:"Subject"`
	To      []struct {
		Address string `json:"Address"`
	} `json:"To"`
}

type mailpitMessageDetail struct {
	Text string `json:"Text"`
}

// This test is opt-in because it needs a live Mailpit service and a runner on
// the same Compose network. It creates only an in-memory synthetic account.
func TestPasswordRecoveryMailpitIntegration(t *testing.T) {
	if os.Getenv(mailpitIntegrationEnabledEnv) != "1" {
		t.Skip("set PASSWORD_RECOVERY_MAILPIT_INTEGRATION=1 on the Mailpit Compose network")
	}

	apiURL := strings.TrimRight(strings.TrimSpace(os.Getenv(mailpitAPIURLEnv)), "/")
	smtpHost := strings.TrimSpace(os.Getenv(mailpitSMTPHostEnv))
	if apiURL == "" || smtpHost == "" {
		t.Fatal("Mailpit integration requires its API URL and SMTP host")
	}
	trustedBaseURL := strings.TrimRight(strings.TrimSpace(os.Getenv(mailpitTrustedBaseURLEnv)), "/")
	if trustedBaseURL == "" {
		trustedBaseURL = mailpitTestTrustedBaseURL
	}

	client := &http.Client{Timeout: 5 * time.Second}
	before, err := listMailpitMessages(client, apiURL)
	if err != nil {
		t.Fatalf("Mailpit API was unavailable before the request (status detail omitted): %v", err)
	}
	knownIDs := make(map[string]struct{}, len(before))
	for _, message := range before {
		knownIDs[message.ID] = struct{}{}
	}
	db := setupPasswordRecoveryTest(t)
	expectedSubject := i18nx.Getf(i18nx.DefaultLocale, "email.passwordReset.subject")
	cfg := config.GetCurrent()
	cfg.Email.Enabled = true
	cfg.Email.Provider = string(email.ProviderSMTP)
	cfg.Email.FromAddress = "no-reply@kestreldesk.local"
	cfg.Email.FromName = "KestrelDesk"
	cfg.Email.SMTPHost = smtpHost
	cfg.Email.SMTPPort = 1025
	cfg.Email.SMTPUser = ""
	cfg.Email.SMTPPassword = ""
	cfg.Email.SMTPUseTLS = false
	cfg.Email.SMTPSecurity = "none"
	cfg.Email.SMTPTimeoutSeconds = 5
	cfg.Email.PasswordResetBaseURL = trustedBaseURL
	cfg.Email.PasswordResetTTLMinutes = 30

	createPasswordRecoveryUser(
		t,
		db,
		"mailpit-integration-user",
		mailpitTestRecipient,
		enums.UserTypeEmployee,
		enums.StatusOk,
		passwordRecoveryEphemeralTestPassword(t),
	)
	service := newPasswordRecoveryService(nil, &passwordRecoveryInvalidatorSpy{})
	if err := service.RequestReset(context.Background(), "mailpit-integration-user", PasswordRecoverySurfaceDashboard, "203.0.113.99"); err != nil {
		t.Fatalf("password recovery request failed unexpectedly: %v", err)
	}

	var received []mailpitMessageSummary
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		messages, err := listMailpitMessages(client, apiURL)
		if err != nil {
			t.Fatalf("Mailpit API failed while awaiting delivery (status detail omitted): %v", err)
		}
		received = nil
		for _, message := range messages {
			if _, existed := knownIDs[message.ID]; existed {
				continue
			}
			if message.Subject == expectedSubject && mailpitRecipientMatches(message, mailpitTestRecipient) {
				received = append(received, message)
			}
		}
		if len(received) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(received) != 1 {
		t.Fatalf("Mailpit received %d matching new reset messages, want exactly one", len(received))
	}

	detail, err := getMailpitMessage(client, apiURL, received[0].ID)
	if err != nil {
		t.Fatalf("Mailpit message detail could not be read: %v", err)
	}
	if received[0].Subject != expectedSubject {
		t.Error("reset email subject did not match the configured default locale")
	}
	const linkPlaceholder = "__RESET_LINK__"
	expectedBody := i18nx.Getf(i18nx.DefaultLocale, "email.passwordReset.body", linkPlaceholder, 30)
	placeholderIndex := strings.Index(expectedBody, linkPlaceholder)
	if placeholderIndex < 0 || !strings.Contains(detail.Text, expectedBody[placeholderIndex+len(linkPlaceholder):]) {
		t.Error("reset email did not contain the localized expiry wording")
	}
	linkText := firstHTTPURL(detail.Text)
	if linkText == "" {
		t.Fatal("reset email did not contain a URL")
	}
	resetURL, err := url.Parse(strings.TrimRight(linkText, ".,;!:)"))
	if err != nil {
		t.Fatal("reset email URL was invalid")
	}
	trustedBase, err := url.Parse(trustedBaseURL)
	if err != nil || resetURL.Scheme != trustedBase.Scheme || resetURL.Host != trustedBase.Host {
		t.Error("reset email URL did not use the configured trusted base URL")
	}
	if resetURL.Path != "/dashboard/reset-password" {
		t.Error("reset email URL did not target the Dashboard reset page")
	}
	if resetURL.Query().Get("token") == "" {
		t.Error("reset email URL did not contain a reset token")
	}
}

func TestPasswordRecoverySMTPTimeoutIsBoundedAndSanitized(t *testing.T) {
	db := setupPasswordRecoveryTest(t)
	cfg := config.GetCurrent()
	cfg.Email.SMTPTimeoutSeconds = 1

	logWriter := &passwordRecoveryTestLogWriter{}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logWriter, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	mailer := &passwordRecoveryTestMailer{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	service := newPasswordRecoveryService(mailer, &passwordRecoveryInvalidatorSpy{})
	user := createPasswordRecoveryUser(
		t,
		db,
		"smtp-timeout-user",
		"smtp-timeout@example.test",
		enums.UserTypeEmployee,
		enums.StatusOk,
		passwordRecoveryEphemeralTestPassword(t),
	)

	startedAt := time.Now()
	if err := service.RequestReset(context.Background(), user.Username, PasswordRecoverySurfaceDashboard, "203.0.113.98"); err != nil {
		t.Fatalf("public password recovery request exposed the delivery timeout: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed < passwordResetRequestMinimumDuration {
		t.Error("password recovery request returned before the common minimum response floor")
	}
	select {
	case <-mailer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("mail worker did not begin the timeout test delivery")
	}
	select {
	case <-mailer.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("mail worker did not stop after its configured SMTP timeout")
	}

	messages := waitForPasswordRecoveryMessages(t, mailer, 1)
	token, _ := resetTokenFromMessage(t, messages[0].BodyText)
	waitForPasswordRecoveryTokenUsed(t, db, passwordResetTokenHash(token))
	const failureLogMessage = "password recovery email delivery failed"
	logDeadline := time.Now().Add(time.Second)
	for !strings.Contains(logWriter.String(), failureLogMessage) && time.Now().Before(logDeadline) {
		time.Sleep(5 * time.Millisecond)
	}
	logs := logWriter.String()
	if !strings.Contains(logs, failureLogMessage) {
		t.Error("SMTP timeout did not produce the fixed sanitized failure log")
	}
	if strings.Contains(logs, "context deadline exceeded") || strings.Contains(logs, token) {
		t.Error("SMTP timeout logs exposed transport or reset-token details")
	}
}

func listMailpitMessages(client *http.Client, apiURL string) ([]mailpitMessageSummary, error) {
	request, err := http.NewRequest(http.MethodGet, apiURL+"/api/v1/messages?start=0&limit=200", nil)
	if err != nil {
		return nil, errors.New("invalid Mailpit API URL")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("Mailpit API request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Mailpit API returned HTTP %d", response.StatusCode)
	}
	var result mailpitMessageList
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, errors.New("Mailpit API response was invalid")
	}
	return result.Messages, nil
}

func getMailpitMessage(client *http.Client, apiURL, id string) (mailpitMessageDetail, error) {
	request, err := http.NewRequest(http.MethodGet, apiURL+"/api/v1/message/"+url.PathEscape(id), nil)
	if err != nil {
		return mailpitMessageDetail{}, errors.New("invalid Mailpit API URL")
	}
	response, err := client.Do(request)
	if err != nil {
		return mailpitMessageDetail{}, errors.New("Mailpit message request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return mailpitMessageDetail{}, fmt.Errorf("Mailpit message API returned HTTP %d", response.StatusCode)
	}
	var result mailpitMessageDetail
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return mailpitMessageDetail{}, errors.New("Mailpit message response was invalid")
	}
	return result, nil
}

func mailpitRecipientMatches(message mailpitMessageSummary, recipient string) bool {
	for _, address := range message.To {
		if strings.EqualFold(address.Address, recipient) {
			return true
		}
	}
	return false
}

var mailpitURLPattern = regexp.MustCompile(`https?://[^\s<>"']+`)

func firstHTTPURL(text string) string {
	return mailpitURLPattern.FindString(text)
}
