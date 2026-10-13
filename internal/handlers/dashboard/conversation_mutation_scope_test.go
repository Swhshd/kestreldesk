package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/services"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func conversationMutationFixture(t *testing.T) (*gorm.DB, *dto.AuthPrincipal) {
	t.Helper()
	db, principal := conversationHTTPFixture(t)
	if err := db.AutoMigrate(&models.ConversationAssignment{}, &models.ConversationEventLog{}, &models.ConversationTag{}, &models.Tag{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Tag{ID: 1, Name: "synthetic-enabled", Status: enums.StatusOk}).Error; err != nil {
		t.Fatal(err)
	}
	return db, principal
}

func conversationMutationHTTPRequest(t *testing.T, principal *dto.AuthPrincipal, path, contentType, body string) conversationHTTPResult {
	t.Helper()
	router := gin.New()
	router.Use(func(ctx *gin.Context) { ctx.Set("authPrincipal", principal) })
	router.POST("/send", ConversationPostSend_message)
	router.POST("/recall", ConversationPostRecall_message)
	router.POST("/close", ConversationPostClose)
	router.POST("/upload_image", ConversationPostUpload_image)
	router.POST("/upload_attachment", ConversationPostUpload_attachment)
	router.POST("/add_tag", ConversationPostAdd_tag)
	router.POST("/remove_tag", ConversationPostRemove_tag)
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	var result conversationHTTPResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

func TestConversationHTTPMutationScopeRejectsDisabledProfileWithoutWrites(t *testing.T) {
	tests := []struct {
		name, path, contentType, body, permission string
	}{
		{"send", "/send", "application/json", `{"conversationId":1,"clientMsgId":"synthetic-disabled-profile","messageType":"text","content":"synthetic message"}`, constants.PermissionConversationSend.Code},
		{"image upload", "/upload_image", "application/x-www-form-urlencoded", url.Values{"conversationId": {"1"}}.Encode(), constants.PermissionConversationSend.Code},
		{"attachment upload", "/upload_attachment", "application/x-www-form-urlencoded", url.Values{"conversationId": {"1"}}.Encode(), constants.PermissionConversationSend.Code},
		{"close", "/close", "application/json", `{"conversationId":1,"closeReason":"synthetic reason"}`, constants.PermissionConversationClose.Code},
		{"add tag", "/add_tag", "application/json", `{"conversationId":1,"tagId":1}`, constants.PermissionConversationTag.Code},
		{"remove tag", "/remove_tag", "application/json", `{"conversationId":1,"tagId":1}`, constants.PermissionConversationTag.Code},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, principal := conversationMutationFixture(t)
			principal.Permissions = append(principal.Permissions, test.permission)
			if err := db.Model(&models.AgentProfile{}).Where("user_id = ?", principal.UserID).Update("status", enums.StatusDisabled).Error; err != nil {
				t.Fatal(err)
			}
			if test.name == "remove tag" {
				if err := db.Create(&models.ConversationTag{ConversationID: 1, TagID: 1}).Error; err != nil {
					t.Fatal(err)
				}
			}
			var beforeConversation models.Conversation
			if err := db.First(&beforeConversation, 1).Error; err != nil {
				t.Fatal(err)
			}
			var beforeMessages, beforeEvents, beforeTags int64
			db.Model(&models.Message{}).Where("conversation_id = ?", 1).Count(&beforeMessages)
			db.Model(&models.ConversationEventLog{}).Where("conversation_id = ?", 1).Count(&beforeEvents)
			db.Model(&models.ConversationTag{}).Where("conversation_id = ?", 1).Count(&beforeTags)

			result := conversationMutationHTTPRequest(t, principal, test.path, test.contentType, test.body)
			if result.Code != errorsx.CodeAuthForbidden {
				t.Fatalf("disabled profile mutation response code=%d, want forbidden", result.Code)
			}
			var afterConversation models.Conversation
			if err := db.First(&afterConversation, 1).Error; err != nil {
				t.Fatal(err)
			}
			if afterConversation.Status != beforeConversation.Status || afterConversation.CloseReason != beforeConversation.CloseReason {
				t.Fatalf("denied mutation changed conversation: before=%+v after=%+v", beforeConversation, afterConversation)
			}
			var afterMessages, afterEvents, afterTags int64
			db.Model(&models.Message{}).Where("conversation_id = ?", 1).Count(&afterMessages)
			db.Model(&models.ConversationEventLog{}).Where("conversation_id = ?", 1).Count(&afterEvents)
			db.Model(&models.ConversationTag{}).Where("conversation_id = ?", 1).Count(&afterTags)
			if afterMessages != beforeMessages || afterEvents != beforeEvents || afterTags != beforeTags {
				t.Fatalf("denied mutation wrote data: messages %d->%d events %d->%d tags %d->%d", beforeMessages, afterMessages, beforeEvents, afterEvents, beforeTags, afterTags)
			}
		})
	}
}

