package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/gin-gonic/gin"
)

type passwordRecoveryOperationsStub struct {
	requestCalls int
	resetCalls   int
	requestErr   error
	resetErr     error
	deliver      bool
	eligible     bool
	canonicalID  int64
	canonicalIDs map[string]int64
	admittedIDs  []int64
	admissions   []bool
	identifier   string
	requestIP    string
	requestScope string
	resetToken   string
	resetScope   string
	newPassword  string
	confirmation string
}

func (s *passwordRecoveryOperationsStub) RequestResetWithAdmissionCallback(_ context.Context, identifier, surface, sourceIP string, admit func(int64) (func(), bool), onDelivered func(int64)) error {
	s.requestCalls++
	s.identifier = identifier
	s.requestIP = sourceIP
	s.requestScope = surface
	if !s.eligible {
		return s.requestErr
	}
	canonicalID := s.canonicalID
	if id, ok := s.canonicalIDs[normalizePasswordRecoveryTestIdentifier(identifier)]; ok {
		canonicalID = id
	}
	var release func()
	accepted := true
	if admit != nil {
		release, accepted = admit(canonicalID)
	}
	s.admittedIDs = append(s.admittedIDs, canonicalID)
	s.admissions = append(s.admissions, accepted)
	if !accepted || s.requestErr != nil {
		if release != nil {
			release()
		}
		return s.requestErr
	}
	if accepted && s.deliver && onDelivered != nil {
		onDelivered(canonicalID)
	}
	// A nil error represents a token persisted and its queued job admitted.
	return s.requestErr
}

func normalizePasswordRecoveryTestIdentifier(identifier string) string {
	return strings.ToLower(strings.TrimSpace(identifier))
}

func (s *passwordRecoveryOperationsStub) ResetPassword(_ context.Context, token, surface, password, confirmation string) error {
	s.resetCalls++
	s.resetToken = token
	s.resetScope = surface
	s.newPassword = password
	s.confirmation = confirmation
	return s.resetErr
}

func newPasswordRecoveryTestRouter(controls *PasswordRecoveryControls, service PasswordRecoveryOperations) *gin.Engine {
	gin.SetMode(gin.TestMode)
	app := gin.New()
	app.POST("/api/auth/password_reset/request",
		PasswordRecoveryRequestIPLimit(controls),
		PasswordResetRequestHandler("dashboard", controls, service),
	)
	app.POST("/api/support/auth/password_reset/request",
		PasswordRecoveryRequestIPLimit(controls),
		SupportAuthPasswordResetRequestHandler(controls, service),
	)
	app.POST("/api/auth/password_reset/reset", PasswordRecoveryResetIPLimit(controls), AuthPasswordResetHandler(service))
	app.POST("/api/support/auth/password_reset/reset", PasswordRecoveryResetIPLimit(controls), PasswordResetHandler("support", service))
	return app
}

func recoveryTestPost(app *gin.Engine, path, body, ip string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":52000"
	app.ServeHTTP(rec, req)
	return rec
}

func decodeRecoveryResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode recovery response: %v", err)
	}
	return body
}

