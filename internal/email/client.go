package email

// This file has been modified for the KestrelDesk project.

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBrevoBaseURL    = "https://api.brevo.com/v3"
	defaultSendGridBaseURL = "https://api.sendgrid.com/v3"
	defaultResendBaseURL   = "https://api.resend.com"
	defaultPostmarkBaseURL = "https://api.postmarkapp.com"
	defaultMailgunBaseURL  = "https://api.mailgun.net/v3"
	defaultTimeout         = 20 * time.Second
	maxSMTPTimeout         = 30 * time.Second
)

// Client interface for sending emails across multiple providers.
type Client interface {
	SendEmail(ctx context.Context, req SendEmailParams) error
}

// HTTPStatusError reports an explicit non-2xx response from an email provider.
// It intentionally omits the response body because providers may echo message data.
type HTTPStatusError struct {
	Provider   DeliveryProvider
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return "email provider returned an HTTP error"
	}
	if e.Provider == "" {
		return fmt.Sprintf("email API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("%s email API returned HTTP %d", e.Provider, e.StatusCode)
}

func (e *HTTPStatusError) HTTPStatusCode() int {
	if e == nil {
		return 0
	}
	return e.StatusCode
}

type emailClient struct {
	cfg         ClientConfig
	httpClient  *http.Client
	dialContext func(context.Context, string, string) (net.Conn, error)
}

// NewClient creates a new unified Email client.
func NewClient(cfg ClientConfig) Client {
	provider := DeliveryProvider(strings.ToLower(strings.TrimSpace(string(cfg.Provider))))
	if provider == "" {
		if cfg.APIKey != "" {
			if strings.HasPrefix(cfg.APIKey, "xkeysib-") {
				provider = ProviderBrevo
			} else if strings.HasPrefix(cfg.APIKey, "SG.") {
				provider = ProviderSendGrid
			} else if strings.HasPrefix(cfg.APIKey, "re_") {
				provider = ProviderResend
			} else {
				provider = ProviderBrevo
			}
		} else {
			provider = ProviderSMTP
		}
	}
	cfg.Provider = provider
	if cfg.SMTPPort <= 0 {
		cfg.SMTPPort = 587
	}
	cfg.SMTPSecurity = strings.ToLower(strings.TrimSpace(cfg.SMTPSecurity))
	cfg.SMTPTimeout = boundedSMTPTimeout(cfg.SMTPTimeout)
	dialer := &net.Dialer{}
	return &emailClient{
		cfg:         cfg,
		httpClient:  &http.Client{Timeout: defaultTimeout},
		dialContext: dialer.DialContext,
	}
}

func boundedSMTPTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultTimeout
	}
	if timeout > maxSMTPTimeout {
		return maxSMTPTimeout
	}
	return timeout
}

func (c *emailClient) SendEmail(ctx context.Context, req SendEmailParams) error {
	req.FromEmail = strings.TrimSpace(req.FromEmail)
	req.ToEmail = strings.TrimSpace(req.ToEmail)
	if req.FromEmail == "" || req.ToEmail == "" {
		return fmt.Errorf("fromEmail and toEmail are required")
	}
	if req.Subject == "" {
		req.Subject = "Support Notification"
	}

	switch c.cfg.Provider {
	case ProviderBrevo:
		return c.sendViaBrevo(ctx, req)
	case ProviderSendGrid:
		return c.sendViaSendGrid(ctx, req)
	case ProviderResend:
		return c.sendViaResend(ctx, req)
	case ProviderPostmark:
		return c.sendViaPostmark(ctx, req)
	case ProviderMailgun:
		return c.sendViaMailgun(ctx, req)
	default:
		return c.sendViaSMTP(ctx, req)
	}
}

