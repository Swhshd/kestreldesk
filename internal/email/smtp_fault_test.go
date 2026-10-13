package email

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"
)

type syntheticSMTPServer struct {
	listener net.Listener
	address  string
	body     chan string
	accepted chan struct{}
	done     chan struct{}
}

func startSyntheticSMTPServer(t *testing.T, finalStatus int, blackholeGreeting bool, authFailureResponse ...string) *syntheticSMTPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for synthetic SMTP server: %v", err)
	}
	server := &syntheticSMTPServer{
		listener: listener,
		address:  listener.Addr().String(),
		body:     make(chan string, 1),
		accepted: make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-server.done:
		case <-time.After(time.Second):
			t.Error("synthetic SMTP server did not stop")
		}
	})

	go func() {
		defer close(server.done)
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		server.accepted <- struct{}{}
		defer conn.Close()
		if blackholeGreeting {
			_, _ = io.Copy(io.Discard, conn)
			return
		}

		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)
		writeLine := func(line string) bool {
			if _, writeErr := fmt.Fprint(writer, line+"\r\n"); writeErr != nil {
				return false
			}
			return writer.Flush() == nil
		}
		if !writeLine("220 localhost ESMTP synthetic-test") {
			return
		}
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				return
			}
			command := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(command, "EHLO "):
				if !writeLine("250-localhost") {
					return
				}
				if len(authFailureResponse) > 0 {
					if !writeLine("250-AUTH PLAIN") {
						return
					}
				}
				if !writeLine("250 PIPELINING") {
					return
				}
			case strings.HasPrefix(command, "HELO "):
				if !writeLine("250 localhost") {
					return
				}
			case strings.HasPrefix(command, "AUTH PLAIN") && len(authFailureResponse) > 0:
				if !writeLine(authFailureResponse[0]) {
					return
				}
			case strings.HasPrefix(command, "MAIL FROM:"):
				if !writeLine("250 sender accepted") {
					return
				}
			case strings.HasPrefix(command, "RCPT TO:"):
				if !writeLine("250 recipient accepted") {
					return
				}
			case command == "DATA":
				if !writeLine("354 send message") {
					return
				}
				var body strings.Builder
				for {
					messageLine, messageErr := reader.ReadString('\n')
					if messageErr != nil {
						return
					}
					if messageLine == ".\r\n" {
						break
					}
					body.WriteString(messageLine)
				}
				server.body <- body.String()
				if !writeLine(fmt.Sprintf("%d synthetic delivery failure", finalStatus)) {
					return
				}
			case command == "QUIT":
				_ = writeLine("221 closing connection")
				return
			case command == "RSET" || command == "NOOP":
				if !writeLine("250 ok") {
					return
				}
			default:
				if !writeLine("250 ok") {
					return
				}
			}
		}
	}()
	return server
}

func syntheticSMTPClient(server *syntheticSMTPServer, timeout time.Duration, credentials ...string) Client {
	host, portText, _ := net.SplitHostPort(server.address)
	port, _ := strconv.Atoi(portText)
	config := ClientConfig{
		Provider:     ProviderSMTP,
		SMTPHost:     host,
		SMTPPort:     port,
		SMTPSecurity: "none",
		SMTPTimeout:  timeout,
	}
	if len(credentials) >= 2 {
		config.SMTPUser = credentials[0]
		config.SMTPPassword = credentials[1]
	}
	return NewClient(config)
}

func TestSendEmailReturnsSyntheticSMTP451And550(t *testing.T) {
	for _, status := range []int{451, 550} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := startSyntheticSMTPServer(t, status, false)
			client := syntheticSMTPClient(server, time.Second)
			err := client.SendEmail(context.Background(), SendEmailParams{
				FromEmail: "no-reply@example.test",
				ToEmail:   "synthetic@example.test",
				Subject:   "synthetic SMTP fault test",
				BodyText:  "synthetic body",
			})
			if err == nil {
				t.Fatal("SendEmail() error = nil, want the synthetic SMTP failure")
			}
			var responseErr *textproto.Error
			if !errors.As(err, &responseErr) {
				t.Fatalf("SendEmail() error does not preserve SMTP response code: %T", err)
			}
			if responseErr.Code != status {
				t.Fatalf("SMTP response code = %d, want %d", responseErr.Code, status)
			}
			select {
			case body := <-server.body:
				if !strings.Contains(body, "synthetic body") {
					t.Fatalf("SMTP server received unexpected body: %q", body)
				}
			case <-time.After(time.Second):
				t.Fatal("SMTP server did not receive the test message")
			}
			select {
			case <-server.done:
			case <-time.After(time.Second):
				t.Fatal("SMTP server did not finish after the failure response")
			}
		})
	}
}

func TestSendEmailDoesNotExposeSMTPAuthFailureTextOrCredentials(t *testing.T) {
	const (
		smtpUser     = "synthetic-smtp-user"
		smtpPassword = "synthetic-smtp-password-sentinel"
	)
	serverResponse := "535 5.7.8 rejected user=" + smtpUser + " password=" + smtpPassword
	server := startSyntheticSMTPServer(t, 0, false, serverResponse)
	client := syntheticSMTPClient(server, time.Second, smtpUser, smtpPassword)

	err := client.SendEmail(context.Background(), SendEmailParams{
		FromEmail: "no-reply@example.test",
		ToEmail:   "synthetic@example.test",
		Subject:   "synthetic SMTP auth fault test",
		BodyText:  "synthetic body",
	})
	if err == nil {
		t.Fatal("SendEmail() error = nil, want the synthetic SMTP authentication failure")
	}
	if strings.Contains(err.Error(), smtpUser) || strings.Contains(err.Error(), smtpPassword) {
		t.Fatal("SMTP authentication error exposed configured credentials")
	}
	if strings.Contains(err.Error(), "rejected user=") {
		t.Fatal("SMTP authentication error exposed server-controlled response text")
	}
	var responseErr *textproto.Error
	if !errors.As(err, &responseErr) || responseErr.Code != 535 {
		t.Fatal("SMTP authentication error did not preserve the 535 response status")
	}
}

func TestSendEmailSMTPTimeoutClosesBlackholeConnection(t *testing.T) {
	server := startSyntheticSMTPServer(t, 0, true)
	client := syntheticSMTPClient(server, 500*time.Millisecond)
	startedAt := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- client.SendEmail(context.Background(), SendEmailParams{
			FromEmail: "no-reply@example.test",
			ToEmail:   "synthetic@example.test",
			Subject:   "synthetic SMTP timeout test",
			BodyText:  "synthetic body",
		})
	}()
	select {
	case <-server.accepted:
	case err := <-done:
		t.Fatalf("SMTP send failed before the blackhole accepted the connection: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("blackhole SMTP server did not accept the connection")
	}
	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SMTP send did not finish within the configured timeout")
	}
	if err == nil {
		t.Fatal("SendEmail() error = nil, want configured SMTP timeout")
	}
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
		t.Fatalf("SMTP timeout took %s, want it bounded by the configured timeout", elapsed)
	}
	select {
	case <-server.done:
	case <-time.After(time.Second):
		t.Fatal("SMTP connection remained open after the configured timeout")
	}
}
