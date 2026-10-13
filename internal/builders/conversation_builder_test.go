package builders

// This file has been modified for the KestrelDesk project.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/i18nx"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"github.com/Swhshd/kestreldesk/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/gorilla/websocket"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Presence via the shared external ID must fail the A-online/B-offline result.
func TestConversationBuilderCustomerOnlineUsesConversationCustomerID(t *testing.T) {
	previousDB, previousConfig := sqls.DB(), config.GetCurrent()
	t.Cleanup(func() {
		sqls.SetDB(previousDB)
		config.SetCurrent(previousConfig)
	})
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "t_", SingularTable: true}})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&models.Channel{}, &models.Customer{}, &models.CustomerIdentity{}, &models.Conversation{}, &models.ConversationReadState{}); err != nil {
		t.Fatal(err)
	}
	sqls.SetDB(db)
	signingKey := make([]byte, 32)
	if _, err := rand.Read(signingKey); err != nil {
		t.Fatal("generate in-memory signing key")
	}
	config.SetCurrent(&config.Config{CustomerSession: config.CustomerSessionConfig{Secret: base64.RawURLEncoding.EncodeToString(signingKey)}})
	channel := &models.Channel{ID: 701, ChannelID: "synthetic-builder-channel", Status: enums.StatusOk}
	customerA := &models.Customer{ID: 741, Name: "synthetic-builder-A", Status: enums.StatusOk}
	customerB := &models.Customer{ID: 742, Name: "synthetic-builder-B", Status: enums.StatusOk}
	externalID := "synthetic-builder-collision"
	externalA := openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: externalID}
	conversationA := &models.Conversation{ID: 751, CustomerID: customerA.ID, ChannelID: channel.ID}
	conversationB := &models.Conversation{ID: 752, CustomerID: customerB.ID, ChannelID: channel.ID}
	for _, row := range []any{
		channel, customerA, customerB, conversationA, conversationB,
		&models.CustomerIdentity{CustomerID: customerA.ID, ExternalSource: enums.ExternalSourceGuest, ExternalID: externalID, Status: enums.StatusOk},
		&models.CustomerIdentity{CustomerID: customerB.ID, ExternalSource: enums.ExternalSourceUser, ExternalID: externalID, Status: enums.StatusOk},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	token, _, err := services.CustomerSessionService.Sign(channel, customerA, externalA)
	if err != nil {
		t.Fatal("sign synthetic builder session")
	}
	router := gin.New()
	router.GET("/api/ws/open", services.WsService.HandleOpenWS)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	headers := http.Header{"Authorization": {"Bearer " + token}, "X-Channel-ID": {channel.ChannelID}}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/ws/open", headers)
	if err != nil {
		t.Fatal("synthetic builder WebSocket handshake failed")
	}
	t.Cleanup(func() {
		_ = conn.Close()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if !services.WsService.IsCustomerOnline(customerA.ID) {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Error("synthetic builder socket remained online after cleanup")
	})
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var connected struct {
		Type string `json:"type"`
		Data struct {
			Topics []string `json:"topics"`
		} `json:"data"`
	}
	if err := conn.ReadJSON(&connected); err != nil {
		t.Fatal("read synthetic builder connected frame")
	}
	if connected.Type != enums.IMRealtimeEventConnected || len(connected.Data.Topics) != 1 || connected.Data.Topics[0] != "customer:741" {
		t.Fatal("builder fixture did not authenticate Customer A on only its customer topic")
	}
	var identityOrChannelQueries atomic.Int64
	if err := db.Callback().Query().Before("gorm:query").Register("test:builder_presence_lookup", func(tx *gorm.DB) {
		if tx.Statement.Table == "t_customer_identity" || tx.Statement.Table == "t_channel" {
			identityOrChannelQueries.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !BuildConversationWithLocale(conversationA, i18nx.DefaultLocale).CustomerOnline {
		t.Error("A conversation must report its authenticated customer online")
	}
	if BuildConversationWithLocale(conversationB, i18nx.DefaultLocale).CustomerOnline {
		t.Error("B conversation must remain offline despite A's matching external ID")
	}
	if got := identityOrChannelQueries.Load(); got != 0 {
		t.Errorf("builder presence queried customer identities/channels %d times", got)
	}
}

func TestLocalizeConversationSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		locale  string
		summary string
		want    string
	}{
		{
			name:    "image summary in english",
			locale:  i18nx.LocaleEnUS,
			summary: "[图片]",
			want:    "[Image]",
		},
		{
			name:    "attachment summary in english",
			locale:  i18nx.LocaleEnUS,
			summary: "[附件] spec.pdf",
			want:    "[Attachment] spec.pdf",
		},
		{
			name:    "recalled message in english",
			locale:  i18nx.LocaleEnUS,
			summary: "该消息已撤回",
			want:    "This message was recalled.",
		},
		{
			name:    "business text is not translated",
			locale:  i18nx.LocaleEnUS,
			summary: "客户反馈无法登录",
			want:    "客户反馈无法登录",
		},
		{
			name:    "chinese locale keeps existing summary",
			locale:  i18nx.LocaleZhCN,
			summary: "[图片]",
			want:    "[图片]",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := localizeConversationSummary(tt.locale, tt.summary); got != tt.want {
				t.Fatalf("localizeConversationSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLocalizeRenderableMessageContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		locale  string
		content string
		want    string
	}{
		{
			name:    "recalled message in english",
			locale:  i18nx.LocaleEnUS,
			content: "该消息已撤回",
			want:    "This message was recalled.",
		},
		{
			name:    "normal customer message is not translated",
			locale:  i18nx.LocaleEnUS,
			content: "客户反馈无法登录",
			want:    "客户反馈无法登录",
		},
		{
			name:    "chinese locale keeps content",
			locale:  i18nx.LocaleZhCN,
			content: "该消息已撤回",
			want:    "该消息已撤回",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := localizeRenderableMessageContent(tt.locale, tt.content); got != tt.want {
				t.Fatalf("localizeRenderableMessageContent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildMessageIncludesWorkflowRunID(t *testing.T) {
	resp := BuildMessageWithReadStatesAndLocale(&models.Message{
		ID:             1,
		ConversationID: 2,
		SenderType:     enums.IMSenderTypeAI,
		MessageType:    enums.IMMessageTypeText,
		Content:        "AI reply",
		WorkflowRunID:  9988,
	}, nil, nil, nil, nil, nil, i18nx.DefaultLocale)

	if resp.WorkflowRunID != 9988 {
		t.Fatalf("resp.WorkflowRunID=%d want 9988", resp.WorkflowRunID)
	}
}

func TestBuildMessageJSONDoesNotExposeSeqNo(t *testing.T) {
	resp := BuildMessageWithReadStatesAndLocale(&models.Message{
		ID:             1,
		ConversationID: 2,
		SenderType:     enums.IMSenderTypeCustomer,
		MessageType:    enums.IMMessageTypeText,
		Content:        "hello",
	}, nil, nil, nil, nil, nil, i18nx.DefaultLocale)

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal message response: %v", err)
	}
	if strings.Contains(string(raw), "seqNo") {
		t.Fatalf("message response should not expose seqNo, got %s", raw)
	}
}
