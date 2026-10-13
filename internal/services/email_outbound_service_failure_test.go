package services

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/email"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestEmailOutboxClassifiesSMTPFailures(t *testing.T) {
	tests := []struct {
		name           string
		rcptResponse   string
		wantStatus     string
		wantRetryCount int
		wantRetryAt    bool
	}{
		{
			name:           "451 is retried",
			rcptResponse:   "451 4.3.0 synthetic temporary failure",
			wantStatus:     string(enums.ChannelMessageOutboxStatusPending),
			wantRetryCount: 1,
			wantRetryAt:    true,
		},
		{
			name:           "550 is terminal",
			rcptResponse:   "550 5.1.1 synthetic permanent failure",
			wantStatus:     string(enums.ChannelMessageOutboxStatusFailed),
			wantRetryCount: 0,
			wantRetryAt:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			smtp := startOutboxTestSMTPServer(t, tt.rcptResponse, false)
			db, outbox := newEmailOutboxFailureFixture(t, smtp.Addr())
			pending := ChannelMessageOutboxService.ListPending(string(enums.ChannelTypeEmail), 10)
			if len(pending) != 1 || pending[0].ID != outbox.ID {
				t.Fatalf("ListPending() = %#v, want the initial pending outbox row", pending)
			}

			service := newEmailOutboundService()
			if processed := service.doDispatchPendingOutbox(10); processed != 1 {
				t.Fatalf("doDispatchPendingOutbox() = %d, want 1 processed row", processed)
			}

			var got models.ChannelMessageOutbox
			if err := db.First(&got, outbox.ID).Error; err != nil {
				t.Fatalf("reload outbox: %v", err)
			}
			if got.SendStatus != tt.wantStatus {
				t.Errorf("send_status = %q, want %q (last_error=%q)", got.SendStatus, tt.wantStatus, got.LastError)
			}
			if got.RetryCount != tt.wantRetryCount {
				t.Errorf("retry_count = %d, want %d", got.RetryCount, tt.wantRetryCount)
			}
			if (got.NextRetryAt != nil) != tt.wantRetryAt {
				t.Errorf("next_retry_at = %v, want present=%v", got.NextRetryAt, tt.wantRetryAt)
			}
			if got.NextRetryAt != nil && !got.NextRetryAt.After(time.Now()) {
				t.Errorf("next_retry_at = %v, want a future retry deadline", *got.NextRetryAt)
			}
			if pending := ChannelMessageOutboxService.ListPending(string(enums.ChannelTypeEmail), 10); len(pending) != 0 {
				t.Errorf("ListPending() after SMTP failure = %#v, want no immediately eligible retry", pending)
			}
			if processed := service.doDispatchPendingOutbox(10); processed != 0 {
				t.Fatalf("doDispatchPendingOutbox() before retry deadline = %d, want 0", processed)
			}
			if connections := smtp.connections.Load(); connections != 1 {
				t.Errorf("SMTP connections after second dispatch = %d, want 1", connections)
			}
			if tt.wantRetryAt {
				pastDeadline := time.Now().Add(-time.Second)
				if err := db.Model(&models.ChannelMessageOutbox{}).Where("id = ?", outbox.ID).Update("next_retry_at", &pastDeadline).Error; err != nil {
					t.Fatalf("make synthetic retry deadline due: %v", err)
				}
				if pending := ChannelMessageOutboxService.ListPending(string(enums.ChannelTypeEmail), 10); len(pending) != 1 {
					t.Fatalf("ListPending() after retry deadline elapsed = %#v, want one row", pending)
				}
				if processed := service.doDispatchPendingOutbox(10); processed != 1 {
					t.Fatalf("doDispatchPendingOutbox() after retry deadline elapsed = %d, want 1", processed)
				}
				if connections := smtp.connections.Load(); connections != 2 {
					t.Errorf("SMTP connections after eligible retry = %d, want 2", connections)
				}
				if err := db.First(&got, outbox.ID).Error; err != nil {
					t.Fatalf("reload outbox after eligible retry: %v", err)
				}
				if got.RetryCount != 2 || got.SendStatus != string(enums.ChannelMessageOutboxStatusPending) {
					t.Errorf("outbox after eligible retry = status %q, retry_count %d; want pending and 2", got.SendStatus, got.RetryCount)
				}
			}
		})
	}
}

