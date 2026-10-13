package config

// This file has been modified for the KestrelDesk project.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func isolateBootstrapAdminPasswordEnvironment(t *testing.T) {
	t.Helper()

	for _, name := range []string{"KESTRELDESK_AUTH_BOOTSTRAPADMINPASSWORD", "BOOTSTRAP_ADMIN_PASSWORD"} {
		value, existed := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("Unsetenv(%s) error = %v", name, err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}

	// Keep repository and user dotenv files from adding ambient aliases.
	t.Setenv("KESTRELDESK_ENV_FILE", os.DevNull)
	t.Setenv("ENV_FILE", os.DevNull)
}

func writeBootstrapAdminPasswordTestConfig(t *testing.T, password string, includePassword bool) string {
	t.Helper()

	content := "server:\n  port: 8083\n"
	if includePassword {
		content += fmt.Sprintf("auth:\n  bootstrapAdminPassword: %q\n", password)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func newBootstrapAdminPasswordTestValue(t *testing.T) string {
	t.Helper()

	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		t.Fatalf("generating test input failed: %v", err)
	}
	return "test-bootstrap-" + hex.EncodeToString(random)
}

func TestLoadReadsBootstrapAdminPasswordFromYAML(t *testing.T) {
	isolateBootstrapAdminPasswordEnvironment(t)
	want := newBootstrapAdminPasswordTestValue(t)
	cfg, err := Load(writeBootstrapAdminPasswordTestConfig(t, want, true))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Auth.BootstrapAdminPassword != want {
		t.Fatal("Auth.BootstrapAdminPassword did not match the YAML input")
	}
}

func TestLoadReadsBootstrapAdminPasswordEnvironmentAliases(t *testing.T) {
	for _, alias := range []string{"KESTRELDESK_AUTH_BOOTSTRAPADMINPASSWORD", "BOOTSTRAP_ADMIN_PASSWORD"} {
		t.Run(alias, func(t *testing.T) {
			isolateBootstrapAdminPasswordEnvironment(t)
			want := newBootstrapAdminPasswordTestValue(t)
			t.Setenv(alias, want)
			cfg, err := Load(writeBootstrapAdminPasswordTestConfig(t, "", false))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Auth.BootstrapAdminPassword != want {
				t.Fatal("Auth.BootstrapAdminPassword did not match the environment input")
			}
		})
	}
}

func TestLoadLeavesBootstrapAdminPasswordEmptyWhenUnset(t *testing.T) {
	isolateBootstrapAdminPasswordEnvironment(t)
	cfg, err := Load(writeBootstrapAdminPasswordTestConfig(t, "", false))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Auth.BootstrapAdminPassword != "" {
		t.Fatal("Auth.BootstrapAdminPassword was not empty when unset")
	}
}

func TestLoadBootstrapAdminPasswordEnvironmentOverridesYAML(t *testing.T) {
	isolateBootstrapAdminPasswordEnvironment(t)
	yamlValue := newBootstrapAdminPasswordTestValue(t)
	environmentValue := newBootstrapAdminPasswordTestValue(t)
	t.Setenv("KESTRELDESK_AUTH_BOOTSTRAPADMINPASSWORD", environmentValue)
	cfg, err := Load(writeBootstrapAdminPasswordTestConfig(t, yamlValue, true))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Auth.BootstrapAdminPassword != environmentValue {
		t.Fatal("Auth.BootstrapAdminPassword did not prefer the non-empty environment input")
	}
}

func TestLoadEmptyBootstrapAdminPasswordEnvironmentKeepsYAML(t *testing.T) {
	isolateBootstrapAdminPasswordEnvironment(t)
	want := newBootstrapAdminPasswordTestValue(t)
	t.Setenv("KESTRELDESK_AUTH_BOOTSTRAPADMINPASSWORD", "")
	t.Setenv("BOOTSTRAP_ADMIN_PASSWORD", "")
	cfg, err := Load(writeBootstrapAdminPasswordTestConfig(t, want, true))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Auth.BootstrapAdminPassword != want {
		t.Fatal("empty environment inputs did not preserve the YAML value")
	}
}

func TestLoadReadsCORSAllowedOrigins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`server:
  port: 8083
  cors:
    allowedOrigins:
      - https://console.example.com
      - http://localhost:3000
`)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	got := cfg.Server.CORS.AllowedOrigins
	want := []string{"https://console.example.com", "http://localhost:3000"}
	if len(got) != len(want) {
		t.Fatalf("len(AllowedOrigins)=%d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllowedOrigins[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

func TestLoadOverridesValuesFromEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`server:
  port: 8083
db:
  type: sqlite
  dsn: file:./data/app.db?_busy_timeout=5000
storage:
  local:
    baseUrl: /storage
mcp:
  servers:
    system:
      endpoint: http://127.0.0.1:8083/api/mcp
`)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("KESTRELDESK_SERVER_PORT", "8090")
	t.Setenv("KESTRELDESK_DB_DSN", "mysql-dsn")
	t.Setenv("KESTRELDESK_STORAGE_LOCAL_BASEURL", "/files")
	t.Setenv("KESTRELDESK_MCP_SERVERS_SYSTEM_ENDPOINT", "http://127.0.0.1:8090/api/mcp")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != 8090 {
		t.Fatalf("Server.Port=%d want 8090", cfg.Server.Port)
	}
	if cfg.DB.Type != "sqlite" {
		t.Fatalf("DB.Type=%q want sqlite", cfg.DB.Type)
	}
	if cfg.DB.DSN != "mysql-dsn" {
		t.Fatalf("DB.DSN=%q want mysql-dsn", cfg.DB.DSN)
	}
	if cfg.Storage.Local.BaseURL != "/files" {
		t.Fatalf("Storage.Local.BaseURL=%q want /files", cfg.Storage.Local.BaseURL)
	}
	if cfg.MCP.Servers["system"].Endpoint != "http://127.0.0.1:8090/api/mcp" {
		t.Fatalf("MCP system endpoint=%q", cfg.MCP.Servers["system"].Endpoint)
	}
}

