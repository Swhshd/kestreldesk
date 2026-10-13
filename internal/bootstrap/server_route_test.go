package bootstrap

// This file has been modified for the KestrelDesk project.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
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

func TestNewServerRegistersGinRoutes(t *testing.T) {
	config.SetCurrent(&config.Config{
		Storage: config.StorageConfig{
			Local: config.LocalStorageConfig{
				Root:    "storage",
				BaseURL: "/storage",
			},
		},
	})

	app, err := NewServer()
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	routes := make(map[string]bool)
	for _, route := range app.Routes() {
		routes[route.Method+" "+route.Path] = true
	}

	expected := []string{
		http.MethodPost + " /api/auth/login",
		http.MethodPost + " /api/auth/password_reset/request",
		http.MethodPost + " /api/auth/password_reset/reset",
		http.MethodPost + " /api/support/auth/login",
		http.MethodPost + " /api/support/auth/password_reset/request",
		http.MethodPost + " /api/support/auth/password_reset/reset",
		http.MethodPost + " /api/support/auth/register",
		http.MethodPost + " /api/support/profile/update",
		http.MethodPost + " /api/support/profile/avatar/upload",
		http.MethodGet + " /api/support/me",
		http.MethodPost + " /api/support/community/posts/delete",
		http.MethodGet + " /api/config",
		http.MethodGet + " /api/health",
		http.MethodGet + " /api/auth/oidc_login",
		http.MethodGet + " /api/auth/oidc_callback",
		http.MethodGet + " /api/auth/callback/custom",
		http.MethodPost + " /api/auth/oidc_exchange",
		http.MethodGet + " /api/auth/profile",
		http.MethodPost + " /api/webhooks/org-sync",
		http.MethodGet + " /api/dashboard/organization/my_list",
		http.MethodPost + " /api/dashboard/organization/switch",
		http.MethodGet + " /api/dashboard/user/list",
		http.MethodGet + " /api/dashboard/user/:id",
		http.MethodPost + " /api/dashboard/user/create",
		http.MethodPost + " /api/dashboard/conversation/send_message",
		http.MethodPost + " /api/dashboard/conversation/claim",
		http.MethodGet + " /api/dashboard/ai-workflow/default-definition",
		http.MethodGet + " /api/dashboard/ai-workflow/template/list",
		http.MethodGet + " /api/dashboard/ai-workflow/run/list",
		http.MethodGet + " /api/dashboard/ai-workflow/run/:id",
		http.MethodGet + " /api/dashboard/agent-run/metrics",
		http.MethodPost + " /api/dashboard/agent-run/evaluate",
		http.MethodGet + " /api/dashboard/agent-run/:id",
		http.MethodPost + " /api/dashboard/ai-agent/rollback_rollout",
		http.MethodPost + " /api/dashboard/channel/rollback_ai_agent_rollout",
		http.MethodPost + " /api/dashboard/agent-run/quality_feedback",
		http.MethodGet + " /api/dashboard/agent-run/list",
		http.MethodGet + " /api/ws/dashboard",
		http.MethodGet + " /api/ws/open",
	}
	for _, route := range expected {
		if !routes[route] {
			t.Fatalf("expected route %s to be registered", route)
		}
	}
}

