package email

// This file has been modified for the KestrelDesk project.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSendSuccessLogsOmitRecipientAndSubject(t *testing.T) {
	const recipient = "privacy-marker@example.test"
	const subject = "password-reset-subject-marker"

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	t.Run("mailgun", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		client := NewClient(ClientConfig{Provider: ProviderMailgun, APIKey: "test-key", Domain: "example.test", BaseURL: server.URL})
		if err := client.SendEmail(context.Background(), SendEmailParams{
			FromEmail: "support@example.test",
			ToEmail:   recipient,
			Subject:   subject,
		}); err != nil {
			t.Fatal("Mailgun send failed")
		}
		assertEmailSuccessLogOmitsPII(t, &logs, recipient, subject)
	})

	t.Run("smtp", func(t *testing.T) {
		logs.Reset()
		addr, _, _ := startSMTPTestServer(t, false, false)
		host, port := splitSMTPTestAddr(t, addr)
		client := NewClient(ClientConfig{
			Provider:     ProviderSMTP,
			SMTPHost:     host,
			SMTPPort:     port,
			SMTPSecurity: "none",
		})
		if err := client.SendEmail(context.Background(), SendEmailParams{
			FromEmail: "support@example.test",
			ToEmail:   recipient,
			Subject:   subject,
		}); err != nil {
			t.Fatal("SMTP send failed")
		}
		assertEmailSuccessLogOmitsPII(t, &logs, recipient, subject)
	})
}

func assertEmailSuccessLogOmitsPII(t *testing.T, logs *bytes.Buffer, recipient, subject string) {
	t.Helper()
	if strings.Contains(logs.String(), recipient) || strings.Contains(logs.String(), subject) {
		t.Fatal("email success log exposed recipient or subject")
	}
}

func TestNewClientInfersProviderFromLegacyAPIKeyOrDefault(t *testing.T) {
	for _, item := range []struct {
		name     string
		apiKey   string
		provider DeliveryProvider
	}{
		{name: "legacy SendGrid key", apiKey: "SG.synthetic-key", provider: ProviderSendGrid},
		{name: "no provider credentials", provider: ProviderSMTP},
	} {
		t.Run(item.name, func(t *testing.T) {
			client := NewClient(ClientConfig{APIKey: item.apiKey}).(*emailClient)
			if client.cfg.Provider != item.provider {
				t.Fatalf("inferred provider = %q, want %q", client.cfg.Provider, item.provider)
			}
		})
	}
}

func TestParseAddress(t *testing.T) {
	tests := []struct {
		input     string
		wantEmail string
		wantName  string
	}{
		{"John Doe <john@example.com>", "john@example.com", "John Doe"},
		{"<support@example.com>", "support@example.com", ""},
		{"plain@example.com", "plain@example.com", ""},
		{"  Alice Smith <ALICE@DOMAIN.COM>  ", "alice@domain.com", "Alice Smith"},
		{"", "", ""},
	}

	for _, tt := range tests {
		gotEmail, gotName := ParseAddress(tt.input)
		if gotEmail != tt.wantEmail || gotName != tt.wantName {
			t.Errorf("ParseAddress(%q) = (%q, %q), want (%q, %q)", tt.input, gotEmail, gotName, tt.wantEmail, tt.wantName)
		}
	}
}