func TestEmailOutboxRetriesOnlyExplicitAPIRateLimit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statusCode int
		wantStatus string
		wantRetry  int
		wantNext   bool
	}{
		{name: "429 explicit rate limit", statusCode: http.StatusTooManyRequests, wantStatus: string(enums.ChannelMessageOutboxStatusPending), wantRetry: 1, wantNext: true},
		{name: "500 terminal provider failure", statusCode: http.StatusInternalServerError, wantStatus: string(enums.ChannelMessageOutboxStatusFailed)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, outbox := newEmailOutboxFailureFixture(t, "127.0.0.1:1")
			claimed, err := ChannelMessageOutboxService.ClaimForSending(outbox, time.Now())
			if err != nil || !claimed {
				t.Fatalf("ClaimForSending() = (%v, %v), want a successful claim", claimed, err)
			}

			service := newEmailOutboundService()
			if err := service.handleOutboxSendError(outbox, &email.HTTPStatusError{Provider: email.ProviderSendGrid, StatusCode: tc.statusCode}); err != nil {
				t.Fatalf("handleOutboxSendError() error = %v", err)
			}

			var got models.ChannelMessageOutbox
			if err := db.First(&got, outbox.ID).Error; err != nil {
				t.Fatalf("reload outbox: %v", err)
			}
			if got.SendStatus != tc.wantStatus || got.RetryCount != tc.wantRetry || (got.NextRetryAt != nil) != tc.wantNext {
				t.Fatalf("outbox state = (status %q, retry_count %d, next_retry_at %v), want (%q, %d, present=%v)", got.SendStatus, got.RetryCount, got.NextRetryAt, tc.wantStatus, tc.wantRetry, tc.wantNext)
			}
			if tc.wantNext && !got.NextRetryAt.After(time.Now()) {
				t.Fatalf("next_retry_at = %v, want a future retry deadline", *got.NextRetryAt)
			}
		})
	}
}

func TestEmailOutboxConcurrentDispatchClaimsOnce(t *testing.T) {
	for i := range 10 {
		t.Run(fmt.Sprintf("iteration-%02d", i+1), func(t *testing.T) {
			smtp := startOutboxTestSMTPServer(t, "", true)
			db, outbox := newEmailOutboxFailureFixture(t, smtp.Addr())
			service := newEmailOutboundService()

			firstDone := make(chan error, 1)
			go func() { firstDone <- service.processOutbox(outbox.ID) }()
			select {
			case <-smtp.dataStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("first dispatcher did not reach SMTP DATA")
			}

			secondDone := make(chan error, 1)
			go func() { secondDone <- service.processOutbox(outbox.ID) }()
			select {
			case <-smtp.dataStarted:
				smtp.releaseData()
				if err := <-firstDone; err != nil {
					t.Errorf("first processOutbox() error = %v", err)
				}
				if err := <-secondDone; err != nil {
					t.Errorf("second processOutbox() error = %v", err)
				}
				t.Fatal("both dispatchers reached SMTP DATA for the same outbox row")
			case err := <-secondDone:
				if err != nil {
					smtp.releaseData()
					<-firstDone
					t.Fatalf("second processOutbox() error = %v", err)
				}
			case <-time.After(5 * time.Second):
				smtp.releaseData()
				<-firstDone
				t.Fatal("second dispatcher neither claimed nor skipped the outbox row")
			}

			smtp.releaseData()
			if err := <-firstDone; err != nil {
				t.Fatalf("first processOutbox() error = %v", err)
			}

			var got models.ChannelMessageOutbox
			if err := db.First(&got, outbox.ID).Error; err != nil {
				t.Fatalf("reload outbox: %v", err)
			}
			if got.SendStatus != string(enums.ChannelMessageOutboxStatusSent) {
				t.Fatalf("send_status = %q, want sent", got.SendStatus)
			}
			if connections := smtp.connections.Load(); connections != 1 {
				t.Fatalf("SMTP connections = %d, want exactly 1", connections)
			}
		})
	}
}

type outboxTestSMTPServer struct {
	listener    net.Listener
	rcptReply   string
	holdData    bool
	dataStarted chan struct{}
	release     chan struct{}
	connections atomic.Int32
}