func TestPasswordResetRequestReturnsTheSameGenericResponseForEveryOutcome(t *testing.T) {
	cases := []struct {
		name       string
		deliver    bool
		serviceErr error
	}{
		{name: "eligible account", deliver: true},
		{name: "nonexistent account"},
		{name: "wrong surface principal"},
		{name: "disabled account"},
		{name: "account without usable email"},
		{name: "mail delivery error", serviceErr: errors.New("synthetic SMTP failure details")},
	}

	var firstBody string
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &passwordRecoveryOperationsStub{deliver: tc.deliver, eligible: tc.deliver || tc.serviceErr != nil, canonicalID: 40, requestErr: tc.serviceErr}
			app := newPasswordRecoveryTestRouter(NewPasswordRecoveryControls(), service)
			rec := recoveryTestPost(app, "/api/auth/password_reset/request",
				`{"identifier":"synthetic@example.test","surface":"support"}`, "203.0.113.40")

			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
			}
			body := decodeRecoveryResponse(t, rec)
			if body["success"] != true || body["errorCode"] != float64(0) {
				t.Fatalf("unexpected generic response envelope: %s", rec.Body.String())
			}
			data, ok := body["data"].(map[string]any)
			if !ok || data["message"] != "如果该账号存在，我们会向绑定邮箱发送密码重置邮件。" {
				t.Fatalf("unexpected generic response data: %s", rec.Body.String())
			}
			if service.requestCalls != 1 || service.requestScope != "dashboard" {
				t.Fatalf("request calls=%d route surface=%q; want one call on dashboard", service.requestCalls, service.requestScope)
			}
			if strings.Contains(rec.Body.String(), "SMTP") || strings.Contains(rec.Body.String(), "synthetic@example.test") {
				t.Fatalf("response disclosed request details: %s", rec.Body.String())
			}
			if firstBody == "" {
				firstBody = rec.Body.String()
			} else if rec.Body.String() != firstBody {
				t.Fatalf("generic response differs by account/delivery outcome: got %s, want %s", rec.Body.String(), firstBody)
			}
		})
	}
}

type blockingPasswordRecoveryOperations struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockingPasswordRecoveryOperations) RequestResetWithAdmissionCallback(_ context.Context, _, _, _ string, _ func(int64) (func(), bool), _ func(int64)) error {
	close(s.started)
	<-s.release
	return nil
}

func (*blockingPasswordRecoveryOperations) ResetPassword(context.Context, string, string, string, string) error {
	return nil
}

func TestPasswordResetRequestReturnsBeforeAccountLookupCompletes(t *testing.T) {
	service := &blockingPasswordRecoveryOperations{started: make(chan struct{}), release: make(chan struct{})}
	app := newPasswordRecoveryTestRouter(NewPasswordRecoveryControls(), service)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/password_reset/request", strings.NewReader(`{"identifier":"synthetic@example.test"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.40:52000"
	done := make(chan struct{})
	go func() {
		app.ServeHTTP(rec, req)
		close(done)
	}()
	t.Cleanup(func() {
		select {
		case <-service.release:
		default:
			close(service.release)
		}
	})

	select {
	case <-service.started:
	case <-time.After(time.Second):
		t.Fatal("background recovery work did not start")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("forgot-password response waited for account lookup work")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want generic %d", rec.Code, http.StatusOK)
	}
	if got := decodeRecoveryResponse(t, rec)["data"].(map[string]any)["message"]; got != "如果该账号存在，我们会向绑定邮箱发送密码重置邮件。" {
		t.Fatalf("unexpected generic forgot-password response: %v", got)
	}
}

func TestPasswordResetRequestNormalizesAccountThrottleKey(t *testing.T) {
	service := &passwordRecoveryOperationsStub{
		eligible: true,
		canonicalIDs: map[string]int64{
			"alice@example.test": 41,
			"other@example.test": 42,
		},
	}
	controls := NewPasswordRecoveryControls()
	app := newPasswordRecoveryTestRouter(controls, service)
	inputs := []string{
		" Alice@example.test ",
		"alice@example.test",
		"ALICE@example.test",
		" Alice@Example.Test",
		"alice@example.test ",
		"ALICE@EXAMPLE.TEST",
	}

	var firstBody string
	for _, identifier := range inputs {
		body := `{"identifier":"` + identifier + `"}`
		rec := recoveryTestPost(app, "/api/auth/password_reset/request", body, "203.0.113.41")
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d want %d for normalized identifier", rec.Code, http.StatusOK)
		}
		if firstBody == "" {
			firstBody = rec.Body.String()
		} else if rec.Body.String() != firstBody {
			t.Fatalf("account throttling changed generic response: got %s want %s", rec.Body.String(), firstBody)
		}
	}
	if service.requestCalls != 6 || len(service.admissions) != 6 {
		t.Fatalf("service calls=%d admissions=%d want six service resolutions and admission decisions", service.requestCalls, len(service.admissions))
	}
	for i, accepted := range service.admissions {
		if accepted != (i < 5) || service.admittedIDs[i] != 41 {
			t.Fatalf("admission %d accepted=%v canonicalID=%d; want accepted=%v for canonical user 41", i+1, accepted, service.admittedIDs[i], i < 5)
		}
	}

	other := recoveryTestPost(app, "/api/auth/password_reset/request", `{"identifier":"other@example.test"}`, "203.0.113.41")
	if other.Code != http.StatusOK || service.requestCalls != 7 || !service.admissions[6] || service.admittedIDs[6] != 42 {
		t.Fatalf("different canonical user did not get a separate budget: status=%d calls=%d admissions=%v IDs=%v", other.Code, service.requestCalls, service.admissions, service.admittedIDs)
	}
}