func TestLoadFromDotEnvAndStandardEnvAliases(t *testing.T) {
	tempDir := t.TempDir()
	envPath := filepath.Join(tempDir, ".env")
	envContent := []byte(`PORT=9090
COMPANY_NAME=CustomDesk
COMPANY_LOGO_URL=/custom-logo.svg
DATABASE_URL=postgres://user:pass@localhost:5432/mydb?sslmode=disable
PASSWORD_LOGIN_ENABLED=false
JWT_SECRET=super-secret-key-12345
QDRANT_HOST=10.0.0.5
QDRANT_PORT=6334
OIDC_ENABLED=true
OIDC_ISSUER=https://auth.example.com
OIDC_CLIENT_ID=client-123
OIDC_CLIENT_SECRET=secret-456
OIDC_REDIRECT_URL=https://desk.example.com/api/auth/oidc_callback
ORG_SYNC_SECRET=webhook-secret-789
`)
	if err := os.WriteFile(envPath, envContent, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("ENV_FILE", envPath)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != 9090 {
		t.Fatalf("Server.Port=%d want 9090", cfg.Server.Port)
	}
	if cfg.Server.CompanyName != "CustomDesk" {
		t.Fatalf("Server.CompanyName=%q want CustomDesk", cfg.Server.CompanyName)
	}
	if cfg.Server.CompanyLogoURL != "/custom-logo.svg" {
		t.Fatalf("Server.CompanyLogoURL=%q want /custom-logo.svg", cfg.Server.CompanyLogoURL)
	}
	if cfg.DB.Type != "postgres" {
		t.Fatalf("DB.Type=%q want postgres", cfg.DB.Type)
	}
	if cfg.DB.DSN != "postgres://user:pass@localhost:5432/mydb?sslmode=disable" {
		t.Fatalf("DB.DSN=%q", cfg.DB.DSN)
	}
	if cfg.Auth.IsPasswordLoginEnabled() {
		t.Fatalf("expected PasswordLoginEnabled to be false")
	}
	if cfg.CustomerSession.Secret != "super-secret-key-12345" {
		t.Fatalf("CustomerSession.Secret=%q", cfg.CustomerSession.Secret)
	}
	if cfg.VectorDB.Qdrant.Host != "10.0.0.5" {
		t.Fatalf("Qdrant.Host=%q", cfg.VectorDB.Qdrant.Host)
	}
	if cfg.VectorDB.Qdrant.GrpcPort != 6334 {
		t.Fatalf("Qdrant.GrpcPort=%d", cfg.VectorDB.Qdrant.GrpcPort)
	}
	if !cfg.OIDC.Enabled {
		t.Fatalf("expected OIDC.Enabled=true")
	}
	if cfg.OIDC.Issuer != "https://auth.example.com" {
		t.Fatalf("OIDC.Issuer=%q", cfg.OIDC.Issuer)
	}
	if cfg.OIDC.ClientID != "client-123" {
		t.Fatalf("OIDC.ClientID=%q", cfg.OIDC.ClientID)
	}
	if cfg.OIDC.ClientSecret != "secret-456" {
		t.Fatalf("OIDC.ClientSecret=%q", cfg.OIDC.ClientSecret)
	}
	if cfg.OIDC.RedirectURL != "https://desk.example.com/api/auth/oidc_callback" {
		t.Fatalf("OIDC.RedirectURL=%q", cfg.OIDC.RedirectURL)
	}
	if cfg.Webhook.OrgSyncSecret != "webhook-secret-789" {
		t.Fatalf("Webhook.OrgSyncSecret=%q", cfg.Webhook.OrgSyncSecret)
	}
}

func TestLoadFromKestrelDeskPrefixedEnvironment(t *testing.T) {
	t.Setenv("KESTRELDESK_ENV_FILE", os.DevNull)
	t.Setenv("ENV_FILE", os.DevNull)
	t.Setenv("KESTRELDESK_SERVER_PORT", "8191")

	path := writeBootstrapAdminPasswordTestConfig(t, "", false)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.Port != 8191 {
		t.Fatalf("Server.Port=%d want 8191", cfg.Server.Port)
	}
}

func TestAuthConfigMaxFailedAttemptsPerIPOrDefault(t *testing.T) {
	cases := []struct {
		name string
		cfg  AuthConfig
		want int
	}{
		{"explicit value wins", AuthConfig{MaxFailedAttempts: 5, MaxFailedAttemptsPerIP: 30}, 30},
		{"unset derives four times the per-account limit", AuthConfig{MaxFailedAttempts: 5}, 20},
		{"disabled per-account limit disables both", AuthConfig{MaxFailedAttempts: 0}, 0},
		{"negative per-account limit disables both", AuthConfig{MaxFailedAttempts: -1}, 0},
	}
	for _, tc := range cases {
		if got := tc.cfg.MaxFailedAttemptsPerIPOrDefault(); got != tc.want {
			t.Errorf("%s: MaxFailedAttemptsPerIPOrDefault() = %d want %d", tc.name, got, tc.want)
		}
	}
}

func TestServerConfigTrustedProxiesOrDefault(t *testing.T) {
	// An unset list must not fall through to Gin's own default of 0.0.0.0/0 and
	// ::/0, which trusts every peer and makes X-Forwarded-For authoritative.
	got := ServerConfig{}.TrustedProxiesOrDefault()
	for _, want := range []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "::1/128", "fc00::/7", "fe80::/10"} {
		found := false
		for _, entry := range got {
			if entry == want {
				found = true
			}
		}
		if !found {
			t.Errorf("TrustedProxiesOrDefault() = %v, missing %s", got, want)
		}
	}
	for _, entry := range got {
		if entry == "0.0.0.0/0" || entry == "::/0" {
			t.Errorf("TrustedProxiesOrDefault() includes %s, which trusts every peer", entry)
		}
	}

	// Blank entries from a comma-separated environment variable must not become
	// empty CIDRs, which Gin would reject at startup.
	got = ServerConfig{TrustedProxies: []string{" 10.1.0.0/16 ", "", "  "}}.TrustedProxiesOrDefault()
	if len(got) != 1 || got[0] != "10.1.0.0/16" {
		t.Errorf("TrustedProxiesOrDefault() = %v want [10.1.0.0/16]", got)
	}
}

