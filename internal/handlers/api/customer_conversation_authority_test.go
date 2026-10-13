package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/middleware"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"

	"github.com/gin-gonic/gin"
)

// Dropping verified B and resolving guest:X again must never reopen or authorize A.
func TestCustomerRESTConversationAuthority(t *testing.T) {
	db, router, channel, agent := newCustomerRecoveryFixture(t)
	if err := db.AutoMigrate(&models.ConversationAssignment{}); err != nil {
		t.Fatal(err)
	}
	a := customerRecoveryExchange(t, router, channel, "X", "A", "", "")
	b := customerRecoveryExchange(t, router, channel, "X", "B", "", "")
	if a.Data == nil || b.Data == nil || a.Data.Customer.ID == b.Data.Customer.ID {
		t.Fatal("distinct guest fixture required")
	}
	convA := &models.Conversation{CustomerID: a.Data.Customer.ID, CustomerName: "A", ChannelID: channel.ID, AIAgentID: agent.ID, Status: enums.IMConversationStatusAIServing}
	if err := db.Create(convA).Error; err != nil {
		t.Fatal(err)
	}
	type result struct {
		Success   bool `json:"success"`
		ErrorCode int  `json:"errorCode"`
		Data      struct {
			ID         int64 `json:"id"`
			CustomerID int64 `json:"customerId"`
		} `json:"data"`
	}
	call := func(method, path, body, token string) result {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Channel-ID", channel.ChannelID)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Customer-ID", fmt.Sprint(a.Data.Customer.ID))
		req.Header.Set("customerId", fmt.Sprint(a.Data.Customer.ID))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var got result
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal("decode conversation response")
		}
		return got
	}
	injected := fmt.Sprintf("?customerId=%d", a.Data.Customer.ID)
	body := fmt.Sprintf(`{"customerId":%d}`, a.Data.Customer.ID)
	first := call("POST", "/api/conversation/create_or_match"+injected, body, b.Data.CustomerSessionToken)
	if !first.Success || first.Data.CustomerID != b.Data.Customer.ID || first.Data.ID == convA.ID {
		t.Errorf("B create_or_match owner=%d conversation=%d error=%d; want B=%d and distinct from A=%d", first.Data.CustomerID, first.Data.ID, first.ErrorCode, b.Data.Customer.ID, convA.ID)
	}
	again := call("POST", "/api/conversation/create_or_match"+injected, body, b.Data.CustomerSessionToken)
	if !again.Success || again.Data.ID != first.Data.ID || again.Data.CustomerID != b.Data.Customer.ID {
		t.Error("B active conversation not reused")
	}
	if own := call("POST", "/api/conversation/create_or_match", "{}", a.Data.CustomerSessionToken); !own.Success || own.Data.ID != convA.ID {
		t.Error("A cannot reuse its active conversation")
	}
	path := fmt.Sprintf("/api/conversation/%d%s", convA.ID, injected)
	if denied := call("GET", path, "", b.Data.CustomerSessionToken); denied.Success {
		t.Error("B detail authorized A by shared hint")
	}
	if allowed := call("GET", path, "", a.Data.CustomerSessionToken); !allowed.Success || allowed.Data.ID != convA.ID {
		t.Error("A detail denied")
	}
	closeBody := fmt.Sprintf(`{"conversationId":%d,"customerId":%d}`, convA.ID, a.Data.Customer.ID)
	if denied := call("POST", "/api/conversation/close"+injected, closeBody, b.Data.CustomerSessionToken); denied.Success {
		t.Error("B close authorized A by shared hint")
	}
	var unchanged models.Conversation
	if err := db.First(&unchanged, convA.ID).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != enums.IMConversationStatusAIServing {
		t.Error("denied B close mutated A")
	}
	if allowed := call("POST", "/api/conversation/close", closeBody, a.Data.CustomerSessionToken); !allowed.Success {
		t.Error("A close denied")
	}
	if err := db.First(&unchanged, convA.ID).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != enums.IMConversationStatusClosed {
		t.Error("A close did not close its conversation")
	}
	if own := call("GET", fmt.Sprintf("/api/conversation/%d", first.Data.ID), "", b.Data.CustomerSessionToken); !own.Success || own.Data.CustomerID != b.Data.Customer.ID {
		t.Error("B detail denied its own conversation")
	}
}

// Handlers must fail closed if a future route omits either authenticated context value.
func TestCustomerRESTConversationAuthorityMissingContext(t *testing.T) {
	db, router, channel, _ := newCustomerRecoveryFixture(t)
	if err := db.AutoMigrate(&models.ConversationAssignment{}); err != nil {
		t.Fatal(err)
	}
	conv := &models.Conversation{CustomerID: 41, Status: enums.IMConversationStatusAIServing}
	if err := db.Create(conv).Error; err != nil {
		t.Fatal(err)
	}
	external := &openidentity.ExternalUser{ExternalSource: enums.ExternalSourceGuest, ExternalID: "X"}
	for _, contextKind := range []string{"missing ID", "missing external", "missing both"} {
		for _, action := range []string{"create", "detail", "close"} {
			path := "/context/" + strings.ReplaceAll(contextKind, " ", "_") + "/" + action
			router.POST(path, func(ctx *gin.Context) {
				if contextKind == "missing ID" {
					ctx.Set("externalUser", external)
				}
				if contextKind == "missing external" {
					ctx.Set("verifiedCustomerID", int64(41))
				}
				switch action {
				case "create":
					ConversationPostCreate_or_match(ctx)
				case "detail":
					ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(conv.ID)}}
					ConversationGetBy(ctx)
				case "close":
					ConversationPostClose(ctx)
				}
			})
			req := httptest.NewRequest("POST", path+"?customerId=41", strings.NewReader(fmt.Sprintf(`{"conversationId":%d,"customerId":41}`, conv.ID)))
			req.Header.Set("X-Channel-ID", channel.ChannelID)
			req.Header.Set("X-Customer-ID", "41")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			var result struct {
				Success bool `json:"success"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Success {
				t.Errorf("%s accepted with %s", action, contextKind)
			}
		}
	}
	var after models.Conversation
	if err := db.First(&after, conv.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != enums.IMConversationStatusAIServing {
		t.Error("missing authentication context closed conversation")
	}
}

func TestCustomerMiddlewareRetainsVerifiedID(t *testing.T) {
	_, router, channel, _ := newCustomerRecoveryFixture(t)
	a := customerRecoveryExchange(t, router, channel, "X", "A", "", "")
	b := customerRecoveryExchange(t, router, channel, "X", "B", "", "")
	if a.Data == nil || b.Data == nil {
		t.Fatal("exchange fixture failed")
	}
	calls := 0
	router.GET("/probe", middleware.ExternalUserMiddleware, func(ctx *gin.Context) {
		calls++
		raw, exists := ctx.Get("verifiedCustomerID")
		id, valid := raw.(int64)
		if !exists || !valid || id != b.Data.Customer.ID {
			t.Errorf("trusted middleware ID=%v exists=%v; want B=%d", raw, exists, b.Data.Customer.ID)
		}
	})
	for _, token := range []string{b.Data.CustomerSessionToken, "malformed", ""} {
		req := httptest.NewRequest("GET", fmt.Sprintf("/probe?customerId=%d", a.Data.Customer.ID), nil)
		req.Header.Set("X-Channel-ID", channel.ChannelID)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Customer-ID", fmt.Sprint(a.Data.Customer.ID))
		router.ServeHTTP(httptest.NewRecorder(), req)
	}
	if calls != 1 {
		t.Errorf("downstream calls=%d, want only successful verification", calls)
	}
}