func TestPasswordResetAccountThrottleUsesCanonicalUserAcrossIdentifierAliases(t *testing.T) {
	service := &passwordRecoveryOperationsStub{
		eligible: true,
		canonicalIDs: map[string]int64{
			"alice":                77,
			"alice@example.test":   77,
			"+15551234567":         77,
			"another@example.test": 77,
			"another-username":     77,
			"sixth@example.test":   77,
			"fresh@example.test":   78,
		},
	}
	app := newPasswordRecoveryTestRouter(NewPasswordRecoveryControls(), service)
	aliases := []string{"alice", "alice@example.test", "+15551234567", "another@example.test", "another-username", "sixth@example.test", "fresh@example.test"}
	for i, identifier := range aliases {
		rec := recoveryTestPost(app, "/api/auth/password_reset/request", `{"identifier":"`+identifier+`"}`, "203.0.113.47")
		if rec.Code != http.StatusOK {
			t.Fatalf("alias %q status=%d want generic 200", identifier, rec.Code)
		}
		if i < 5 && (len(service.admissions) <= i || !service.admissions[i]) {
			t.Fatalf("alias %q was denied before canonical account budget was exhausted", identifier)
		}
	}
	if len(service.admissions) != 7 || len(service.admittedIDs) != 7 {
		t.Fatalf("admission decisions=%v IDs=%v; want seven canonical decisions", service.admissions, service.admittedIDs)
	}
	if service.admittedIDs[5] != 77 || service.admissions[5] {
		t.Fatalf("sixth alias decision ID=%d accepted=%v; want canonical user 77 denied at budget", service.admittedIDs[5], service.admissions[5])
	}
	if service.admittedIDs[6] != 78 || !service.admissions[6] {
		t.Fatalf("separate canonical user ID=%d accepted=%v; want user 78 admitted", service.admittedIDs[6], service.admissions[6])
	}
}

func TestPasswordResetCooldownUsesCanonicalUserAcrossIdentifierAliases(t *testing.T) {
	service := &passwordRecoveryOperationsStub{
		eligible: true,
		deliver:  true,
		canonicalIDs: map[string]int64{
			"alice":              88,
			"alice@example.test": 88,
		},
	}
	app := newPasswordRecoveryTestRouter(NewPasswordRecoveryControls(), service)
	for _, identifier := range []string{"alice", "alice@example.test"} {
		rec := recoveryTestPost(app, "/api/auth/password_reset/request", `{"identifier":"`+identifier+`"}`, "203.0.113.48")
		if rec.Code != http.StatusOK {
			t.Fatalf("alias %q status=%d want generic 200", identifier, rec.Code)
		}
	}
	if len(service.admissions) != 2 || !service.admissions[0] || service.admissions[1] ||
		service.admittedIDs[0] != 88 || service.admittedIDs[1] != 88 {
		t.Fatalf("canonical cooldown decisions=%v IDs=%v; want first admitted and alias suppressed for user 88", service.admissions, service.admittedIDs)
	}
}

