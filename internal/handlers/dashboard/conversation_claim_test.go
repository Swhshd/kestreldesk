package dashboard

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/gin-gonic/gin"
)

func TestConversationHTTPClaim(t *testing.T) {
	for _, test := range []struct {
		name, body          string
		permission, allowed bool
	}{
		{"self", `{"conversationId":4}`, true, true},
		{"trailing-whitespace", "{\"conversationId\":4} \r\n\t ", true, true},
		{"trailing-json-value", `{"conversationId":4} {"assigneeId":102}`, true, false},
		{"missing-permission", `{"conversationId":4}`, false, false},
		{"injected-assignee", `{"conversationId":4,"assigneeId":102}`, true, false},
		{"injected-team", `{"conversationId":4,"teamId":20}`, true, false},
		{"injected-employee", `{"conversationId":4,"employeeId":102}`, true, false},
		{"unknown-target", `{"conversationId":4,"target":102}`, true, false},
		{"invalid", `{"conversationId":0}`, true, false},
		{"wrong-team", `{"conversationId":5}`, true, false},
		{"ai-serving", `{"conversationId":6}`, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, p := conversationHTTPFixture(t)
			if err := db.AutoMigrate(&models.ConversationAssignment{}, &models.ConversationEventLog{}); err != nil {
				t.Fatal(err)
			}
			if test.permission {
				p.Permissions = append(p.Permissions, "conversation.claim")
			}
			router := gin.New()
			router.Use(func(ctx *gin.Context) { ctx.Set("authPrincipal", p) })
			router.POST("/claim", ConversationPostClaim)
			req := httptest.NewRequest("POST", "/claim", strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			var result struct {
				Success bool `json:"success"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Success != test.allowed {
				t.Fatalf("allowed=%v response=%s", test.allowed, recorder.Body.String())
			}
			var count int64
			db.Model(&models.ConversationAssignment{}).Count(&count)
			if test.allowed && count != 1 || !test.allowed && count != 0 {
				t.Fatalf("audit count=%d", count)
			}
		})
	}
}