func TestCsUserTagMutationsRequireOwnedActiveConversation(t *testing.T) {
	for _, id := range []int64{2, 3, 4, 5, 6, 7, 8, 9} {
		t.Run(fmt.Sprintf("conversation-%d", id), func(t *testing.T) {
			db, principal := conversationMutationFixture(t)
			principal.Permissions = append(principal.Permissions, constants.PermissionConversationTag.Code)
			before := int64(0)
			db.Model(&models.ConversationTag{}).Where("conversation_id = ?", id).Count(&before)
			result := conversationMutationHTTPRequest(t, principal, "/add_tag", "application/json", fmt.Sprintf(`{"conversationId":%d,"tagId":1}`, id))
			if result.Code != errorsx.CodeAuthForbidden {
				t.Fatalf("out-of-scope tag mutation response code=%d, want forbidden", result.Code)
			}
			after := int64(0)
			db.Model(&models.ConversationTag{}).Where("conversation_id = ?", id).Count(&after)
			if after != before {
				t.Fatalf("denied tag mutation changed relation count %d -> %d", before, after)
			}
		})
	}
}

func TestCsUserCloseRequiresOwnedActiveConversation(t *testing.T) {
	t.Run("own active conversation", func(t *testing.T) {
		db, principal := conversationMutationFixture(t)
		principal.Permissions = append(principal.Permissions, constants.PermissionConversationClose.Code)
		result := conversationMutationHTTPRequest(t, principal, "/close", "application/json", `{"conversationId":1,"closeReason":"  synthetic resolution  "}`)
		if result.Code != 0 {
			t.Fatalf("cs_user could not close its active conversation: code=%d", result.Code)
		}
		var after models.Conversation
		if err := db.First(&after, 1).Error; err != nil {
			t.Fatal(err)
		}
		if after.Status != enums.IMConversationStatusClosed || after.CloseReason != "synthetic resolution" {
			t.Fatalf("close state/reason=%d/%q", after.Status, after.CloseReason)
		}
	})

	for _, id := range []int64{2, 3, 4, 5, 6, 7, 8, 9} {
		t.Run(fmt.Sprintf("deny conversation-%d", id), func(t *testing.T) {
			db, principal := conversationMutationFixture(t)
			principal.Permissions = append(principal.Permissions, constants.PermissionConversationClose.Code)
			var before models.Conversation
			if err := db.First(&before, id).Error; err != nil {
				t.Fatal(err)
			}
			var beforeEvents int64
			db.Model(&models.ConversationEventLog{}).Where("conversation_id = ?", id).Count(&beforeEvents)

			result := conversationMutationHTTPRequest(t, principal, "/close", "application/json", fmt.Sprintf(`{"conversationId":%d,"closeReason":"synthetic resolution"}`, id))
			if result.Code == 0 {
				t.Fatalf("out-of-scope close unexpectedly succeeded for conversation %d", id)
			}
			var after models.Conversation
			if err := db.First(&after, id).Error; err != nil {
				t.Fatal(err)
			}
			var afterEvents int64
			db.Model(&models.ConversationEventLog{}).Where("conversation_id = ?", id).Count(&afterEvents)
			if after.Status != before.Status || after.CloseReason != before.CloseReason || afterEvents != beforeEvents {
				t.Fatalf("denied close wrote conversation/event: before=%+v/%d after=%+v/%d", before, beforeEvents, after, afterEvents)
			}
		})
	}
}