func TestPasswordResetAccountAdmissionIsAtomicAcrossAliases(t *testing.T) {
	controls := NewPasswordRecoveryControls()
	const callers = 100
	var allowed atomic.Int64
	var group sync.WaitGroup
	group.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer group.Done()
			release, admitted := controls.reserveAccount(99)
			_ = release // Keep successful reservations to model committed requests.
			if admitted {
				allowed.Add(1)
			}
		}()
	}
	group.Wait()
	if got := allowed.Load(); got != 5 {
		t.Fatalf("concurrent aliases admitted %d requests, want exactly the account limit 5", got)
	}
}

func TestPasswordRecoveryIPStateIsBoundedAndReclaimsExpiredKeys(t *testing.T) {
	now := time.Date(2026, time.October, 11, 0, 0, 0, 0, time.UTC)
	controls := newPasswordRecoveryControlsWithCapacity(2, 2, 2, func() time.Time { return now })
	for _, ip := range []string{"ip-a", "ip-b"} {
		if !controls.allowRequestIP(ip) {
			t.Fatalf("initial IP key %q was denied", ip)
		}
	}
	for i := 1; i < passwordRecoveryIPLimit; i++ {
		if !controls.allowRequestIP("ip-a") {
			t.Fatalf("existing IP key was denied before its request limit at request %d", i+1)
		}
	}
	if controls.allowRequestIP("ip-c") {
		t.Fatal("new IP key was admitted while the bounded IP state was full")
	}
	if got := len(controls.requestIPs); got != 2 {
		t.Fatalf("tracked IP keys=%d, want hard capacity 2", got)
	}
	firstSweep := controls.lastRequestIPSweep
	now = now.Add(30 * time.Second)
	if controls.allowRequestIP("ip-d") || !controls.lastRequestIPSweep.Equal(firstSweep) {
		t.Fatal("full IP state did not fail closed without repeating a capacity sweep")
	}
	if !controls.allowRequestIP("ip-b") {
		t.Fatal("existing IP key was evicted or denied while its window was live")
	}

	now = now.Add(passwordRecoveryLimitWindow + time.Second)
	if !controls.allowRequestIP("ip-c") {
		t.Fatal("expired IP keys were not reclaimed for a new key")
	}
	if got := len(controls.requestIPs); got != 1 {
		t.Fatalf("tracked IP keys after expiry sweep=%d, want 1", got)
	}
}

func TestPasswordRecoveryAccountStateIsBoundedAndReclaimsExpiredKeys(t *testing.T) {
	now := time.Date(2026, time.October, 11, 0, 0, 0, 0, time.UTC)
	controls := newPasswordRecoveryControlsWithCapacity(2, 2, 2, func() time.Time { return now })
	for i := 0; i < passwordRecoveryAccountLimit; i++ {
		if _, admitted := controls.reserveAccount(101); !admitted {
			t.Fatalf("existing user was denied before its account limit at request %d", i+1)
		}
	}
	if _, admitted := controls.reserveAccount(102); !admitted {
		t.Fatal("second user was denied below capacity")
	}
	release, admitted := controls.reserveAccount(102)
	if !admitted {
		t.Fatal("existing user was denied while the account state was at capacity")
	}
	release()
	controls.markDelivered(102)
	if _, admitted := controls.reserveAccount(102); admitted {
		t.Fatal("delivery cooldown for an existing user was not preserved")
	}
	if _, admitted := controls.reserveAccount(103); admitted {
		t.Fatal("new user was admitted while the bounded account state was full")
	}
	if got := len(controls.accounts); got != 2 {
		t.Fatalf("tracked account keys=%d, want hard capacity 2", got)
	}
	firstSweep := controls.lastAccountSweep
	now = now.Add(30 * time.Second)
	if _, admitted := controls.reserveAccount(104); admitted || !controls.lastAccountSweep.Equal(firstSweep) {
		t.Fatal("full account state did not fail closed without repeating a capacity sweep")
	}
	controls.markDelivered(104)
	if got := len(controls.accounts); got != 2 {
		t.Fatalf("delivery callback exceeded hard account capacity: tracked %d keys", got)
	}
	if _, admitted := controls.reserveAccount(101); admitted {
		t.Fatal("existing user counter was evicted while its request window was live")
	}

	now = now.Add(passwordRecoveryLimitWindow + time.Second)
	if _, admitted := controls.reserveAccount(103); !admitted {
		t.Fatal("expired account keys were not reclaimed for a new user")
	}
	if got := len(controls.accounts); got != 1 {
		t.Fatalf("tracked account keys after expiry sweep=%d, want 1", got)
	}
}