func TestParseInboundWebhook_Formats(t *testing.T) {
	// 1. Generic / Cloudflare format
	genericJSON := []byte(`{
		"from": "user@example.com",
		"from_name": "Test User",
		"to": "help@example.com",
		"subject": "Hello Support",
		"text": "Please help with login.",
		"message_id": "<msg-001@example.com>"
	}`)

	items, err := ParseInboundWebhook("application/json", genericJSON, nil)
	if err != nil {
		t.Fatalf("ParseInboundWebhook generic failed: %v", err)
	}
	if len(items) != 1 || items[0].FromEmail != "user@example.com" || items[0].ToEmail != "help@example.com" {
		t.Errorf("unexpected generic parsed output: %+v", items)
	}

	// 2. Brevo format
	brevoJSON := []byte(`{
		"items": [
			{
				"Sender": "Alice <alice@test.com>",
				"Recipient": "support@example.com",
				"Subject": "Brevo Inquiry",
				"RawTextBody": "Brevo message text"
			}
		]
	}`)
	items, err = ParseInboundWebhook("application/json", brevoJSON, nil)
	if err != nil {
		t.Fatalf("ParseInboundWebhook brevo failed: %v", err)
	}
	if len(items) != 1 || items[0].FromEmail != "alice@test.com" || items[0].FromName != "Alice" {
		t.Errorf("unexpected brevo parsed output: %+v", items)
	}

	// 3. Postmark format
	postmarkJSON := []byte(`{
		"From": "bob@domain.org",
		"FromName": "Bob Developer",
		"To": "help@example.com",
		"Subject": "Postmark Question",
		"TextBody": "Text from Postmark",
		"MessageID": "pm-12345",
		"Headers": [
			{"Name": "In-Reply-To", "Value": "<parent-msg@example.com>"}
		]
	}`)
	items, err = ParseInboundWebhook("application/json", postmarkJSON, nil)
	if err != nil {
		t.Fatalf("ParseInboundWebhook postmark failed: %v", err)
	}
	if len(items) != 1 || items[0].FromEmail != "bob@domain.org" || items[0].InReplyTo != "<parent-msg@example.com>" {
		t.Errorf("unexpected postmark parsed output: %+v", items)
	}

	// 4. Mailgun form data
	mgForm := url.Values{}
	mgForm.Set("sender", "developer@client.com")
	mgForm.Set("from", "Dev <developer@client.com>")
	mgForm.Set("recipient", "help@example.com")
	mgForm.Set("subject", "Mailgun Support")
	mgForm.Set("body-plain", "Plain body from mailgun")
	mgForm.Set("In-Reply-To", "<msg-999@example.com>")

	items, err = ParseInboundWebhook("application/x-www-form-urlencoded", nil, mgForm)
	if err != nil {
		t.Fatalf("ParseInboundWebhook mailgun failed: %v", err)
	}
	if len(items) != 1 || items[0].FromEmail != "developer@client.com" || items[0].InReplyTo != "<msg-999@example.com>" {
		t.Errorf("unexpected mailgun parsed output: %+v", items)
	}
}

func TestSendGridSendEmail(t *testing.T) {
	var receivedAuth string
	var receivedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Provider: ProviderSendGrid,
		APIKey:   "SG.test-key",
		BaseURL:  server.URL,
	})

	err := client.SendEmail(context.Background(), SendEmailParams{
		FromEmail: "support@example.com",
		ToEmail:   "user@example.com",
		Subject:   "SendGrid Test",
		BodyText:  "Hello via SendGrid",
	})

	if err != nil {
		t.Fatalf("SendEmail SendGrid failed: %v", err)
	}
	if receivedAuth != "Bearer SG.test-key" {
		t.Errorf("expected Bearer SG.test-key, got %s", receivedAuth)
	}
	if receivedPath != "/mail/send" {
		t.Errorf("expected /mail/send, got %s", receivedPath)
	}
}