func TestServerConfigTrustedPlatformHeader(t *testing.T) {
	cases := []struct {
		platform string
		want     string
	}{
		{"", ""},
		{"cloudflare", "CF-Connecting-IP"},
		{"Cloudflare", "CF-Connecting-IP"},
		{"  CF  ", "CF-Connecting-IP"},
		{"fly.io", "Fly-Client-IP"},
		{"google-app-engine", "X-Appengine-Remote-Addr"},
		// Anything unrecognised is a literal header name, which is what Gin's
		// TrustedPlatform field expects.
		{"X-CDN-IP", "X-CDN-IP"},
	}
	for _, tc := range cases {
		if got := (ServerConfig{TrustedPlatform: tc.platform}).TrustedPlatformHeader(); got != tc.want {
			t.Errorf("TrustedPlatformHeader(%q) = %q want %q", tc.platform, got, tc.want)
		}
	}
}

// TestLoadReadsTrustedProxySettings covers the environment spelling, including
// the comma-separated list. Gin rejects an unparseable CIDR at startup, so a
// value that arrives as one string instead of a slice would take the process
// down rather than degrade quietly.
func TestLoadReadsTrustedProxySettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 8083\n"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("ENV_FILE", os.DevNull)
	t.Setenv("KESTRELDESK_ENV_FILE", os.DevNull)
	t.Setenv("TRUSTED_PROXIES", "10.0.0.0/8,172.16.0.0/12")
	t.Setenv("TRUSTED_PLATFORM", "cloudflare")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := []string{"10.0.0.0/8", "172.16.0.0/12"}
	got := cfg.Server.TrustedProxies
	if len(got) != len(want) {
		t.Fatalf("TrustedProxies = %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("TrustedProxies[%d] = %q want %q", i, got[i], want[i])
		}
	}
	if cfg.Server.TrustedPlatformHeader() != "CF-Connecting-IP" {
		t.Errorf("TrustedPlatformHeader() = %q want CF-Connecting-IP", cfg.Server.TrustedPlatformHeader())
	}
}