func TestPasswordRecoveryIPCapacityOverflowKeepsGenericLatencyAndResponse(t *testing.T) {
	controls := newPasswordRecoveryControlsWithCapacity(1, 1, 2, time.Now)
	if !controls.allowRequestIP("203.0.113.61") {
		t.Fatal("failed to seed the IP capacity")
	}
	service := &passwordRecoveryOperationsStub{}
	app := newPasswordRecoveryTestRouter(controls, service)
	baseline := recoveryTestPost(app, "/api/auth/password_reset/request", `{"identifier":"missing"}`, "203.0.113.61")
	started := time.Now()
	overflow := recoveryTestPost(app, "/api/auth/password_reset/request", `{"identifier":"missing"}`, "203.0.113.62")
	if elapsed := time.Since(started); elapsed < passwordRecoveryRequestFloor-10*time.Millisecond {
		t.Fatalf("IP capacity overflow took %s; want the 250ms common floor", elapsed)
	}
	if overflow.Code != http.StatusOK || overflow.Body.String() != baseline.Body.String() {
		t.Fatalf("IP capacity overflow response=%d %s; want the same generic response %d %s", overflow.Code, overflow.Body.String(), baseline.Code, baseline.Body.String())
	}
	if service.requestCalls != 1 {
		t.Fatalf("service calls=%d, want overflow IP to be suppressed before service", service.requestCalls)
	}
}

func TestPasswordRecoveryAccountCapacityOverflowKeepsGenericLatencyAndResponse(t *testing.T) {
	controls := newPasswordRecoveryControlsWithCapacity(2, 2, 1, time.Now)
	for i := 0; i < passwordRecoveryAccountLimit; i++ {
		if _, admitted := controls.reserveAccount(201); !admitted {
			t.Fatalf("failed to seed existing account request %d", i+1)
		}
	}
	service := &passwordRecoveryOperationsStub{}
	app := newPasswordRecoveryTestRouter(controls, service)
	baseline := recoveryTestPost(app, "/api/auth/password_reset/request", `{"identifier":"missing"}`, "203.0.113.63")
	service.eligible = true
	service.canonicalID = 202
	started := time.Now()
	overflow := recoveryTestPost(app, "/api/auth/password_reset/request", `{"identifier":"known-alias"}`, "203.0.113.63")
	if elapsed := time.Since(started); elapsed < passwordRecoveryRequestFloor-10*time.Millisecond {
		t.Fatalf("account capacity overflow took %s; want the 250ms common floor", elapsed)
	}
	if overflow.Code != http.StatusOK || overflow.Body.String() != baseline.Body.String() {
		t.Fatalf("account capacity overflow response=%d %s; want the same generic response %d %s", overflow.Code, overflow.Body.String(), baseline.Code, baseline.Body.String())
	}
	if service.requestCalls != 2 || len(service.admissions) != 1 || service.admissions[0] {
		t.Fatalf("service calls=%d admissions=%v; want an internal fail-closed admission", service.requestCalls, service.admissions)
	}
}

