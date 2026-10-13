package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/services"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func conversationHTTPFixture(t *testing.T) (*gorm.DB, *dto.AuthPrincipal) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "conversation-http.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	previous := sqls.DB()
	sqls.SetDB(db)
	t.Cleanup(func() { sqls.SetDB(previous); raw, _ := db.DB(); _ = raw.Close() })
	if err := db.AutoMigrate(&models.User{}, &models.AgentProfile{}, &models.AgentTeam{}, &models.Conversation{}, &models.Message{}, &models.ConversationReadState{}, &models.ConversationParticipant{}); err != nil {
		t.Fatal(err)
	}
	for _, team := range []int64{10, 20} {
		if err := db.Create(&models.AgentTeam{ID: team, Name: fmt.Sprintf("synthetic-team-%d", team), Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, employee := range []struct{ id, team int64 }{{101, 10}, {102, 10}, {201, 20}} {
		if err := db.Create(&models.User{ID: employee.id, Username: fmt.Sprintf("synthetic-employee-%d", employee.id), UserType: enums.UserTypeEmployee, Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.AgentProfile{UserID: employee.id, TeamID: employee.team, AgentCode: fmt.Sprintf("synthetic-%d", employee.id), Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows := []models.Conversation{
		{ID: 1, Status: enums.IMConversationStatusActive, CurrentAssigneeID: 101, CurrentTeamID: 10},
		{ID: 2, Status: enums.IMConversationStatusClosed, CurrentAssigneeID: 101, CurrentTeamID: 10},
		{ID: 3, Status: enums.IMConversationStatusActive, CurrentAssigneeID: 102, CurrentTeamID: 10},
		{ID: 4, Status: enums.IMConversationStatusPending, CurrentTeamID: 10},
		{ID: 5, Status: enums.IMConversationStatusPending, CurrentTeamID: 20},
		{ID: 6, Status: enums.IMConversationStatusAIServing, CurrentTeamID: 10},
		{ID: 7, Status: enums.IMConversationStatusActive, CurrentAssigneeID: 201, CurrentTeamID: 20},
		{ID: 8, Status: enums.IMConversationStatusPending, CurrentTeamID: 10, ServiceMode: enums.IMConversationServiceModeAIOnly},
		{ID: 9, Status: enums.IMConversationStatusPending},
	}
	for i := range rows {
		rows[i].CustomerName = "synthetic match"
		rows[i].LastMessageSummary = "synthetic match"
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return db, &dto.AuthPrincipal{UserID: 101, UserType: enums.UserTypeEmployee, Status: enums.StatusOk, Roles: []string{constants.RoleCodeCsUser}, Permissions: []string{constants.PermissionConversationView.Code}}
}

type conversationHTTPResult struct {
	Code int             `json:"errorCode"`
	Data json.RawMessage `json:"data"`
}

func conversationHTTPRequest(t *testing.T, principal *dto.AuthPrincipal, path, body string) conversationHTTPResult {
	t.Helper()
	router := gin.New()
	router.Use(func(ctx *gin.Context) { ctx.Set("authPrincipal", principal) })
	router.GET("/list", ConversationAnyList)
	router.GET("/conversations", ConversationAnyConversations)
	router.GET("/message_list", ConversationAnyMessage_list)
	router.GET("/detail/:id", ConversationGetBy)
	router.POST("/read", ConversationPostRead)
	method := http.MethodGet
	if body != "" {
		method = http.MethodPost
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	var result conversationHTTPResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

func conversationListIDs(t *testing.T, result conversationHTTPResult) []int64 {
	t.Helper()
	if result.Code != 0 {
		t.Fatalf("list denied: code=%d", result.Code)
	}
	var page struct {
		Results []struct {
			ID int64 `json:"id"`
		} `json:"results"`
		Page *struct {
			Total int64 `json:"total"`
		} `json:"page"`
	}
	if err := json.Unmarshal(result.Data, &page); err != nil {
		t.Fatal(err)
	}
	if page.Page == nil || page.Page.Total != int64(len(page.Results)) {
		t.Fatalf("pagination shape/count invalid: %s", result.Data)
	}
	ids := []int64{}
	for _, row := range page.Results {
		ids = append(ids, row.ID)
	}
	slices.Sort(ids)
	return ids
}

func TestConversationHTTPReadScopeLists(t *testing.T) {
	_, p := conversationHTTPFixture(t)
	for _, test := range []struct {
		role, path string
		ids        []int64
	}{
		{constants.RoleCodeCsUser, "/list", []int64{1, 2, 4}},
		{constants.RoleCodeCsTeamLeader, "/list", []int64{1, 2, 3, 4, 6, 8}},
		{constants.RoleCodeSuperAdmin, "/list", []int64{1, 2, 3, 4, 5, 6, 7, 8, 9}},
		{constants.RoleCodeAdmin, "/list", []int64{1, 2, 4}},
		{constants.RoleCodeCsUser, "/conversations?filter=pending", []int64{4}},
		{constants.RoleCodeCsUser, "/conversations?filter=mine", []int64{1, 2}},
		{constants.RoleCodeCsUser, "/conversations?filter=active", []int64{1}},
		{constants.RoleCodeCsUser, "/conversations?filter=closed", []int64{2}},
		{constants.RoleCodeCsUser, "/conversations?filter=ai_serving", []int64{}},
		{constants.RoleCodeCsTeamLeader, "/conversations?filter=active", []int64{1, 3}},
		{constants.RoleCodeSuperAdmin, "/conversations?filter=active", []int64{1, 3, 7}},
	} {
		t.Run(test.role+test.path, func(t *testing.T) {
			p.Roles = []string{test.role}
			got := conversationListIDs(t, conversationHTTPRequest(t, p, test.path, ""))
			if !reflect.DeepEqual(got, test.ids) {
				t.Fatalf("IDs=%v, want=%v", got, test.ids)
			}
		})
	}
}

func TestConversationHTTPReadScopeFiltersOnlyNarrow(t *testing.T) {
	_, p := conversationHTTPFixture(t)
	for _, path := range []string{
		"/list?agentTeamId=20", "/list?teamId=20", "/list?assigneeId=102", "/list?currentAssigneeId=102",
		"/conversations?filter=pending&agentTeamId=20", "/conversations?filter=pending&teamId=20",
		"/conversations?filter=active&assigneeId=102", "/conversations?filter=active&currentAssigneeId=102",
	} {
		t.Run(path, func(t *testing.T) {
			got := conversationListIDs(t, conversationHTTPRequest(t, p, path, ""))
			if len(got) != 0 {
				t.Fatalf("forged filter returned %v", got)
			}
		})
	}
	for _, path := range []string{"/list?keyword=match", "/conversations?filter=pending&keyword=match"} {
		ids := conversationListIDs(t, conversationHTTPRequest(t, p, path, ""))
		for _, id := range ids {
			if id != 1 && id != 2 && id != 4 {
				t.Fatalf("keyword bypassed scope: %v", ids)
			}
		}
	}
	got := conversationListIDs(t, conversationHTTPRequest(t, p, "/list?teamId=10&currentAssigneeId=0", ""))
	if !reflect.DeepEqual(got, []int64{4}) {
		t.Fatalf("allowed narrowing should preserve waiting queue, got %v", got)
	}
}

func TestConversationHTTPReadScopeDirectIDMatrix(t *testing.T) {
	db, p := conversationHTTPFixture(t)
	for _, role := range []string{constants.RoleCodeCsUser, constants.RoleCodeCsTeamLeader, constants.RoleCodeSuperAdmin} {
		p.Roles = []string{role}
		for _, id := range []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 999} {
			allowed := role == constants.RoleCodeSuperAdmin && id != 999 || role == constants.RoleCodeCsUser && slices.Contains([]int64{1, 2, 4}, id) || role == constants.RoleCodeCsTeamLeader && slices.Contains([]int64{1, 2, 3, 4, 6, 8}, id)
			for _, operation := range []string{"detail", "message_list", "read"} {
				t.Run(fmt.Sprintf("%s/%d/%s", role, id, operation), func(t *testing.T) {
					path, body := fmt.Sprintf("/detail/%d", id), ""
					if operation == "message_list" {
						path = fmt.Sprintf("/message_list?conversationId=%d", id)
					}
					if operation == "read" {
						path = "/read"
						body = fmt.Sprintf(`{"conversationId":%d}`, id)
					}
					result := conversationHTTPRequest(t, p, path, body)
					if allowed && result.Code != 0 || !allowed && result.Code != errorsx.CodeAuthForbidden {
						t.Fatalf("allowed=%v response code=%d", allowed, result.Code)
					}
				})
			}
		}
	}
	p.Roles = []string{constants.RoleCodeCsUser}
	if err := db.Model(&models.Conversation{}).Where("id = ?", 1).Update("current_assignee_id", 102).Error; err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/detail/1", "/message_list?conversationId=1"} {
		if result := conversationHTTPRequest(t, p, path, ""); result.Code != errorsx.CodeAuthForbidden {
			t.Fatalf("transferred conversation remains accessible: %s", path)
		}
	}
	if err := services.ConversationService.MarkAgentConversationReadToMessage(1, 0, p); err == nil {
		t.Fatal("read-state service permits transferred conversation")
	}
}

func TestConversationHTTPReadScopeFailClosedAndPermission(t *testing.T) {
	db, p := conversationHTTPFixture(t)
	if err := db.Model(&models.AgentProfile{}).Where("user_id = ?", p.UserID).Update("status", enums.StatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/list", "/conversations?filter=mine", "/detail/1", "/message_list?conversationId=1"} {
		if result := conversationHTTPRequest(t, p, path, ""); result.Code != errorsx.CodeAuthForbidden {
			t.Fatalf("invalid profile allowed: %s", path)
		}
	}
	p.Roles = []string{constants.RoleCodeSuperAdmin}
	p.Permissions = nil
	if result := conversationHTTPRequest(t, p, "/detail/1", ""); result.Code != errorsx.CodeAuthForbidden {
		t.Fatal("scope bypassed endpoint permission")
	}
}

func TestConversationHTTPReadScopeHistoryAndReadState(t *testing.T) {
	db, p := conversationHTTPFixture(t)
	for _, id := range []int64{1, 3} {
		if err := db.Create(&models.Message{ID: id, ConversationID: id, ClientMsgID: fmt.Sprintf("synthetic-%d", id), SenderType: enums.IMSenderTypeCustomer, MessageType: enums.IMMessageTypeText, Content: "synthetic private history"}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&models.Conversation{}).Where("id = ?", id).Updates(map[string]any{"last_message_id": id, "agent_unread_count": 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	result := conversationHTTPRequest(t, p, "/message_list?conversationId=1", "")
	if result.Code != 0 {
		t.Fatal("own history denied")
	}
	var cursor struct {
		Results []struct {
			Content string `json:"content"`
		} `json:"results"`
		Cursor  string `json:"cursor"`
		HasMore bool   `json:"hasMore"`
	}
	if err := json.Unmarshal(result.Data, &cursor); err != nil {
		t.Fatal(err)
	}
	if len(cursor.Results) != 1 || cursor.Results[0].Content != "synthetic private history" {
		t.Fatal("history DTO changed or content missing")
	}
	if result := conversationHTTPRequest(t, p, "/read", `{"conversationId":3,"messageId":3}`); result.Code != errorsx.CodeAuthForbidden {
		t.Fatal("teammate read mutation allowed")
	}
	var denied models.Conversation
	if err := db.First(&denied, 3).Error; err != nil {
		t.Fatal(err)
	}
	if denied.AgentUnreadCount != 1 {
		t.Fatal("denied read changed unread count")
	}
	var readStates int64
	if err := db.Model(&models.ConversationReadState{}).Where("conversation_id = ?", 3).Count(&readStates).Error; err != nil {
		t.Fatal(err)
	}
	if readStates != 0 {
		t.Fatal("denied read created cursor")
	}
	if result := conversationHTTPRequest(t, p, "/read", `{"conversationId":1,"messageId":1}`); result.Code != 0 {
		t.Fatalf("own read denied: %d", result.Code)
	}
	var own models.ConversationReadState
	if err := db.Where("conversation_id = ? AND reader_id = ?", 1, p.UserID).First(&own).Error; err != nil {
		t.Fatal(err)
	}
	if own.LastReadMessageID != 1 {
		t.Fatal("own read did not advance cursor")
	}
}

// The message lookup follows initial authorization but precedes persistence in
// both branches. Reassign there to deterministically exercise a lost scope.
func TestConversationEmployeeReadRevalidatesBeforePersistence(t *testing.T) {
	for _, startID := range []int64{1, 4} {
		for _, withMessage := range []bool{false, true} {
			t.Run(fmt.Sprintf("conversation-%d/message-%v", startID, withMessage), func(t *testing.T) {
				db, principal := conversationHTTPFixture(t)
				if err := db.Model(&models.Conversation{}).Where("id = ?", startID).Updates(map[string]any{"agent_unread_count": 1, "update_user_id": 201, "update_user_name": "synthetic audit marker"}).Error; err != nil {
					t.Fatal(err)
				}
				if withMessage {
					if err := db.Create(&models.Message{ID: 501, ConversationID: startID, ClientMsgID: "synthetic-target", SenderType: enums.IMSenderTypeCustomer, MessageType: enums.IMMessageTypeText, Content: "synthetic read target"}).Error; err != nil {
						t.Fatal(err)
					}
				}
				var before models.Conversation
				if err := db.First(&before, startID).Error; err != nil {
					t.Fatal(err)
				}
				reassigned := false
				if err := db.Callback().Query().After("gorm:query").Register("synthetic:reassign_before_read_persistence", func(tx *gorm.DB) {
					if tx.Statement.Table != "messages" || reassigned {
						return
					}
					reassigned = true
					if err := db.Model(&models.Conversation{}).Where("id = ?", startID).UpdateColumns(map[string]any{"current_assignee_id": 102, "status": enums.IMConversationStatusActive}).Error; err != nil {
						t.Fatal(err)
					}
				}); err != nil {
					t.Fatal(err)
				}
				err := services.ConversationService.MarkAgentConversationReadToMessage(startID, 0, principal)
				if !reassigned {
					t.Fatal("deterministic interleaving did not occur")
				}
				var denied *errorsx.I18nError
				if !errors.As(err, &denied) || denied.Code != errorsx.CodeAuthForbidden {
					t.Errorf("lost scope read should be denied, got %v", err)
				}
				var after models.Conversation
				if err := db.First(&after, startID).Error; err != nil {
					t.Fatal(err)
				}
				if after.AgentUnreadCount != before.AgentUnreadCount {
					t.Errorf("lost scope read changed unread count: %d -> %d", before.AgentUnreadCount, after.AgentUnreadCount)
				}
				if !reflect.DeepEqual(before.AuditFields, after.AuditFields) {
					t.Error("lost scope read changed audit fields")
				}
				var cursors int64
				if err := db.Model(&models.ConversationReadState{}).Where("conversation_id = ?", startID).Count(&cursors).Error; err != nil {
					t.Fatal(err)
				}
				if cursors != 0 {
					t.Error("lost scope read created a cursor")
				}
			})
		}
	}
}