func TestConversationMutationsRecheckScopeAfterInitialAuthorization(t *testing.T) {
	tests := []struct {
		name, path, contentType, body, permission, triggerTable string
		triggerQuery                                            int
		removeTag                                               bool
	}{
		{"send", "/send", "application/json", `{"conversationId":1,"clientMsgId":"synthetic-race-send","messageType":"text","content":"synthetic message"}`, constants.PermissionConversationSend.Code, "messages", 1, false},
		{"add tag", "/add_tag", "application/json", `{"conversationId":1,"tagId":1}`, constants.PermissionConversationTag.Code, "conversations", 1, false},
		{"remove tag", "/remove_tag", "application/json", `{"conversationId":1,"tagId":1}`, constants.PermissionConversationTag.Code, "conversations", 1, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, principal := conversationMutationFixture(t)
			principal.Permissions = append(principal.Permissions, test.permission)
			if test.removeTag {
				if err := db.Create(&models.ConversationTag{ConversationID: 1, TagID: 1}).Error; err != nil {
					t.Fatal(err)
				}
			}
			var beforeMessages, beforeEvents, beforeTags int64
			db.Model(&models.Message{}).Where("conversation_id = ?", 1).Count(&beforeMessages)
			db.Model(&models.ConversationEventLog{}).Where("conversation_id = ?", 1).Count(&beforeEvents)
			db.Model(&models.ConversationTag{}).Where("conversation_id = ?", 1).Count(&beforeTags)

			conversationQueries := 0
			interleaved := false
			callback := "synthetic:reassign_before_mutation_persistence"
			if err := db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == test.triggerTable {
					conversationQueries++
				}
				if tx.Statement.Table != test.triggerTable || conversationQueries != test.triggerQuery || interleaved {
					return
				}
				interleaved = true
				changes := map[string]any{"current_assignee_id": 102}
				if test.name == "remove tag" {
					changes = map[string]any{"current_assignee_id": 0, "status": enums.IMConversationStatusPending}
				}
				if err := db.Model(&models.Conversation{}).Where("id = ?", 1).UpdateColumns(changes).Error; err != nil {
					t.Errorf("interleave reassignment: %v", err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Query().Remove(callback) })

			result := conversationMutationHTTPRequest(t, principal, test.path, test.contentType, test.body)
			if !interleaved {
				t.Fatal("deterministic assignment change did not occur")
			}
			if result.Code == 0 {
				t.Fatal("mutation succeeded after the operator lost assignment")
			}
			var afterMessages, afterEvents, afterTags int64
			db.Model(&models.Message{}).Where("conversation_id = ?", 1).Count(&afterMessages)
			db.Model(&models.ConversationEventLog{}).Where("conversation_id = ?", 1).Count(&afterEvents)
			db.Model(&models.ConversationTag{}).Where("conversation_id = ?", 1).Count(&afterTags)
			if afterMessages != beforeMessages || afterEvents != beforeEvents || afterTags != beforeTags {
				t.Fatalf("stale mutation wrote data: messages %d->%d events %d->%d tags %d->%d", beforeMessages, afterMessages, beforeEvents, afterEvents, beforeTags, afterTags)
			}
		})
	}
}