func TestSupportCustomerAccountEndpointsEnforceCustomerIdentity(t *testing.T) {
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

	createIdentity := func(username string, userType enums.UserType) (*models.User, string) {
		t.Helper()
		const password = "synthetic-password"
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			t.Fatalf("hash synthetic password: %v", err)
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
			t.Fatalf("create identity fixture: %v", err)
		}
		return user, password
	}
	type identity struct {
		name     string
		userID   int64
		username string
		password string
		token    string
	}
	login := func(user *models.User, password string) string {
		t.Helper()
		ret, err := services.AuthService.Login(request.LoginRequest{Username: user.Username, Password: password}, config.AuthConfig{TokenTTLHours: 2}, "127.0.0.1", "go-test")
		if err != nil || ret == nil || ret.AccessToken == "" {
			t.Fatalf("generic login fixture failed for %s, response present=%t, error=%v", user.Username, ret != nil, err)
		}
		return ret.AccessToken
	}

	customer, customerPassword := createIdentity("registered-customer", enums.UserTypeUser)
	if err := db.Create(&models.Customer{UserID: customer.ID, Name: "Synthetic Customer", Status: enums.StatusOk}).Error; err != nil {
		t.Fatalf("create active customer fixture: %v", err)
	}
	employee, employeePassword := createIdentity("support-employee", enums.UserTypeEmployee)
	if err := db.Create(&models.Customer{UserID: employee.ID, Name: "Synthetic Employee Customer", Status: enums.StatusOk}).Error; err != nil {
		t.Fatalf("create employee-associated customer fixture: %v", err)
	}
	admin, adminPassword := createIdentity("support-admin", enums.UserTypeEmployee)
	if err := db.Create(&models.Customer{UserID: admin.ID, Name: "Synthetic Admin Customer", Status: enums.StatusOk}).Error; err != nil {
		t.Fatalf("create admin-associated customer fixture: %v", err)
	}
	adminRole := &models.Role{Code: constants.RoleCodeSuperAdmin, Name: "Synthetic Super Admin", Status: enums.StatusOk}
	if err := db.Create(adminRole).Error; err != nil {
		t.Fatalf("create admin role: %v", err)
	}
	if err := db.Create(&models.UserRole{UserID: admin.ID, RoleID: adminRole.ID}).Error; err != nil {
		t.Fatalf("assign admin role: %v", err)
	}
	missingCustomer, missingPassword := createIdentity("registered-without-customer", enums.UserTypeUser)
	identities := []identity{
		{name: "registered customer", userID: customer.ID, username: customer.Username, password: customerPassword, token: login(customer, customerPassword)},
		{name: "employee", userID: employee.ID, username: employee.Username, password: employeePassword, token: login(employee, employeePassword)},
		{name: "administrator", userID: admin.ID, username: admin.Username, password: adminPassword, token: login(admin, adminPassword)},
		{name: "user without customer record", userID: missingCustomer.ID, username: missingCustomer.Username, password: missingPassword, token: login(missingCustomer, missingPassword)},
		{name: "guest"},
	}

	app, err := NewServer()
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	requests := []struct {
		name   string
		method string
		path   string
		body   string
		avatar bool
	}{
		{name: "me", method: http.MethodGet, path: "/api/support/me"},
		{name: "profile update", method: http.MethodPost, path: "/api/support/profile/update", body: `{"nickname":"Support Customer Updated"}`},
		{name: "profile avatar upload", method: http.MethodPost, path: "/api/support/profile/avatar/upload", avatar: true},
	}
	for _, identity := range identities {
		for _, endpoint := range requests {
			name := identity.name + " / " + endpoint.name
			t.Run(name, func(t *testing.T) {
				rec := httptest.NewRecorder()
				requestBody := strings.NewReader(endpoint.body)
				contentType := ""
				if endpoint.avatar {
					body, multipartType := supportTestAvatarBody(t)
					requestBody = strings.NewReader(body.String())
					contentType = multipartType
				} else if endpoint.body != "" {
					contentType = "application/json"
				}
				req := httptest.NewRequest(endpoint.method, endpoint.path, requestBody)
				if contentType != "" {
					req.Header.Set("Content-Type", contentType)
				}
				if identity.token != "" {
					req.Header.Set("Authorization", "Bearer "+identity.token)
				}
				app.ServeHTTP(rec, req)
				var body struct {
					Success bool `json:"success"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
				}
				wantSuccess := identity.name == "registered customer"
				if body.Success != wantSuccess {
					t.Fatalf("success=%t, want %t; status=%d body=%s", body.Success, wantSuccess, rec.Code, rec.Body.String())
				}
			})
		}
	}

	// The Support login endpoint is customer-only and leaves the generic login
	// route available to employee accounts.
	for _, identity := range identities[:4] {
		var before int64
		if err := db.Model(&models.LoginSession{}).Where("user_id = ?", identity.userID).Count(&before).Error; err != nil {
			t.Fatalf("count sessions before Support login: %v", err)
		}
		rec := httptest.NewRecorder()
		body := `{"username":"` + identity.username + `","password":"` + identity.password + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/support/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		app.ServeHTTP(rec, req)
		var result struct {
			Success bool `json:"success"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode Support login response: %v; body=%s", err, rec.Body.String())
		}
		wantSuccess := identity.name == "registered customer"
		if result.Success != wantSuccess {
			t.Fatalf("Support login success=%t, want %t for %s; status=%d body=%s", result.Success, wantSuccess, identity.name, rec.Code, rec.Body.String())
		}
		var after int64
		if err := db.Model(&models.LoginSession{}).Where("user_id = ?", identity.userID).Count(&after).Error; err != nil {
			t.Fatalf("count sessions after Support login: %v", err)
		}
		wantAfter := before
		if wantSuccess {
			wantAfter++
		}
		if after != wantAfter {
			t.Fatalf("Support login for %s changed LoginSession count from %d to %d, want %d", identity.name, before, after, wantAfter)
		}
	}
	genericLogin := httptest.NewRecorder()
	genericLoginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"`+employee.Username+`","password":"`+employeePassword+`"}`))
	genericLoginRequest.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(genericLogin, genericLoginRequest)
	var genericLoginResult struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(genericLogin.Body.Bytes(), &genericLoginResult); err != nil || !genericLoginResult.Success {
		t.Fatalf("generic employee login failed: status=%d body=%s error=%v", genericLogin.Code, genericLogin.Body.String(), err)
	}

	post := &models.Post{UserID: customer.ID, Title: "Synthetic public post", Content: "Synthetic post content", Status: enums.PostStatusNormal}
	if err := db.Create(post).Error; err != nil {
		t.Fatalf("create public community post fixture: %v", err)
	}
	for _, path := range []string{
		"/api/support/community/posts/list?limit=10",
		"/api/support/community/posts/" + strconv.FormatInt(post.ID, 10),
	} {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), post.Title) {
			t.Fatalf("anonymous public community read %s failed: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	customerCommentPayload, err := json.Marshal(map[string]any{"postId": post.ID, "content": "Synthetic customer comment"})
	if err != nil {
		t.Fatalf("marshal synthetic customer comment: %v", err)
	}
	guestComment := httptest.NewRecorder()
	guestCommentRequest := httptest.NewRequest(http.MethodPost, "/api/support/community/comments/create", bytes.NewReader(customerCommentPayload))
	guestCommentRequest.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(guestComment, guestCommentRequest)
	var guestCommentResult struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(guestComment.Body.Bytes(), &guestCommentResult); err != nil || guestCommentResult.Success {
		t.Fatalf("unauthenticated guest comment unexpectedly succeeded: status=%d body=%s error=%v", guestComment.Code, guestComment.Body.String(), err)
	}
	customerComment := httptest.NewRecorder()
	customerCommentRequest := httptest.NewRequest(http.MethodPost, "/api/support/community/comments/create", bytes.NewReader(customerCommentPayload))
	customerCommentRequest.Header.Set("Content-Type", "application/json")
	customerCommentRequest.Header.Set("Authorization", "Bearer "+identities[0].token)
	app.ServeHTTP(customerComment, customerCommentRequest)
	var customerCommentResult struct {
		Success bool `json:"success"`
		Data    struct {
			AuthorType enums.CommentAuthorType `json:"authorType"`
			AuthorID   int64                   `json:"authorId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(customerComment.Body.Bytes(), &customerCommentResult); err != nil || !customerCommentResult.Success {
		t.Fatalf("registered customer comment failed: status=%d body=%s error=%v", customerComment.Code, customerComment.Body.String(), err)
	}
	if customerCommentResult.Data.AuthorType != enums.CommentAuthorTypeUser || customerCommentResult.Data.AuthorID != customer.ID {
		t.Fatalf("registered customer comment identity mismatch: type=%q author=%d body=%s", customerCommentResult.Data.AuthorType, customerCommentResult.Data.AuthorID, customerComment.Body.String())
	}
	createEmployeeComment := func(parentID int64, content string) models.Comment {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"postId": post.ID, "parentId": parentID, "content": content})
		if err != nil {
			t.Fatalf("marshal synthetic community comment request: %v", err)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/support/community/comments/create", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+identities[1].token)
		app.ServeHTTP(rec, req)
		var result struct {
			Success bool `json:"success"`
			Data    struct {
				ID         int64                   `json:"id"`
				ParentID   int64                   `json:"parentId"`
				AuthorID   int64                   `json:"authorId"`
				AuthorType enums.CommentAuthorType `json:"authorType"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || !result.Success {
			t.Fatalf("employee comment request failed: status=%d body=%s error=%v", rec.Code, rec.Body.String(), err)
		}
		if result.Data.AuthorType != enums.CommentAuthorTypeEmployee || result.Data.AuthorID != employee.ID || result.Data.ParentID != parentID {
			t.Fatalf("employee comment response identity mismatch: type=%q author=%d parent=%d body=%s", result.Data.AuthorType, result.Data.AuthorID, result.Data.ParentID, rec.Body.String())
		}
		var stored models.Comment
		if err := db.First(&stored, result.Data.ID).Error; err != nil {
			t.Fatalf("read stored employee comment: %v", err)
		}
		if stored.AuthorType != enums.CommentAuthorTypeEmployee || stored.AuthorID != employee.ID || stored.ParentID != parentID {
			t.Fatalf("stored employee comment identity mismatch: type=%q author=%d parent=%d", stored.AuthorType, stored.AuthorID, stored.ParentID)
		}
		return stored
	}
	employeeComment := createEmployeeComment(0, "Synthetic employee comment")
	createEmployeeComment(employeeComment.ID, "Synthetic employee reply")

	// Shared employee profile routes remain generic-auth endpoints and continue
	// to accept the same employee session.
	for _, endpoint := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/api/auth/profile"},
		{method: http.MethodPost, path: "/api/auth/profile/update", body: `{"nickname":"Employee Profile Updated"}`},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(endpoint.body))
		if endpoint.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+identities[1].token)
		app.ServeHTTP(rec, req)
		var body struct {
			Success bool `json:"success"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.Success {
			t.Fatalf("generic employee profile route %s %s failed: status=%d body=%s error=%v", endpoint.method, endpoint.path, rec.Code, rec.Body.String(), err)
		}
	}
}

func supportTestAvatarBody(t *testing.T) (*bytes.Buffer, string) {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+nm1sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode synthetic PNG: %v", err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="file"; filename="synthetic-avatar.png"`},
		"Content-Type":        {"image/png"},
	})
	if err != nil {
		t.Fatalf("create synthetic avatar form part: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write synthetic avatar: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close synthetic avatar form: %v", err)
	}
	return &body, writer.FormDataContentType()
}

func TestNewServerHealthEndpointIsPublic(t *testing.T) {
	config.SetCurrent(&config.Config{
		Storage: config.StorageConfig{
			Local: config.LocalStorageConfig{
				Root:    "storage",
				BaseURL: "/storage",
			},
		},
	})

	app, err := NewServer()
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Success bool `json:"success"`
		Data    struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !body.Success {
		t.Fatalf("success=false, body=%s", rec.Body.String())
	}
	if body.Data.Status != "ok" {
		t.Fatalf("status=%q want ok", body.Data.Status)
	}
}

func TestNewServerExposesPublicConfig(t *testing.T) {
	config.SetCurrent(&config.Config{
		Language: "zh-CN",
		Storage: config.StorageConfig{
			Local: config.LocalStorageConfig{
				Root:    "storage",
				BaseURL: "/storage",
			},
		},
		WxWork: config.WxWorkConfig{
			Enabled: true,
		},
		OIDC: config.OIDCConfig{
			Enabled:      false,
			ClientSecret: "must-not-leak",
		},
	})

	app, err := NewServer()
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		Success bool `json:"success"`
		Data    struct {
			Language             string `json:"language"`
			PasswordLoginEnabled bool   `json:"passwordLoginEnabled"`
			WxWorkEnabled        bool   `json:"wxworkEnabled"`
			OIDCEnabled          bool   `json:"oidcEnabled"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !body.Success {
		t.Fatalf("success=false, body=%s", rec.Body.String())
	}
	if body.Data.Language != "zh-CN" {
		t.Fatalf("language=%q want zh-CN", body.Data.Language)
	}
	if !body.Data.PasswordLoginEnabled {
		t.Fatalf("passwordLoginEnabled=false want true")
	}
	if !body.Data.WxWorkEnabled {
		t.Fatalf("wxworkEnabled=false want true")
	}
	if body.Data.OIDCEnabled {
		t.Fatalf("oidcEnabled=true want false")
	}
	if strings.Contains(rec.Body.String(), "must-not-leak") {
		t.Fatalf("response leaked sensitive OIDC config: %s", rec.Body.String())
	}
}

func TestNewServerDoesNotExposeLegacyAuthOptions(t *testing.T) {
	config.SetCurrent(&config.Config{
		Storage: config.StorageConfig{
			Local: config.LocalStorageConfig{
				Root:    "storage",
				BaseURL: "/storage",
			},
		},
	})

	app, err := NewServer()
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/options", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestNewServerPasswordLoginDisabled(t *testing.T) {
	disabled := false
	config.SetCurrent(&config.Config{
		Auth: config.AuthConfig{
			PasswordLoginEnabled: &disabled,
		},
		Storage: config.StorageConfig{
			Local: config.LocalStorageConfig{
				Root:    "storage",
				BaseURL: "/storage",
			},
		},
	})

	app, err := NewServer()
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	// 1. /api/config should return passwordLoginEnabled=false
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusOK)
	}
	var configBody struct {
		Data struct {
			PasswordLoginEnabled bool `json:"passwordLoginEnabled"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &configBody); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if configBody.Data.PasswordLoginEnabled {
		t.Fatalf("expected passwordLoginEnabled=false, got true")
	}

	// 2. /api/auth/login should be rejected
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(rec, req)

	var loginBody struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &loginBody); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if loginBody.Success {
		t.Fatalf("expected login failure when password login is disabled")
	}
}

func TestNewServerSeparatesAPIStaticAndSPA(t *testing.T) {
	for _, rootDir := range []string{"web/out", "../web/out", "../../web/out"} {
		_ = os.MkdirAll(rootDir, 0o755)
		_ = os.WriteFile(filepath.Join(rootDir, "index.html"), []byte("<html>spa</html>"), 0o644)
		defer func(d string) {
			_ = os.Remove(filepath.Join(d, "index.html"))
		}(rootDir)
	}

	config.SetCurrent(&config.Config{
		Storage: config.StorageConfig{
			Local: config.LocalStorageConfig{
				Root:    "storage",
				BaseURL: "/storage",
			},
		},
	})

	app, err := NewServer()
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	tests := []struct {
		path        string
		wantStatus  int
		contentType string
	}{
		{path: "/api/not-exists", wantStatus: http.StatusNotFound, contentType: "application/json"},
		{path: "/dashboard/not-exists", wantStatus: http.StatusOK, contentType: "text/html"},
		{path: "/support/docs/runtime-authored-slug", wantStatus: http.StatusOK, contentType: "text/html"},
		{path: "/support/community/posts/12345", wantStatus: http.StatusOK, contentType: "text/html"},
		{path: "/support/community/categories/product", wantStatus: http.StatusOK, contentType: "text/html"},
		{path: "/support/community/posts/new", wantStatus: http.StatusOK, contentType: "text/html"},
	}

	for _, tt := range tests {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

		if rec.Code != tt.wantStatus {
			t.Fatalf("%s status=%d want %d", tt.path, rec.Code, tt.wantStatus)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), tt.contentType) {
			t.Fatalf("%s Content-Type=%q want %q", tt.path, rec.Header().Get("Content-Type"), tt.contentType)
		}
	}
}