// 1. Brevo v3 API
func (c *emailClient) sendViaBrevo(ctx context.Context, req SendEmailParams) error {
	baseURL := strings.TrimRight(c.cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultBrevoBaseURL
	}
	apiURL := fmt.Sprintf("%s/smtp/email", baseURL)

	senderName := req.FromName
	if senderName == "" {
		senderName = "Customer Support"
	}

	payload := BrevoSendEmailRequest{
		Sender: BrevoEmailContact{
			Name:  senderName,
			Email: req.FromEmail,
		},
		To: []BrevoEmailContact{
			{
				Name:  req.ToName,
				Email: req.ToEmail,
			},
		},
		Subject:     req.Subject,
		TextContent: req.BodyText,
		HTMLContent: req.BodyHTML,
		Headers:     req.Headers,
	}
	if req.ReplyTo != "" {
		payload.ReplyTo = &BrevoEmailContact{Email: req.ReplyTo}
	}
	if payload.HTMLContent == "" && payload.TextContent != "" {
		payload.HTMLContent = formatHTMLParagraphs(payload.TextContent)
	}

	return c.postJSON(ctx, apiURL, payload, map[string]string{
		"api-key": c.cfg.APIKey,
	})
}

// 2. SendGrid v3 API
func (c *emailClient) sendViaSendGrid(ctx context.Context, req SendEmailParams) error {
	baseURL := strings.TrimRight(c.cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultSendGridBaseURL
	}
	apiURL := fmt.Sprintf("%s/mail/send", baseURL)

	payload := SendGridSendEmailRequest{
		Personalizations: []SendGridPersonalization{
			{
				To: []SendGridContact{{Email: req.ToEmail, Name: req.ToName}},
			},
		},
		From:    SendGridContact{Email: req.FromEmail, Name: req.FromName},
		Subject: req.Subject,
		Headers: req.Headers,
	}
	if req.ReplyTo != "" {
		payload.ReplyTo = &SendGridContact{Email: req.ReplyTo}
	}
	if req.BodyText != "" {
		payload.Content = append(payload.Content, SendGridContent{Type: "text/plain", Value: req.BodyText})
	}
	if req.BodyHTML != "" {
		payload.Content = append(payload.Content, SendGridContent{Type: "text/html", Value: req.BodyHTML})
	} else if req.BodyText != "" {
		payload.Content = append(payload.Content, SendGridContent{Type: "text/html", Value: formatHTMLParagraphs(req.BodyText)})
	}

	return c.postJSON(ctx, apiURL, payload, map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s", c.cfg.APIKey),
	})
}

// 3. Resend API
func (c *emailClient) sendViaResend(ctx context.Context, req SendEmailParams) error {
	baseURL := strings.TrimRight(c.cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultResendBaseURL
	}
	apiURL := fmt.Sprintf("%s/emails", baseURL)

	fromHeader := req.FromEmail
	if req.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", req.FromName, req.FromEmail)
	}

	payload := ResendSendEmailRequest{
		From:    fromHeader,
		To:      []string{req.ToEmail},
		ReplyTo: req.ReplyTo,
		Subject: req.Subject,
		Text:    req.BodyText,
		HTML:    req.BodyHTML,
		Headers: req.Headers,
	}
	if payload.HTML == "" && payload.Text != "" {
		payload.HTML = formatHTMLParagraphs(payload.Text)
	}

	return c.postJSON(ctx, apiURL, payload, map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s", c.cfg.APIKey),
	})
}

// 4. Postmark API
func (c *emailClient) sendViaPostmark(ctx context.Context, req SendEmailParams) error {
	baseURL := strings.TrimRight(c.cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultPostmarkBaseURL
	}
	apiURL := fmt.Sprintf("%s/email", baseURL)

	fromHeader := req.FromEmail
	if req.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", req.FromName, req.FromEmail)
	}

	var headers []PostmarkHeader
	for k, v := range req.Headers {
		headers = append(headers, PostmarkHeader{Name: k, Value: v})
	}

	payload := PostmarkSendEmailRequest{
		From:     fromHeader,
		To:       req.ToEmail,
		ReplyTo:  req.ReplyTo,
		Subject:  req.Subject,
		TextBody: req.BodyText,
		HtmlBody: req.BodyHTML,
		Headers:  headers,
	}
	if payload.HtmlBody == "" && payload.TextBody != "" {
		payload.HtmlBody = formatHTMLParagraphs(payload.TextBody)
	}

	return c.postJSON(ctx, apiURL, payload, map[string]string{
		"X-Postmark-Server-Token": c.cfg.APIKey,
	})
}