func TestRateLimitConfigDefaultsToEnabledWithAMinuteWindow(t *testing.T) {
	var cfg RateLimitConfig
	if !cfg.IsEnabled() {
		t.Error("rate limiting must default to enabled; a zero-value config should not silently turn protection off")
	}
	if got := cfg.WindowSecondsOrDefault(); got != 60 {
		t.Errorf("WindowSecondsOrDefault() = %d want 60", got)
	}

	off := false
	cfg = RateLimitConfig{Enabled: &off, WindowSeconds: -5}
	if cfg.IsEnabled() {
		t.Error("Enabled=false must disable the limits")
	}
	if got := cfg.WindowSecondsOrDefault(); got != 60 {
		t.Errorf("a non-positive window must fall back to 60, got %d", got)
	}

	on := true
	cfg = RateLimitConfig{Enabled: &on, WindowSeconds: 30}
	if !cfg.IsEnabled() {
		t.Error("Enabled=true must enable the limits")
	}
	if got := cfg.WindowSecondsOrDefault(); got != 30 {
		t.Errorf("WindowSecondsOrDefault() = %d want 30", got)
	}
}

func TestLoadReadsRateLimitSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: 8083\n"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("ENV_FILE", os.DevNull)
	t.Setenv("KESTRELDESK_ENV_FILE", os.DevNull)
	t.Setenv("RATE_LIMIT_ENABLED", "false")
	t.Setenv("RATE_LIMIT_WINDOW_SECONDS", "30")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.RateLimit.IsEnabled() {
		t.Error("RATE_LIMIT_ENABLED=false did not disable the limits")
	}
	if got := cfg.Server.RateLimit.WindowSecondsOrDefault(); got != 30 {
		t.Errorf("WindowSecondsOrDefault() = %d want 30", got)
	}
}