func TestPasswordRecoveryResetIPStateIsBoundedAndReclaimsExpiredKeys(t *testing.T) {
	now := time.Date(2026, time.October, 11, 0, 0, 0, 0, time.UTC)
	controls := newPasswordRecoveryControlsWithCapacity(2, 2, 2, func() time.Time { return now })
	for _, ip := range []string{"reset-ip-a", "reset-ip-b"} {
		if allowed, _ := controls.allowResetIP(ip); !allowed {
			t.Fatalf("initial reset IP key %q was denied", ip)
		}
	}
	for i := 1; i < passwordRecoveryIPLimit; i++ {
		if allowed, _ := controls.allowResetIP("reset-ip-a"); !allowed {
			t.Fatalf("existing reset IP key was denied before request limit at request %d", i+1)
		}
	}
	if allowed, _ := controls.allowResetIP("reset-ip-c"); allowed {
		t.Fatal("new reset IP key was admitted while bounded state was full")
	}
	if got := len(controls.resetIPs); got != 2 {
		t.Fatalf("tracked reset IP keys=%d, want hard capacity 2", got)
	}
	firstSweep := controls.lastResetIPSweep
	now = now.Add(30 * time.Second)
	if allowed, _ := controls.allowResetIP("reset-ip-d"); allowed || !controls.lastResetIPSweep.Equal(firstSweep) {
		t.Fatal("full reset IP state did not fail closed without repeating a capacity sweep")
	}
	if allowed, _ := controls.allowResetIP("reset-ip-b"); !allowed {
		t.Fatal("existing reset IP key was evicted or denied while its window was live")
	}

	now = now.Add(passwordRecoveryLimitWindow + time.Second)
	if allowed, _ := controls.allowResetIP("reset-ip-c"); !allowed {
		t.Fatal("expired reset IP keys were not reclaimed for a new key")
	}
	if got := len(controls.resetIPs); got != 1 {
		t.Fatalf("tracked reset IP keys after expiry sweep=%d, want 1", got)
	}
}

func TestPasswordRecoveryResetAndRequestIPBudgetsAreSeparate(t *testing.T) {
	controls := newPasswordRecoveryControlsWithCapacity(1, 1, 1, time.Now)
	const sharedSourceIP = "203.0.113.64"
	for i := 0; i < passwordRecoveryIPLimit; i++ {
		if !controls.allowRequestIP(sharedSourceIP) {
			t.Fatalf("forgot-request IP budget rejected request %d", i+1)
		}
	}
	for i := 0; i < passwordRecoveryIPLimit; i++ {
		if allowed, _ := controls.allowResetIP(sharedSourceIP); !allowed {
			t.Fatalf("reset IP budget consumed by forgot requests at reset %d", i+1)
		}
	}
	if controls.allowRequestIP(sharedSourceIP) {
		t.Fatal("forgot-request budget did not enforce its own quota")
	}
	if allowed, _ := controls.allowResetIP(sharedSourceIP); allowed {
		t.Fatal("reset budget did not enforce its own quota")
	}
}

func TestPasswordRecoveryResetIPLimitReturnsStandard429AndNilDisables(t *testing.T) {
	controls := newPasswordRecoveryControlsWithCapacity(1, 1, 1, time.Now)
	if allowed, _ := controls.allowResetIP("203.0.113.65"); !allowed {
		t.Fatal("failed to seed reset-IP capacity")
	}
	service := &passwordRecoveryOperationsStub{}
	app := newPasswordRecoveryTestRouter(controls, service)
	body := `{"token":"synthetic-token","newPassword":"synthetic-password","confirmPassword":"synthetic-password"}`
	rec := recoveryTestPost(app, "/api/auth/password_reset/reset", body, "203.0.113.66")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("new reset IP at capacity status=%d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("capacity Retry-After=%q, want 60 seconds until the next bounded sweep", rec.Header().Get("Retry-After"))
	}
	response := decodeRecoveryResponse(t, rec)
	if response["success"] != false || response["message"] == "" {
		t.Fatalf("reset limit response is not the standard JSON error envelope: %s", rec.Body.String())
	}
	if service.resetCalls != 0 {
		t.Fatalf("rate-limited reset invoked the service %d times", service.resetCalls)
	}

	app = newPasswordRecoveryTestRouter(nil, service)
	for i := 0; i < passwordRecoveryIPLimit+5; i++ {
		rec = recoveryTestPost(app, "/api/support/auth/password_reset/reset", body, "203.0.113.66")
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("reset %d was throttled with nil controls", i+1)
		}
	}
}