// 5. Mailgun Messages API
func (c *emailClient) sendViaMailgun(ctx context.Context, req SendEmailParams) error {
	baseURL := strings.TrimRight(c.cfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = defaultMailgunBaseURL
	}
	domain := c.cfg.Domain
	if domain == "" {
		parts := strings.Split(req.FromEmail, "@")
		if len(parts) == 2 {
			domain = parts[1]
		}
	}
	apiURL := fmt.Sprintf("%s/%s/messages", baseURL, domain)

	fromHeader := req.FromEmail
	if req.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", req.FromName, req.FromEmail)
	}

	form := url.Values{}
	form.Set("from", fromHeader)
	form.Set("to", req.ToEmail)
	form.Set("subject", req.Subject)
	if req.BodyText != "" {
		form.Set("text", req.BodyText)
	}
	if req.BodyHTML != "" {
		form.Set("html", req.BodyHTML)
	} else if req.BodyText != "" {
		form.Set("html", formatHTMLParagraphs(req.BodyText))
	}
	if req.ReplyTo != "" {
		form.Set("h:Reply-To", req.ReplyTo)
	}
	if req.InReplyTo != "" {
		form.Set("h:In-Reply-To", req.InReplyTo)
	}
	if req.References != "" {
		form.Set("h:References", req.References)
	}
	for k, v := range req.Headers {
		form.Set(fmt.Sprintf("h:%s", k), v)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create mailgun request failed: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.SetBasicAuth("api", c.cfg.APIKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("mailgun request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPStatusError{Provider: c.cfg.Provider, StatusCode: resp.StatusCode}
	}
	slog.Info("email sent via mailgun")
	return nil
}

// 6. Standard SMTP (AWS SES, Postmark SMTP, SendGrid SMTP, Brevo SMTP, custom Postfix)
func (c *emailClient) sendViaSMTP(ctx context.Context, req SendEmailParams) error {
	smtpHost := strings.TrimSpace(c.cfg.SMTPHost)
	if smtpHost == "" {
		return fmt.Errorf("smtp host is not configured")
	}

	securityMode := c.cfg.SMTPSecurity
	if securityMode == "" && (c.cfg.SMTPPort == 465 || c.cfg.SMTPUseTLS) {
		// Preserve the legacy direct-TLS behavior when SMTP_SECURITY is omitted.
		securityMode = "tls"
	}
	switch securityMode {
	case "", "none", "starttls", "tls":
	default:
		return fmt.Errorf("unsupported SMTP security mode %q", securityMode)
	}

	addr := net.JoinHostPort(smtpHost, strconv.Itoa(c.cfg.SMTPPort))
	timeout := boundedSMTPTimeout(c.cfg.SMTPTimeout)
	smtpCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dialContext := c.dialContext
	if dialContext == nil {
		dialContext = (&net.Dialer{}).DialContext
	}
	conn, err := dialContext(smtpCtx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to dial smtp: %w", err)
	}
	defer conn.Close()

	deadline, ok := smtpCtx.Deadline()
	if !ok {
		deadline = time.Now().Add(timeout)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("failed to set smtp deadline: %w", err)
	}
	stopCancelDeadline := context.AfterFunc(smtpCtx, func() {
		_ = conn.SetDeadline(time.Now())
	})
	defer stopCancelDeadline()

	fromHeader := req.FromEmail
	if req.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", req.FromName, req.FromEmail)
	}

	header := make(map[string]string)
	header["From"] = fromHeader
	header["To"] = req.ToEmail
	header["Subject"] = req.Subject
	header["MIME-Version"] = "1.0"
	if req.ReplyTo != "" {
		header["Reply-To"] = req.ReplyTo
	}
	if req.MessageID != "" {
		header["Message-ID"] = req.MessageID
	}
	if req.InReplyTo != "" {
		header["In-Reply-To"] = req.InReplyTo
	}
	if req.References != "" {
		header["References"] = req.References
	}
	for k, v := range req.Headers {
		header[k] = v
	}

	contentType := "text/plain; charset=UTF-8"
	body := req.BodyText
	if req.BodyHTML != "" {
		contentType = "text/html; charset=UTF-8"
		body = req.BodyHTML
	}
	header["Content-Type"] = contentType

	var msg bytes.Buffer
	for k, v := range header {
		msg.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
	}
	msg.WriteString("\r\n")
	msg.WriteString(body)

	var auth smtp.Auth
	if c.cfg.SMTPUser != "" && c.cfg.SMTPPassword != "" {
		auth = smtp.PlainAuth("", c.cfg.SMTPUser, c.cfg.SMTPPassword, smtpHost)
	}

	tlsConfig := &tls.Config{
		ServerName: smtpHost,
	}
	var tlsEstablished bool
	if securityMode == "tls" {
		tlsConn := tls.Client(conn, tlsConfig)
		if err := tlsConn.HandshakeContext(smtpCtx); err != nil {
			return fmt.Errorf("failed to connect via tls: %w", err)
		}
		conn = tlsConn
		tlsEstablished = true
	}

	client, err := smtp.NewClient(conn, smtpHost)
	if err != nil {
		return fmt.Errorf("failed to create smtp client: %w", err)
	}
	defer client.Quit()

	if securityMode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("smtp server does not support required STARTTLS")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("failed to starttls: %w", err)
		}
		tlsEstablished = true
	} else if securityMode == "" {
		// An omitted mode retains legacy opportunistic STARTTLS behavior.
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err = client.StartTLS(tlsConfig); err != nil {
				return fmt.Errorf("failed to starttls: %w", err)
			}
			tlsEstablished = true
		}
	}

	if auth != nil {
		if ok, _ := client.Extension("AUTH"); ok {
			// net/smtp PlainAuth refuses to send credentials over an unencrypted
			// connection; fail early with an actionable message instead.
			if !tlsEstablished && !isLocalhostSMTPHost(smtpHost) {
				return fmt.Errorf("refusing SMTP auth over an unencrypted connection: enable STARTTLS on the server, set SMTP_USE_TLS=true, or use port 465")
			}
			if err = client.Auth(auth); err != nil {
				var responseErr *textproto.Error
				if errors.As(err, &responseErr) {
					return fmt.Errorf("smtp auth failed: %w", &textproto.Error{
						Code: responseErr.Code,
						Msg:  "authentication rejected",
					})
				}
				return fmt.Errorf("smtp auth failed")
			}
		}
	}

	if err = client.Mail(req.FromEmail); err != nil {
		return fmt.Errorf("smtp mail from failed: %w", err)
	}
	if err = client.Rcpt(req.ToEmail); err != nil {
		return fmt.Errorf("smtp rcpt to failed: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data command failed: %w", err)
	}
	_, err = w.Write(msg.Bytes())
	if err != nil {
		return fmt.Errorf("failed to write email body: %w", err)
	}
	err = w.Close()
	if err != nil {
		return fmt.Errorf("failed to close email writer: %w", err)
	}

	slog.Info("email sent via smtp")
	return nil
}

func (c *emailClient) postJSON(ctx context.Context, apiURL string, payload any, headers map[string]string) error {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal json payload: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("email api request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPStatusError{Provider: c.cfg.Provider, StatusCode: resp.StatusCode}
	}
	return nil
}

func formatHTMLParagraphs(text string) string {
	escaped := strings.ReplaceAll(text, "\n", "<br/>")
	return fmt.Sprintf("<div style=\"font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif; font-size: 14px; line-height: 1.6; color: #333;\">%s</div>", escaped)
}

// ParseAddress parses a raw email address string like "Support Team <help@example.com>" into email and display name.
func ParseAddress(raw string) (emailStr string, nameStr string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	parsed, err := mail.ParseAddress(raw)
	if err == nil && parsed != nil {
		return strings.ToLower(strings.TrimSpace(parsed.Address)), strings.TrimSpace(parsed.Name)
	}
	return strings.ToLower(raw), ""
}

func isLocalhostSMTPHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h, "]") {
		h = h[:i]
	}
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}