func TestCloseRechecksScopeAfterPriorAuthorization(t *testing.T) {
	db, principal := conversationMutationFixture(t)
	principal.Permissions = append(principal.Permissions, constants.PermissionConversationClose.Code)
	if _, err := services.ConversationScopeService.Authorize(db, principal, 1, services.ConversationScopeActiveWrite); err != nil {
		t.Fatalf("initial authorization failed: %v", err)
	}
	if err := db.Model(&models.Conversation{}).Where("id = ?", 1).UpdateColumns(map[string]any{"current_assignee_id": 102, "status": enums.IMConversationStatusPending}).Error; err != nil {
		t.Fatal(err)
	}
	var beforeEvents int64
	db.Model(&models.ConversationEventLog{}).Where("conversation_id = ?", 1).Count(&beforeEvents)

	result := conversationMutationHTTPRequest(t, principal, "/close", "application/json", `{"conversationId":1,"closeReason":"synthetic resolution"}`)
	if result.Code == 0 {
		t.Fatal("close succeeded after the operator lost assignment")
	}
	var after models.Conversation
	if err := db.First(&after, 1).Error; err != nil {
		t.Fatal(err)
	}
	var afterEvents int64
	db.Model(&models.ConversationEventLog{}).Where("conversation_id = ?", 1).Count(&afterEvents)
	if after.Status != enums.IMConversationStatusPending || after.CurrentAssigneeID != 102 || after.CloseReason != "" || afterEvents != beforeEvents {
		t.Fatalf("stale close wrote state/audit: %+v events %d->%d", after, beforeEvents, afterEvents)
	}
}

func TestSuperAdminCanCloseLegacyEligiblePendingAndAIServingConversations(t *testing.T) {
	for _, id := range []int64{4, 6} {
		t.Run(fmt.Sprintf("conversation-%d", id), func(t *testing.T) {
			db, principal := conversationMutationFixture(t)
			principal.Roles = []string{constants.RoleCodeSuperAdmin}
			principal.Permissions = append(principal.Permissions, constants.PermissionConversationClose.Code)
			var before models.Conversation
			if err := db.First(&before, id).Error; err != nil {
				t.Fatal(err)
			}
			var beforeEvents int64
			db.Model(&models.ConversationEventLog{}).Where("conversation_id = ?", id).Count(&beforeEvents)

			result := conversationMutationHTTPRequest(t, principal, "/close", "application/json", fmt.Sprintf(`{"conversationId":%d,"closeReason":"synthetic admin resolution"}`, id))
			if result.Code != 0 {
				t.Fatalf("super_admin could not close legacy-eligible state %d: code=%d", before.Status, result.Code)
			}
			var after models.Conversation
			if err := db.First(&after, id).Error; err != nil {
				t.Fatal(err)
			}
			var afterEvents int64
			db.Model(&models.ConversationEventLog{}).Where("conversation_id = ?", id).Count(&afterEvents)
			if after.Status != enums.IMConversationStatusClosed || after.CloseReason != "synthetic admin resolution" || after.ClosedBy != principal.UserID || afterEvents != beforeEvents+1 {
				t.Fatalf("global close result=%+v events %d->%d", after, beforeEvents, afterEvents)
			}
		})
	}
}