func TestEmailAPI429ReturnsTypedStatusWithoutResponseBody(t *testing.T) {
	const responseBody = "synthetic-provider-error-body"
	for _, tc := range []struct {
		name     string
		provider DeliveryProvider
		config   func(string) ClientConfig
	}{
		{
			name:     "SendGrid",
			provider: ProviderSendGrid,
			config: func(baseURL string) ClientConfig {
				return ClientConfig{Provider: ProviderSendGrid, APIKey: "SG.synthetic-key", BaseURL: baseURL}
			},
		},
		{
			name:     "Mailgun",
			provider: ProviderMailgun,
			config: func(baseURL string) ClientConfig {
				return ClientConfig{Provider: ProviderMailgun, APIKey: "synthetic-key", Domain: "example.test", BaseURL: baseURL}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, responseBody, http.StatusTooManyRequests)
			}))
			defer server.Close()

			client := NewClient(tc.config(server.URL))
			err := client.SendEmail(context.Background(), SendEmailParams{
				FromEmail: "support@example.test",
				ToEmail:   "customer@example.test",
				Subject:   "synthetic rate-limit test",
			})
			if err == nil {
				t.Fatal("SendEmail() error = nil, want HTTP 429")
			}
			var statusErr interface{ HTTPStatusCode() int }
			if !errors.As(err, &statusErr) {
				t.Fatalf("SendEmail() error type = %T, want typed HTTP status error", err)
			}
			if got := statusErr.HTTPStatusCode(); got != http.StatusTooManyRequests {
				t.Fatalf("HTTP status = %d, want 429", got)
			}
			if strings.Contains(err.Error(), responseBody) {
				t.Fatal("email API error included an untrusted response body")
			}
		})
	}
}