func startOutboxTestSMTPServer(t *testing.T, rcptReply string, holdData bool) *outboxTestSMTPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for synthetic SMTP server: %v", err)
	}
	server := &outboxTestSMTPServer{
		listener:    listener,
		rcptReply:   rcptReply,
		holdData:    holdData,
		dataStarted: make(chan struct{}, 4),
		release:     make(chan struct{}),
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			server.connections.Add(1)
			go server.serve(conn)
		}
	}()
	return server
}

func (s *outboxTestSMTPServer) Addr() string { return s.listener.Addr().String() }

func (s *outboxTestSMTPServer) releaseData() {
	select {
	case <-s.release:
	default:
		close(s.release)
	}
}

func (s *outboxTestSMTPServer) serve(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	write := func(line string) bool {
		if _, err := fmt.Fprint(writer, line+"\r\n"); err != nil {
			return false
		}
		return writer.Flush() == nil
	}
	if !write("220 synthetic SMTP") {
		return
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(command, "EHLO "), strings.HasPrefix(command, "HELO "):
			if !write("250-localhost") || !write("250 HELP") {
				return
			}
		case strings.HasPrefix(command, "MAIL FROM:"):
			if !write("250 2.1.0 sender accepted") {
				return
			}
		case strings.HasPrefix(command, "RCPT TO:"):
			if s.rcptReply != "" {
				if !write(s.rcptReply) {
					return
				}
			} else if !write("250 2.1.5 recipient accepted") {
				return
			}
		case command == "DATA":
			if !write("354 end with dot") {
				return
			}
			for {
				dataLine, readErr := reader.ReadString('\n')
				if readErr != nil {
					return
				}
				if strings.TrimSpace(dataLine) == "." {
					break
				}
			}
			s.dataStarted <- struct{}{}
			if s.holdData {
				<-s.release
			}
			if !write("250 2.0.0 queued") {
				return
			}
		case command == "QUIT":
			_ = write("221 2.0.0 bye")
			return
		default:
			if !write("250 2.0.0 ok") {
				return
			}
		}
	}
}

func newEmailOutboxFailureFixture(t *testing.T, smtpAddr string) (*gorm.DB, *models.ChannelMessageOutbox) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{TablePrefix: "t_", SingularTable: true},
	})
	if err != nil {
		t.Fatalf("open synthetic outbox database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get synthetic outbox SQL database: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close synthetic outbox SQL database: %v", err)
		}
	})
	if err := db.AutoMigrate(&models.ChannelMessageOutbox{}, &models.Message{}, &models.Conversation{}, &models.Channel{}, &models.Customer{}); err != nil {
		t.Fatalf("migrate synthetic outbox database: %v", err)
	}
	previousDB := sqls.DB()
	sqls.SetDB(db)
	t.Cleanup(func() { sqls.SetDB(previousDB) })

	channel := models.Channel{
		ChannelType: string(enums.ChannelTypeEmail),
		Status:      enums.StatusOk,
		ConfigJSON:  fmt.Sprintf(`{"provider":"smtp","emailAddress":"support@example.test","smtpHost":"127.0.0.1","smtpPort":%d}`, portFromAddr(t, smtpAddr)),
	}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create synthetic email channel: %v", err)
	}
	customer := models.Customer{Name: "Synthetic customer", PrimaryEmail: "customer@example.test", Status: enums.StatusOk}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatalf("create synthetic customer: %v", err)
	}
	conversation := models.Conversation{ChannelID: channel.ID, CustomerID: customer.ID}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("create synthetic conversation: %v", err)
	}
	message := models.Message{ConversationID: conversation.ID, Content: "synthetic outbox message"}
	if err := db.Create(&message).Error; err != nil {
		t.Fatalf("create synthetic message: %v", err)
	}
	outbox := &models.ChannelMessageOutbox{
		ChannelType:    string(enums.ChannelTypeEmail),
		ConversationID: conversation.ID,
		MessageID:      message.ID,
		SendStatus:     string(enums.ChannelMessageOutboxStatusPending),
	}
	if err := db.Create(outbox).Error; err != nil {
		t.Fatalf("create synthetic outbox row: %v", err)
	}
	return db, outbox
}

func portFromAddr(t *testing.T, addr string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("parse synthetic SMTP address: %v", err)
	}
	var portNumber int
	if _, err := fmt.Sscanf(port, "%d", &portNumber); err != nil {
		t.Fatalf("parse synthetic SMTP port: %v", err)
	}
	return portNumber
}