func TestPasswordResetCooldownSuppressionMeetsCommonLatencyFloor(t *testing.T) {
	service := &passwordRecoveryOperationsStub{
		eligible:    true,
		deliver:     true,
		canonicalID: 101,
	}
	app := newPasswordRecoveryTestRouter(NewPasswordRecoveryControls(), service)
	first := recoveryTestPost(app, "/api/auth/password_reset/request", `{"identifier":"alice"}`, "203.0.113.49")
	if first.Code != http.StatusOK {
		t.Fatalf("first request status=%d want generic 200", first.Code)
	}
	started := time.Now()
	second := recoveryTestPost(app, "/api/auth/password_reset/request", `{"identifier":"alice"}`, "203.0.113.49")
	if elapsed := time.Since(started); elapsed < passwordRecoveryRequestFloor-10*time.Millisecond {
		t.Fatalf("cooldown response took %s; want the 250ms common floor (10ms observation margin)", elapsed)
	}
	if second.Code != http.StatusOK || service.requestCalls != 2 {
		t.Fatalf("cooldown response status=%d service calls=%d; want generic 200 after admission check", second.Code, service.requestCalls)
	}
}

func TestPasswordResetIPSuppressionMeetsCommonLatencyFloor(t *testing.T) {
	controls := NewPasswordRecoveryControls()
	for i := 0; i < passwordRecoveryIPLimit; i++ {
		if !controls.allowRequestIP("203.0.113.50") {
			t.Fatalf("failed to fill IP budget at request %d", i+1)
		}
	}
	service := &passwordRecoveryOperationsStub{}
	app := newPasswordRecoveryTestRouter(controls, service)
	started := time.Now()
	rec := recoveryTestPost(app, "/api/auth/password_reset/request", `{"identifier":"alice"}`, "203.0.113.50")
	if elapsed := time.Since(started); elapsed < passwordRecoveryRequestFloor-10*time.Millisecond {
		t.Fatalf("IP-suppressed response took %s; want the 250ms common floor (10ms observation margin)", elapsed)
	}
	if rec.Code != http.StatusOK || service.requestCalls != 0 {
		t.Fatalf("IP-suppressed response status=%d service calls=%d; want generic 200 and no service call", rec.Code, service.requestCalls)
	}
}

func TestPasswordResetRequestIPGateRunsBeforeTheService(t *testing.T) {
	service := &passwordRecoveryOperationsStub{}
	app := newPasswordRecoveryTestRouter(NewPasswordRecoveryControls(), service)
	var firstBody string
	for i := 0; i < 21; i++ {
		body := `{"identifier":"account-` + string(rune('a'+i)) + `@example.test"}`
		rec := recoveryTestPost(app, "/api/auth/password_reset/request", body, "203.0.113.42")
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status=%d want generic 200", i+1, rec.Code)
		}
		if firstBody == "" {
			firstBody = rec.Body.String()
		} else if rec.Body.String() != firstBody {
			t.Fatalf("IP suppression changed generic response on request %d", i+1)
		}
	}
	if service.requestCalls != 20 {
		t.Fatalf("service calls=%d want 20; IP-suppressed request must not invoke recovery work", service.requestCalls)
	}
}

func TestPasswordResetCooldownStartsOnlyWhenDeliveryCallbackRuns(t *testing.T) {
	cases := []struct {
		name    string
		deliver bool
		err     error
		want    int
	}{
		{name: "successful delivery", deliver: true, want: 2},
		{name: "ineligible account", want: 0},
		{name: "failed SMTP delivery", err: errors.New("synthetic SMTP failure details"), want: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &passwordRecoveryOperationsStub{deliver: tc.deliver, eligible: tc.deliver || tc.err != nil, canonicalID: 43, requestErr: tc.err}
			app := newPasswordRecoveryTestRouter(NewPasswordRecoveryControls(), service)
			firstBody := ""
			for _, identifier := range []string{" Alice@example.test ", "alice@example.test"} {
				rec := recoveryTestPost(app, "/api/auth/password_reset/request", `{"identifier":"`+identifier+`"}`, "203.0.113.43")
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
				}
				if firstBody == "" {
					firstBody = rec.Body.String()
				} else if rec.Body.String() != firstBody {
					t.Fatalf("cooldown changed generic response: got %s want %s", rec.Body.String(), firstBody)
				}
			}
			if len(service.admissions) != tc.want {
				t.Fatalf("admission calls=%d want %d", len(service.admissions), tc.want)
			}
		})
	}
}