func TestResendSendEmail(t *testing.T) {
	var receivedAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"resend-123"}`))
	}))
	defer server.Close()

	client := NewClient(ClientConfig{
		Provider: ProviderResend,
		APIKey:   "re_test_123",
		BaseURL:  server.URL,
	})

	err := client.SendEmail(context.Background(), SendEmailParams{
		FromEmail: "support@example.com",
		ToEmail:   "user@example.com",
		Subject:   "Resend Test",
		BodyText:  "Hello via Resend",
	})

	if err != nil {
		t.Fatalf("SendEmail Resend failed: %v", err)
	}
	if receivedAuth != "Bearer re_test_123" {
		t.Errorf("expected Bearer re_test_123, got %s", receivedAuth)
	}
}

func TestSMTPExplicitNoneDoesNotNegotiateStartTLS(t *testing.T) {
	addr, commands, _ := startSMTPTestServer(t, true, false)
	host, port := splitSMTPTestAddr(t, addr)
	client := NewClient(ClientConfig{
		Provider:     ProviderSMTP,
		SMTPHost:     host,
		SMTPPort:     port,
		SMTPSecurity: "none",
		SMTPTimeout:  500 * time.Millisecond,
	})

	err := client.SendEmail(context.Background(), SendEmailParams{
		FromEmail: "support@example.com",
		ToEmail:   "user@example.com",
		Subject:   "SMTP none",
		BodyText:  "Hello",
	})
	if err != nil {
		t.Fatalf("SendEmail with SMTP security none failed: %v", err)
	}
	for {
		select {
		case command, ok := <-commands:
			if !ok {
				return
			}
			if strings.EqualFold(command, "STARTTLS") {
				t.Fatal("explicit SMTP security none must not negotiate STARTTLS")
			}
			if strings.EqualFold(command, "QUIT") {
				return
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for SMTP commands")
		}
	}
}

func TestSMTPExplicitStartTLSRequiresServerCapability(t *testing.T) {
	addr, _, _ := startSMTPTestServer(t, false, false)
	host, port := splitSMTPTestAddr(t, addr)
	client := NewClient(ClientConfig{
		Provider:     ProviderSMTP,
		SMTPHost:     host,
		SMTPPort:     port,
		SMTPSecurity: "starttls",
		SMTPTimeout:  500 * time.Millisecond,
	})

	err := client.SendEmail(context.Background(), SendEmailParams{
		FromEmail: "support@example.com",
		ToEmail:   "user@example.com",
	})
	if err == nil {
		t.Fatal("explicit STARTTLS mode must fail when the server does not advertise STARTTLS")
	}
}

func TestSMTPExplicitStartTLSUpgradesConnection(t *testing.T) {
	addr, commands, tlsHello := startSMTPTestServer(t, true, false)
	host, port := splitSMTPTestAddr(t, addr)
	client := NewClient(ClientConfig{
		Provider:     ProviderSMTP,
		SMTPHost:     host,
		SMTPPort:     port,
		SMTPSecurity: "starttls",
		SMTPTimeout:  time.Second,
	})

	err := client.SendEmail(context.Background(), SendEmailParams{
		FromEmail: "support@example.com",
		ToEmail:   "user@example.com",
	})
	if err == nil {
		t.Fatal("test server closes during TLS handshake, expected SendEmail to fail")
	}
	if !waitForSMTPCommand(t, commands, "STARTTLS") {
		t.Fatal("client did not issue STARTTLS")
	}
	select {
	case firstByte := <-tlsHello:
		if firstByte != 0x16 {
			t.Fatalf("first byte after STARTTLS = 0x%02x, want TLS handshake record 0x16", firstByte)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not start a TLS handshake after STARTTLS")
	}
}

func TestSMTPExplicitTLSStartsTLSBeforeSMTPGreeting(t *testing.T) {
	addr, firstByte := startRawSMTPTestServer(t)
	host, port := splitSMTPTestAddr(t, addr)
	client := NewClient(ClientConfig{
		Provider:     ProviderSMTP,
		SMTPHost:     host,
		SMTPPort:     port,
		SMTPSecurity: "tls",
		SMTPTimeout:  time.Second,
	})

	err := client.SendEmail(context.Background(), SendEmailParams{
		FromEmail: "support@example.com",
		ToEmail:   "user@example.com",
	})
	if err == nil {
		t.Fatal("test server closes during TLS handshake, expected SendEmail to fail")
	}
	select {
	case firstByte := <-firstByte:
		if firstByte != 0x16 {
			t.Fatalf("first byte on direct TLS connection = 0x%02x, want TLS handshake record 0x16", firstByte)
		}
	case <-time.After(time.Second):
		t.Fatal("client did not start TLS before waiting for an SMTP greeting")
	}
}

func TestSMTPConfiguredTimeoutBoundsConnectionAttempt(t *testing.T) {
	client := NewClient(ClientConfig{
		Provider:     ProviderSMTP,
		SMTPHost:     "smtp.example.com",
		SMTPPort:     2525,
		SMTPSecurity: "none",
		SMTPTimeout:  75 * time.Millisecond,
	}).(*emailClient)
	client.dialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("SMTP dial context must have a deadline")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}

	started := time.Now()
	err := client.SendEmail(context.Background(), SendEmailParams{
		FromEmail: "support@example.com",
		ToEmail:   "user@example.com",
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendEmail error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("connection attempt took %s, want it bounded by the configured timeout", elapsed)
	}
}

func TestSMTPConfiguredTimeoutBoundsServerRead(t *testing.T) {
	addr, _, _ := startSMTPTestServer(t, false, true)
	host, port := splitSMTPTestAddr(t, addr)
	client := NewClient(ClientConfig{
		Provider:     ProviderSMTP,
		SMTPHost:     host,
		SMTPPort:     port,
		SMTPSecurity: "none",
		SMTPTimeout:  75 * time.Millisecond,
	})

	started := time.Now()
	err := client.SendEmail(context.Background(), SendEmailParams{
		FromEmail: "support@example.com",
		ToEmail:   "user@example.com",
	})
	if err == nil {
		t.Fatal("SendEmail must fail when the SMTP greeting exceeds the configured timeout")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("SMTP greeting read took %s, want it bounded by the configured timeout", elapsed)
	}
}

func startSMTPTestServer(t *testing.T, advertiseStartTLS, stallGreeting bool) (string, <-chan string, <-chan byte) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for SMTP test server: %v", err)
	}
	commands := make(chan string, 16)
	tlsHello := make(chan byte, 1)
	t.Cleanup(func() {
		_ = listener.Close()
	})

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			close(commands)
			return
		}
		defer conn.Close()
		defer close(commands)
		if stallGreeting {
			var b [1]byte
			_, _ = conn.Read(b[:])
			return
		}
		_, _ = fmt.Fprint(conn, "220 test SMTP ready\r\n")
		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			command := strings.TrimSpace(line)
			commands <- command
			switch {
			case strings.HasPrefix(strings.ToUpper(command), "EHLO "):
				if advertiseStartTLS {
					_, _ = fmt.Fprint(conn, "250-test SMTP\r\n250-STARTTLS\r\n250 AUTH PLAIN\r\n")
				} else {
					_, _ = fmt.Fprint(conn, "250 test SMTP\r\n")
				}
			case strings.EqualFold(command, "STARTTLS"):
				_, _ = fmt.Fprint(conn, "220 begin TLS\r\n")
				var b [1]byte
				if _, err := conn.Read(b[:]); err == nil {
					tlsHello <- b[0]
				}
				return
			case strings.HasPrefix(strings.ToUpper(command), "MAIL FROM:"):
				_, _ = fmt.Fprint(conn, "250 sender accepted\r\n")
			case strings.HasPrefix(strings.ToUpper(command), "RCPT TO:"):
				_, _ = fmt.Fprint(conn, "250 recipient accepted\r\n")
			case strings.EqualFold(command, "DATA"):
				_, _ = fmt.Fprint(conn, "354 send data\r\n")
				for {
					dataLine, err := reader.ReadString('\n')
					if err != nil || strings.TrimSpace(dataLine) == "." {
						break
					}
				}
				_, _ = fmt.Fprint(conn, "250 queued\r\n")
			case strings.EqualFold(command, "QUIT"):
				_, _ = fmt.Fprint(conn, "221 bye\r\n")
				return
			default:
				_, _ = fmt.Fprint(conn, "250 ok\r\n")
			}
		}
	}()
	return listener.Addr().String(), commands, tlsHello
}

func startRawSMTPTestServer(t *testing.T) (string, <-chan byte) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for direct TLS test server: %v", err)
	}
	firstByte := make(chan byte, 1)
	t.Cleanup(func() {
		_ = listener.Close()
	})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		var b [1]byte
		if _, err := conn.Read(b[:]); err == nil {
			firstByte <- b[0]
		}
	}()
	return listener.Addr().String(), firstByte
}

func splitSMTPTestAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatalf("parse SMTP test port %q: %v", portText, err)
	}
	return host, port
}

func waitForSMTPCommand(t *testing.T, commands <-chan string, want string) bool {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case command, ok := <-commands:
			if !ok {
				return false
			}
			if strings.EqualFold(command, want) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func TestSMTPOmittedSecurityRetainsLegacyOpportunisticStartTLS(t *testing.T) {
	addr, commands, tlsHello := startSMTPTestServer(t, true, false)
	host, port := splitSMTPTestAddr(t, addr)
	client := NewClient(ClientConfig{
		Provider: ProviderSMTP,
		SMTPHost: host,
		SMTPPort: port,
	})

	err := client.SendEmail(context.Background(), SendEmailParams{
		FromEmail: "support@example.com",
		ToEmail:   "user@example.com",
	})
	if err == nil {
		t.Fatal("test server closes during TLS handshake, expected SendEmail to fail")
	}
	if !waitForSMTPCommand(t, commands, "STARTTLS") {
		t.Fatal("omitted SMTP security mode must retain legacy opportunistic STARTTLS")
	}
	select {
	case firstByte := <-tlsHello:
		if firstByte != 0x16 {
			t.Fatalf("first byte after legacy STARTTLS = 0x%02x, want TLS handshake record 0x16", firstByte)
		}
	case <-time.After(time.Second):
		t.Fatal("legacy STARTTLS path did not start a TLS handshake")
	}
}
