package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/i18nx"
	"github.com/Swhshd/kestreldesk/internal/services"
)

// Resolving shared guest:X instead of the middleware's verified ID must fail
// both the foreign-access assertions and B's own-access assertions.
func TestCustomerRESTMessageAuthority(t *testing.T) {
	db, router, channel, agent := newCustomerRecoveryFixture(t)
	if err := db.AutoMigrate(&models.ConversationAssignment{}); err != nil {
		t.Fatal(err)
	}
	a := customerRecoveryExchange(t, router, channel, "X", "A", "", "")
	b := customerRecoveryExchange(t, router, channel, "X", "B", "", "")
	if a.Data == nil || b.Data == nil || a.Data.Customer.ID == b.Data.Customer.ID {
		t.Fatal("distinct guests required")
	}
	convs := []*models.Conversation{
		{CustomerID: a.Data.Customer.ID, ChannelID: channel.ID, AIAgentID: agent.ID, Status: enums.IMConversationStatusAIServing, CustomerUnreadCount: 1},
		{CustomerID: b.Data.Customer.ID, ChannelID: channel.ID, AIAgentID: agent.ID, Status: enums.IMConversationStatusAIServing, CustomerUnreadCount: 1},
	}
	messages := make([]*models.Message, 2)
	for i, conv := range convs {
		if err := db.Create(conv).Error; err != nil {
			t.Fatal(err)
		}
		messages[i] = &models.Message{ConversationID: conv.ID, ClientMsgID: "existing-marker", SenderType: enums.IMSenderTypeAI, MessageType: enums.IMMessageTypeText, Content: fmt.Sprintf("private-marker-%d", i), SendStatus: enums.IMMessageStatusSent}
		if err := db.Create(messages[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	previousHook := services.TriggerAIReplyAsyncHook
	services.TriggerAIReplyAsyncHook = func(models.Conversation, models.Message) {}
	t.Cleanup(func() { services.TriggerAIReplyAsyncHook = previousHook })
	type result struct {
		Success   bool            `json:"success"`
		ErrorCode int             `json:"errorCode"`
		Message   string          `json:"message"`
		Data      json.RawMessage `json:"data"`
	}
	call := func(method, path, body, contentType, token string) result {
		t.Helper()
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		req := httptest.NewRequest(method, path+sep+fmt.Sprintf("customerId=%d", a.Data.Customer.ID), strings.NewReader(body))
		req.Header.Set("X-Channel-ID", channel.ChannelID)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Customer-ID", fmt.Sprint(a.Data.Customer.ID))
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var out result
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	var before models.Conversation
	if err := db.First(&before, convs[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"list", "send", "idempotent send", "read", "upload_image", "upload_attachment", "close"} {
		t.Run("B denied A "+action, func(t *testing.T) {
			path, body, ct, method := "/api/message/"+action, "", "application/json", "POST"
			switch action {
			case "list":
				method = "GET"
				path += fmt.Sprintf("?conversationId=%d", convs[0].ID)
			case "send", "idempotent send":
				path = "/api/message/send"
				clientID := "foreign-new"
				if action == "idempotent send" {
					clientID = "existing-marker"
				}
				body = fmt.Sprintf(`{"conversationId":%d,"customerId":%d,"clientMsgId":%q,"messageType":"text","content":"foreign-content"}`, convs[0].ID, a.Data.Customer.ID, clientID)
			case "read":
				body = fmt.Sprintf(`{"conversationId":%d,"messageId":%d,"customerId":%d}`, convs[0].ID, messages[0].ID, a.Data.Customer.ID)
			case "upload_image", "upload_attachment":
				ct = "application/x-www-form-urlencoded"
				body = fmt.Sprintf("conversationId=%d&customerId=%d", convs[0].ID, a.Data.Customer.ID)
			case "close":
				path = "/api/conversation/close"
				body = fmt.Sprintf(`{"conversationId":%d,"customerId":%d}`, convs[0].ID, a.Data.Customer.ID)
			}
			out := call(method, path, body, ct, b.Data.CustomerSessionToken)
			if out.Success || out.Message != i18nx.Getf(i18nx.DefaultLocale, "error.e0222") {
				t.Errorf("B %s A: success=%v code=%d; want ownership denial before data/file/idempotency access", action, out.Success, out.ErrorCode)
			}
			if strings.Contains(string(out.Data), "private-marker-0") {
				t.Error("A marker leaked to B")
			}
		})
	}
	var after models.Conversation
	if err := db.First(&after, convs[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Error("denied B actions changed A conversation/unread")
	}
	var count int64
	if err := db.Model(&models.Message{}).Where("conversation_id = ?", convs[0].ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("denied B send created A row: %d", count)
	}
	for _, model := range []any{&models.ConversationReadState{}, &models.Asset{}} {
		if err := db.Model(model).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("denied actions created cursor/asset: %d", count)
		}
	}
	if out := call("GET", fmt.Sprintf("/api/message/list?conversationId=%d", convs[1].ID), "", "application/json", b.Data.CustomerSessionToken); !out.Success || !strings.Contains(string(out.Data), "private-marker-1") {
		t.Error("B own list must return B marker")
	}
	body := fmt.Sprintf(`{"conversationId":%d,"customerId":%d,"clientMsgId":"B-send","content":"B-content"}`, convs[1].ID, a.Data.Customer.ID)
	if out := call("POST", "/api/message/send", body, "application/json", b.Data.CustomerSessionToken); !out.Success {
		t.Errorf("B own send denied code=%d", out.ErrorCode)
	}
	read := fmt.Sprintf(`{"conversationId":%d,"messageId":%d,"customerId":%d}`, convs[1].ID, messages[1].ID, a.Data.Customer.ID)
	if out := call("POST", "/api/message/read", read, "application/json", b.Data.CustomerSessionToken); !out.Success {
		t.Errorf("B own read denied code=%d", out.ErrorCode)
	}
	var state models.ConversationReadState
	if err := db.Where("conversation_id = ?", convs[1].ID).First(&state).Error; err != nil || state.LastReadMessageID < messages[1].ID {
		t.Error("B own read did not retain cursor")
	}
}