func TestEmailRecoveryConfigDefaultsPreserveLegacySMTPSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("email:\n  provider: smtp\n"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("ENV_FILE", os.DevNull)
	t.Setenv("KESTRELDESK_ENV_FILE", os.DevNull)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Email.Enabled {
		t.Fatal("password recovery mail must remain disabled by default")
	}
	if cfg.Email.PasswordResetBaseURL != "" {
		t.Fatalf("PasswordResetBaseURL = %q, want empty unless explicitly trusted", cfg.Email.PasswordResetBaseURL)
	}
	if cfg.Email.PasswordResetTTLMinutes != 30 {
		t.Fatalf("PasswordResetTTLMinutes = %d, want 30", cfg.Email.PasswordResetTTLMinutes)
	}
	if cfg.Email.SMTPSecurity != "" {
		t.Fatalf("SMTPSecurity = %q, want empty to preserve legacy SMTP mode selection", cfg.Email.SMTPSecurity)
	}
	if cfg.Email.SMTPPort != 587 || cfg.Email.SMTPUseTLS {
		t.Fatalf("legacy SMTP defaults changed: port=%d useTLS=%t", cfg.Email.SMTPPort, cfg.Email.SMTPUseTLS)
	}
	if cfg.Email.SMTPTimeoutSeconds != 20 || cfg.Email.SMTPTimeout() != 20*time.Second {
		t.Fatalf("SMTP timeout default = %d seconds / %s, want 20 seconds", cfg.Email.SMTPTimeoutSeconds, cfg.Email.SMTPTimeout())
	}
}

func TestLoadReadsEmailRecoveryAndSMTPSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`email:
  enabled: false
  passwordResetBaseUrl: https://mail.example.com
  passwordResetTTLMinutes: 25
  smtpSecurity: starttls
  smtpTimeoutSeconds: 12
`)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("ENV_FILE", os.DevNull)
	t.Setenv("KESTRELDESK_ENV_FILE", os.DevNull)
	t.Setenv("EMAIL_ENABLED", "true")
	t.Setenv("EMAIL_PASSWORD_RESET_BASE_URL", "https://trusted.example.com")
	t.Setenv("EMAIL_PASSWORD_RESET_TTL_MINUTES", "45")
	t.Setenv("SMTP_SECURITY", "tls")
	t.Setenv("SMTP_TIMEOUT_SECONDS", "8")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Email.Enabled {
		t.Fatal("EMAIL_ENABLED=true did not enable email recovery")
	}
	if cfg.Email.PasswordResetBaseURL != "https://trusted.example.com" {
		t.Fatalf("PasswordResetBaseURL = %q", cfg.Email.PasswordResetBaseURL)
	}
	if cfg.Email.PasswordResetTTLMinutes != 45 {
		t.Fatalf("PasswordResetTTLMinutes = %d, want 45", cfg.Email.PasswordResetTTLMinutes)
	}
	if cfg.Email.SMTPSecurity != "tls" {
		t.Fatalf("SMTPSecurity = %q, want tls", cfg.Email.SMTPSecurity)
	}
	if cfg.Email.SMTPTimeoutSeconds != 8 || cfg.Email.SMTPTimeout() != 8*time.Second {
		t.Fatalf("SMTP timeout = %d seconds / %s, want 8 seconds", cfg.Email.SMTPTimeoutSeconds, cfg.Email.SMTPTimeout())
	}
}

func TestEmailSMTPTimeoutIsStrictlyBounded(t *testing.T) {
	cfg := EmailConfig{SMTPTimeoutSeconds: 3600}
	if got, want := cfg.SMTPTimeout(), 30*time.Second; got != want {
		t.Fatalf("SMTPTimeout() = %s, want maximum %s", got, want)
	}
}
