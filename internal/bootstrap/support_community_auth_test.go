package bootstrap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/request"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/services"
	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type supportCommunityTestIdentity struct {
	name     string
	user     *models.User
	password string
	token    string
}

func TestSupportCommunityAuthorizationMatrix(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{TablePrefix: "t_", SingularTable: true},
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(models.Models...); err != nil {
		t.Fatalf("migrate test models: %v", err)
	}
	sqls.SetDB(db)
	config.SetCurrent(&config.Config{
		Storage: config.StorageConfig{Local: config.LocalStorageConfig{Root: t.TempDir(), BaseURL: "/storage"}},
	})

	createIdentity := func(username string, userType enums.UserType, withCustomer bool) supportCommunityTestIdentity {
		t.Helper()
		const password = "synthetic-community-password"
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			t.Fatalf("hash synthetic test password: %v", err)
		}
		now := time.Now()
		user := &models.User{
			Username: username,
			Nickname: username,
			Password: string(hash),
			UserType: userType,
			Status:   enums.StatusOk,
			AuditFields: models.AuditFields{
				CreatedAt: now,
				UpdatedAt: now,
			},
		}
		if err := db.Create(user).Error; err != nil {
			t.Fatalf("create synthetic identity: %v", err)
		}
		if withCustomer {
			if err := db.Create(&models.Customer{UserID: user.ID, Name: username, Status: enums.StatusOk}).Error; err != nil {
				t.Fatalf("create active customer association: %v", err)
			}
		}
		login, err := services.AuthService.Login(request.LoginRequest{Username: username, Password: password}, config.AuthConfig{TokenTTLHours: 2}, "127.0.0.1", "go-test")
		if err != nil || login == nil || login.AccessToken == "" {
			t.Fatalf("create synthetic session for %s: session present=%t error=%v", username, login != nil, err)
		}
		return supportCommunityTestIdentity{name: username, user: user, password: password, token: login.AccessToken}
	}

	customer := createIdentity("community-customer-owner", enums.UserTypeUser, true)
	otherCustomer := createIdentity("community-other-customer", enums.UserTypeUser, true)
	employee := createIdentity("community-employee", enums.UserTypeEmployee, true)
	admin := createIdentity("community-admin", enums.UserTypeEmployee, true)
	adminRole := &models.Role{Code: constants.RoleCodeSuperAdmin, Name: "Synthetic community admin", Status: enums.StatusOk}
	if err := db.Create(adminRole).Error; err != nil {
		t.Fatalf("create synthetic admin role: %v", err)
	}
	if err := db.Create(&models.UserRole{UserID: admin.user.ID, RoleID: adminRole.ID}).Error; err != nil {
		t.Fatalf("assign synthetic admin role: %v", err)
	}
	guest := supportCommunityTestIdentity{name: "guest"}

	app, err := NewServer()
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	call := func(actor supportCommunityTestIdentity, path string, payload any) (bool, map[string]json.RawMessage, int, string) {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal synthetic community request: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		if actor.token != "" {
			req.Header.Set("Authorization", "Bearer "+actor.token)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		var result struct {
			Success bool                       `json:"success"`
			Data    map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode community response: %v; body=%s", err, rec.Body.String())
		}
		return result.Success, result.Data, rec.Code, rec.Body.String()
	}
	assertAction := func(name string, actor supportCommunityTestIdentity, path string, payload any, want bool) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			success, _, status, body := call(actor, path, payload)
			if success != want {
				t.Errorf("success=%t, want %t; HTTP %d body=%s", success, want, status, body)
			}
		})
	}
	seedPost := func(owner supportCommunityTestIdentity, title string) *models.Post {
		t.Helper()
		post := &models.Post{UserID: owner.user.ID, Title: title, Content: "synthetic content", Status: enums.PostStatusNormal}
		if err := db.Create(post).Error; err != nil {
			t.Fatalf("seed synthetic community post: %v", err)
		}
		return post
	}
	seedComment := func(owner supportCommunityTestIdentity, post *models.Post, authorType enums.CommentAuthorType, content string) *models.Comment {
		t.Helper()
		comment := &models.Comment{PostID: post.ID, AuthorType: authorType, AuthorID: owner.user.ID, Content: content, Status: enums.CommentStatusNormal}
		if err := db.Create(comment).Error; err != nil {
			t.Fatalf("seed synthetic community comment: %v", err)
		}
		return comment
	}

	post := seedPost(customer, "Customer owned acceptance post")
	otherPost := seedPost(otherCustomer, "Other customer owned acceptance post")
	createPostPayload := map[string]any{"title": "Synthetic customer post", "content": "Synthetic post content"}
	assertAction("customer creates a top-level post", customer, "/api/support/community/posts/create", createPostPayload, true)
	assertAction("guest cannot create a top-level post", guest, "/api/support/community/posts/create", createPostPayload, false)
	assertAction("employee cannot create a top-level post", employee, "/api/support/community/posts/create", createPostPayload, false)
	assertAction("admin cannot create a top-level post", admin, "/api/support/community/posts/create", createPostPayload, false)

	updatePost := func(id int64) map[string]any {
		return map[string]any{"id": id, "title": "Updated synthetic post", "content": "Updated synthetic content"}
	}
	assertAction("customer can edit own post", customer, "/api/support/community/posts/update", updatePost(post.ID), true)
	assertAction("different customer cannot edit another post", otherCustomer, "/api/support/community/posts/update", updatePost(post.ID), false)
	assertAction("employee cannot edit a customer post", employee, "/api/support/community/posts/update", updatePost(post.ID), false)
	assertAction("admin cannot edit a customer post", admin, "/api/support/community/posts/update", updatePost(post.ID), false)
	assertAction("guest cannot edit a post", guest, "/api/support/community/posts/update", updatePost(post.ID), false)

	deletePostFor := func(owner supportCommunityTestIdentity, title string) *models.Post {
		return seedPost(owner, title)
	}
	deleteOwnPost := deletePostFor(customer, "Customer post to delete")
	deleteOtherPost := deletePostFor(otherCustomer, "Other customer post to protect")
	assertAction("customer can delete own post", customer, "/api/support/community/posts/delete", map[string]any{"id": deleteOwnPost.ID}, true)
	assertAction("different customer cannot delete another post", customer, "/api/support/community/posts/delete", map[string]any{"id": deleteOtherPost.ID}, false)
	assertAction("employee cannot delete a customer post", employee, "/api/support/community/posts/delete", map[string]any{"id": deleteOtherPost.ID}, false)
	assertAction("admin cannot delete a customer post", admin, "/api/support/community/posts/delete", map[string]any{"id": deleteOtherPost.ID}, false)
	assertAction("guest cannot delete a post", guest, "/api/support/community/posts/delete", map[string]any{"id": deleteOtherPost.ID}, false)
	var deletedPost models.Post
	if err := db.First(&deletedPost, deleteOwnPost.ID).Error; err != nil {
		t.Fatalf("read customer-deleted post: %v", err)
	}
	t.Run("customer post deletion is recorded as deleted", func(t *testing.T) {
		if deletedPost.Status != enums.PostStatusDeleted {
			t.Errorf("post status=%q, want %q", deletedPost.Status, enums.PostStatusDeleted)
		}
	})

	answerPost := seedPost(customer, "Customer answer acceptance post")
	answer := seedComment(otherCustomer, answerPost, enums.CommentAuthorTypeUser, "Synthetic answer")
	acceptPayload := map[string]any{"postId": answerPost.ID, "commentId": answer.ID}
	assertAction("other customer cannot accept an answer", otherCustomer, "/api/support/community/posts/accept_comment", acceptPayload, false)
	assertAction("employee cannot accept an answer", employee, "/api/support/community/posts/accept_comment", acceptPayload, false)
	assertAction("admin cannot accept an answer", admin, "/api/support/community/posts/accept_comment", acceptPayload, false)
	assertAction("guest cannot accept an answer", guest, "/api/support/community/posts/accept_comment", acceptPayload, false)
	assertAction("owner customer can accept an answer", customer, "/api/support/community/posts/accept_comment", acceptPayload, true)

	createComment := func(actor supportCommunityTestIdentity, parentID int64, content string, wantSuccess bool, wantAuthor enums.CommentAuthorType) int64 {
		t.Helper()
		payload := map[string]any{"postId": otherPost.ID, "parentId": parentID, "content": content}
		success, data, status, body := call(actor, "/api/support/community/comments/create", payload)
		if success != wantSuccess {
			t.Errorf("%s create comment success=%t, want %t; HTTP %d body=%s", actor.name, success, wantSuccess, status, body)
			return 0
		}
		if !success {
			return 0
		}
		var gotAuthor enums.CommentAuthorType
		if err := json.Unmarshal(data["authorType"], &gotAuthor); err != nil {
			t.Fatalf("decode comment author type: %v", err)
		}
		if gotAuthor != wantAuthor {
			t.Errorf("comment author type=%q, want %q", gotAuthor, wantAuthor)
		}
		var id int64
		if err := json.Unmarshal(data["id"], &id); err != nil {
			t.Fatalf("decode comment ID: %v", err)
		}
		return id
	}
	customerCommentID := createComment(customer, 0, "Synthetic customer comment", true, enums.CommentAuthorTypeUser)
	employeeCommentID := createComment(employee, 0, "Synthetic employee comment", true, enums.CommentAuthorTypeEmployee)
	adminCommentID := createComment(admin, 0, "Synthetic admin staff comment", true, enums.CommentAuthorTypeEmployee)
	createComment(guest, 0, "Guest comment must be denied", false, "")
	createComment(employee, customerCommentID, "Synthetic employee reply", true, enums.CommentAuthorTypeEmployee)
	createComment(admin, employeeCommentID, "Synthetic admin reply", true, enums.CommentAuthorTypeEmployee)

	updateComment := func(id int64) map[string]any { return map[string]any{"id": id, "content": "Updated synthetic comment"} }
	assertAction("customer can edit own comment", customer, "/api/support/community/comments/update", updateComment(customerCommentID), true)
	assertAction("different customer cannot edit another comment", otherCustomer, "/api/support/community/comments/update", updateComment(customerCommentID), false)
	assertAction("employee cannot edit a customer comment", employee, "/api/support/community/comments/update", updateComment(customerCommentID), false)
	assertAction("admin cannot edit another user's comment", admin, "/api/support/community/comments/update", updateComment(employeeCommentID), false)
	assertAction("employee can edit own staff comment", employee, "/api/support/community/comments/update", updateComment(employeeCommentID), true)
	assertAction("admin can edit own staff comment", admin, "/api/support/community/comments/update", updateComment(adminCommentID), true)
	assertAction("guest cannot edit a comment", guest, "/api/support/community/comments/update", updateComment(customerCommentID), false)

	assertAction("customer can delete own comment", customer, "/api/support/community/comments/delete", map[string]any{"id": customerCommentID}, true)
	assertAction("employee cannot delete a customer comment", employee, "/api/support/community/comments/delete", map[string]any{"id": adminCommentID}, false)
	assertAction("admin cannot delete another staff comment", admin, "/api/support/community/comments/delete", map[string]any{"id": employeeCommentID}, false)
	assertAction("employee can delete own staff comment", employee, "/api/support/community/comments/delete", map[string]any{"id": employeeCommentID}, true)
	assertAction("admin can delete own staff comment", admin, "/api/support/community/comments/delete", map[string]any{"id": adminCommentID}, true)
	assertAction("guest cannot delete a comment", guest, "/api/support/community/comments/delete", map[string]any{"id": customerCommentID}, false)

	reportComment := seedComment(otherCustomer, otherPost, enums.CommentAuthorTypeUser, "Synthetic report target")
	assertAction("customer can report a comment", customer, "/api/support/community/comments/report", map[string]any{"id": reportComment.ID, "reason": "synthetic test"}, true)
	assertAction("guest cannot report a comment", guest, "/api/support/community/comments/report", map[string]any{"id": reportComment.ID, "reason": "synthetic test"}, false)
	assertAction("employee cannot report a comment", employee, "/api/support/community/comments/report", map[string]any{"id": reportComment.ID, "reason": "synthetic test"}, false)
	assertAction("admin cannot report a comment", admin, "/api/support/community/comments/report", map[string]any{"id": reportComment.ID, "reason": "synthetic test"}, false)
	var reportCount int64
	if err := db.Model(&models.CommentReport{}).Where("comment_id = ?", reportComment.ID).Count(&reportCount).Error; err != nil {
		t.Fatalf("count synthetic comment reports: %v", err)
	}
	t.Run("only customer report is stored", func(t *testing.T) {
		if reportCount != 1 {
			t.Errorf("stored report count=%d, want 1", reportCount)
		}
	})

	assertAction("customer can toggle a reaction", customer, "/api/support/community/reactions/toggle", map[string]any{"targetType": "post", "targetId": otherPost.ID}, true)
	assertAction("guest cannot toggle a reaction", guest, "/api/support/community/reactions/toggle", map[string]any{"targetType": "post", "targetId": otherPost.ID}, false)
	assertAction("employee cannot toggle a customer reaction", employee, "/api/support/community/reactions/toggle", map[string]any{"targetType": "post", "targetId": otherPost.ID}, false)
	assertAction("admin cannot toggle a customer reaction", admin, "/api/support/community/reactions/toggle", map[string]any{"targetType": "post", "targetId": otherPost.ID}, false)
	var reactionCount int64
	if err := db.Model(&models.Reaction{}).Where("target_type = ? AND target_id = ?", enums.ReactionTargetPost, otherPost.ID).Count(&reactionCount).Error; err != nil {
		t.Fatalf("count synthetic post reactions: %v", err)
	}
	t.Run("only customer reaction is stored", func(t *testing.T) {
		if reactionCount != 1 {
			t.Errorf("stored reaction count=%d, want 1", reactionCount)
		}
	})
}