func TestConversationTeamLeaderMutationsStayWithinTeam(t *testing.T) {
	t.Run("tag teammate", func(t *testing.T) {
		db, principal := conversationMutationFixture(t)
		principal.Roles = []string{constants.RoleCodeCsTeamLeader}
		principal.Permissions = append(principal.Permissions, constants.PermissionConversationTag.Code)
		result := conversationMutationHTTPRequest(t, principal, "/add_tag", "application/json", `{"conversationId":3,"tagId":1}`)
		if result.Code != 0 {
			t.Fatalf("team leader could not tag an active teammate conversation: code=%d", result.Code)
		}
		var count int64
		db.Model(&models.ConversationTag{}).Where("conversation_id = ? AND tag_id = ?", 3, 1).Count(&count)
		if count != 1 {
			t.Fatalf("team conversation tag count=%d, want 1", count)
		}
	})

	t.Run("deny other team", func(t *testing.T) {
		db, principal := conversationMutationFixture(t)
		principal.Roles = []string{constants.RoleCodeCsTeamLeader}
		principal.Permissions = append(principal.Permissions, constants.PermissionConversationTag.Code)
		result := conversationMutationHTTPRequest(t, principal, "/add_tag", "application/json", `{"conversationId":7,"tagId":1}`)
		if result.Code != errorsx.CodeAuthForbidden {
			t.Fatalf("team leader other-team tag response code=%d, want forbidden", result.Code)
		}
		var count int64
		db.Model(&models.ConversationTag{}).Where("conversation_id = ? AND tag_id = ?", 7, 1).Count(&count)
		if count != 0 {
			t.Fatalf("denied other-team tag wrote relation: count=%d", count)
		}
	})

	t.Run("close teammate", func(t *testing.T) {
		db, principal := conversationMutationFixture(t)
		principal.Roles = []string{constants.RoleCodeCsTeamLeader}
		principal.Permissions = append(principal.Permissions, constants.PermissionConversationClose.Code)
		result := conversationMutationHTTPRequest(t, principal, "/close", "application/json", `{"conversationId":3,"closeReason":"  synthetic team resolution  "}`)
		if result.Code != 0 {
			t.Fatalf("team leader could not close an active teammate conversation: code=%d", result.Code)
		}
		var after models.Conversation
		if err := db.First(&after, 3).Error; err != nil {
			t.Fatal(err)
		}
		if after.Status != enums.IMConversationStatusClosed || after.CloseReason != "synthetic team resolution" {
			t.Fatalf("close state/reason=%d/%q", after.Status, after.CloseReason)
		}
	})

	t.Run("deny close other team", func(t *testing.T) {
		db, principal := conversationMutationFixture(t)
		principal.Roles = []string{constants.RoleCodeCsTeamLeader}
		principal.Permissions = append(principal.Permissions, constants.PermissionConversationClose.Code)
		result := conversationMutationHTTPRequest(t, principal, "/close", "application/json", `{"conversationId":7,"closeReason":"synthetic out-of-team resolution"}`)
		if result.Code == 0 {
			t.Fatal("team leader closed another team's conversation")
		}
		var after models.Conversation
		if err := db.First(&after, 7).Error; err != nil {
			t.Fatal(err)
		}
		var events int64
		db.Model(&models.ConversationEventLog{}).Where("conversation_id = ?", 7).Count(&events)
		if after.Status != enums.IMConversationStatusActive || after.CloseReason != "" || events != 0 {
			t.Fatalf("denied cross-team close wrote data: %+v events=%d", after, events)
		}
	})
}

func TestConversationSendPermissionDoesNotAuthorizeRecall(t *testing.T) {
	db, principal := conversationMutationFixture(t)
	principal.Permissions = []string{constants.PermissionConversationSend.Code}
	message := &models.Message{ID: 50, ConversationID: 1, ClientMsgID: "synthetic-recall-authorization", SenderType: enums.IMSenderTypeAgent, SenderID: principal.UserID, MessageType: enums.IMMessageTypeText, Content: "synthetic recall target", SendStatus: enums.IMMessageStatusSent}
	if err := db.Create(message).Error; err != nil {
		t.Fatal(err)
	}
	result := conversationMutationHTTPRequest(t, principal, "/recall", "application/json", `{"messageId":50}`)
	if result.Code != errorsx.CodeAuthForbidden {
		t.Fatalf("conversation.send authorized recall: response code=%d", result.Code)
	}
	var after models.Message
	if err := db.First(&after, message.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.RecalledAt != nil || after.SendStatus != enums.IMMessageStatusSent {
		t.Fatalf("denied recall changed message: status=%v recalledAt=%v", after.SendStatus, after.RecalledAt)
	}
}