func TestNewServerHardensStoredAssetResponses(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"screenshot.png", "archive.zip", "legacy-page.html"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("payload"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}

	config.SetCurrent(&config.Config{
		Storage: config.StorageConfig{
			Local: config.LocalStorageConfig{
				Root:    root,
				BaseURL: "/storage",
			},
		},
	})

	app, err := NewServer()
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	tests := []struct {
		path           string
		wantStatus     int
		contentType    string
		wantAttachment bool
	}{
		{path: "/storage/screenshot.png", wantStatus: http.StatusOK, contentType: "image/png"},
		{path: "/storage/archive.zip", wantStatus: http.StatusOK, wantAttachment: true},
		// A file planted before the upload policy existed must still not render.
		{path: "/storage/legacy-page.html", wantStatus: http.StatusOK, wantAttachment: true},
		{path: "/storage/missing.png", wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

		if rec.Code != tt.wantStatus {
			t.Fatalf("%s status=%d want %d", tt.path, rec.Code, tt.wantStatus)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s X-Content-Type-Options=%q want nosniff", tt.path, got)
		}
		if tt.contentType != "" && !strings.Contains(rec.Header().Get("Content-Type"), tt.contentType) {
			t.Fatalf("%s Content-Type=%q want %q", tt.path, rec.Header().Get("Content-Type"), tt.contentType)
		}
		got := rec.Header().Get("Content-Disposition")
		if tt.wantAttachment && got != "attachment" {
			t.Fatalf("%s Content-Disposition=%q want attachment", tt.path, got)
		}
		if !tt.wantAttachment && got != "" {
			t.Fatalf("%s Content-Disposition=%q want empty so the asset renders inline", tt.path, got)
		}
	}
}

func TestNewServerAllowsConfiguredCORSOrigin(t *testing.T) {
	config.SetCurrent(&config.Config{
		Server: config.ServerConfig{
			CORS: config.CORSConfig{
				AllowedOrigins: []string{"https://console.example.com"},
			},
		},
		Storage: config.StorageConfig{
			Local: config.LocalStorageConfig{
				Root:    "storage",
				BaseURL: "/storage",
			},
		},
	})

	app, err := NewServer()
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/auth/login", nil)
	req.Header.Set("Origin", "https://console.example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://console.example.com" {
		t.Fatalf("Access-Control-Allow-Origin=%q want %q", got, "https://console.example.com")
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, http.MethodPost) {
		t.Fatalf("Access-Control-Allow-Methods=%q should contain %q", got, http.MethodPost)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary=%q want %q", got, "Origin")
	}
}

func TestNewServerRejectsUnconfiguredCORSOrigin(t *testing.T) {
	config.SetCurrent(&config.Config{
		Server: config.ServerConfig{
			CORS: config.CORSConfig{
				AllowedOrigins: []string{"https://console.example.com"},
			},
		},
		Storage: config.StorageConfig{
			Local: config.LocalStorageConfig{
				Root:    "storage",
				BaseURL: "/storage",
			},
		},
	})

	app, err := NewServer()
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/auth/login", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusForbidden)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin=%q want empty", got)
	}
}

func TestNewServerEchoesRequestID(t *testing.T) {
	config.SetCurrent(&config.Config{
		Storage: config.StorageConfig{
			Local: config.LocalStorageConfig{
				Root:    "storage",
				BaseURL: "/storage",
			},
		},
	})

	app, err := NewServer()
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/not-exists", nil)
	req.Header.Set("X-Request-Id", "trace-123")
	app.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-Id"); got != "trace-123" {
		t.Fatalf("X-Request-Id=%q want %q", got, "trace-123")
	}
}

func TestNewServerGeneratesRequestID(t *testing.T) {
	config.SetCurrent(&config.Config{
		Storage: config.StorageConfig{
			Local: config.LocalStorageConfig{
				Root:    "storage",
				BaseURL: "/storage",
			},
		},
	})

	app, err := NewServer()
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/not-exists", nil))

	if got := rec.Header().Get("X-Request-Id"); got == "" {
		t.Fatalf("X-Request-Id should be generated")
	}
}