func TestPasswordResetHandlerUsesTheRegisteredSurfaceAndSafeLocalizedErrors(t *testing.T) {
	service := &passwordRecoveryOperationsStub{eligible: true, canonicalID: 45}
	app := newPasswordRecoveryTestRouter(nil, service)
	rec := recoveryTestPost(app, "/api/support/auth/password_reset/reset",
		`{"token":"synthetic-token","newPassword":"synthetic-new-password","confirmPassword":"synthetic-new-password","surface":"dashboard"}`,
		"203.0.113.44")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
	}
	if service.resetCalls != 1 || service.resetScope != "support" || service.resetToken != "synthetic-token" ||
		service.newPassword != "synthetic-new-password" || service.confirmation != "synthetic-new-password" {
		t.Fatalf("reset handler did not forward the route surface and payload fields")
	}

	service.resetErr = errors.New("synthetic persistence detail")
	rec = recoveryTestPost(app, "/api/support/auth/password_reset/reset",
		`{"token":"synthetic-token","newPassword":"synthetic-new-password","confirmPassword":"synthetic-new-password"}`,
		"203.0.113.44")
	if rec.Code != http.StatusOK {
		t.Fatalf("failure status=%d want %d", rec.Code, http.StatusOK)
	}
	body := decodeRecoveryResponse(t, rec)
	if body["success"] != false || body["message"] != "密码重置暂时无法完成，请重新申请重置链接。" {
		t.Fatalf("reset failure was not safely localized: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "synthetic persistence detail") {
		t.Fatalf("reset failure disclosed an internal detail: %s", rec.Body.String())
	}

	service.resetErr = errorsx.InvalidTokenI18n("error.passwordReset.invalidToken")
	rec = recoveryTestPost(app, "/api/support/auth/password_reset/reset",
		`{"token":"malformed","newPassword":"synthetic-new-password","confirmPassword":"synthetic-new-password"}`,
		"203.0.113.44")
	if got := decodeRecoveryResponse(t, rec)["message"]; got != "此密码重置链接无效、已过期或已使用。" {
		t.Fatalf("token failure message=%v want the localized safe message", got)
	}
}

func TestDashboardPasswordResetHandlerUsesTheRegisteredSurface(t *testing.T) {
	service := &passwordRecoveryOperationsStub{}
	app := newPasswordRecoveryTestRouter(nil, service)
	rec := recoveryTestPost(app, "/api/auth/password_reset/reset",
		`{"token":"synthetic-token","newPassword":"synthetic-new-password","confirmPassword":"synthetic-new-password","surface":"support"}`,
		"203.0.113.46")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
	}
	if service.resetCalls != 1 || service.resetScope != "dashboard" || service.resetToken != "synthetic-token" {
		t.Fatalf("reset calls=%d route surface=%q token=%q; want one Dashboard call with the submitted token", service.resetCalls, service.resetScope, service.resetToken)
	}
}

func TestSupportPasswordResetRequestUsesTheRegisteredSurface(t *testing.T) {
	service := &passwordRecoveryOperationsStub{}
	app := newPasswordRecoveryTestRouter(NewPasswordRecoveryControls(), service)
	rec := recoveryTestPost(app, "/api/support/auth/password_reset/request",
		`{"identifier":"customer@example.test","surface":"dashboard"}`, "203.0.113.45")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
	}
	if service.requestCalls != 1 || service.requestScope != "support" {
		t.Fatalf("request calls=%d route surface=%q; want one call on support", service.requestCalls, service.requestScope)
	}
}
