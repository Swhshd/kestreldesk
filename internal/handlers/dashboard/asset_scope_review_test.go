package dashboard

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/services"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/mlogclub/simple/sqls"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func setupAssetScopeHandlerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{TablePrefix: "t_", SingularTable: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	previousDB := sqls.DB()
	previousConfig := config.GetCurrent()
	config.SetCurrent(&config.Config{})
	sqls.SetDB(db)
	t.Cleanup(func() {
		sqls.SetDB(previousDB)
		config.SetCurrent(previousConfig)
		raw, err := db.DB()
		if err == nil {
			_ = raw.Close()
		}
	})
	if err := db.AutoMigrate(&models.User{}, &models.AgentTeam{}, &models.AgentProfile{}, &models.Asset{}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ userID, teamID int64 }{{101, 10}, {201, 20}} {
		if err := db.Create(&models.User{ID: item.userID, Username: fmt.Sprintf("synthetic-employee-%d", item.userID), UserType: enums.UserTypeEmployee, Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.AgentProfile{UserID: item.userID, TeamID: item.teamID, AgentCode: fmt.Sprintf("synthetic-agent-%d", item.userID), Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
		if item.teamID == 10 {
			if err := db.Create(&models.AgentTeam{ID: item.teamID, Name: "synthetic-team", Status: enums.StatusOk}).Error; err != nil {
				t.Fatal(err)
			}
		} else if err := db.Create(&models.AgentTeam{ID: item.teamID, Name: "synthetic-team-20", Status: enums.StatusOk}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []models.Asset{
		{AssetID: "synthetic-own-asset", Filename: "own-asset-marker", StorageKey: "own-key", Provider: enums.AssetProviderLocal, Status: enums.AssetStatusSuccess, AuditFields: models.AuditFields{CreateUserID: 101}},
		{AssetID: "synthetic-foreign-asset", Filename: "foreign-asset-marker", StorageKey: "foreign-key", Provider: enums.AssetProviderLocal, Status: enums.AssetStatusSuccess, AuditFields: models.AuditFields{CreateUserID: 201}},
	} {
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func assetScopeRequest(t *testing.T, method, path string, principal *dto.AuthPrincipal) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, path, nil)
	ctx.Set("authPrincipal", principal)
	return ctx, recorder
}

func TestAssetHandlersRestrictGenericListAndDetailToUploader(t *testing.T) {
	db := setupAssetScopeHandlerDB(t)
	_ = db
	csUser := &dto.AuthPrincipal{UserID: 101, UserType: enums.UserTypeEmployee, Status: enums.StatusOk, Roles: []string{constants.RoleCodeCsUser}, Permissions: []string{constants.PermissionAssetView.Code}}

	ctx, recorder := assetScopeRequest(t, "GET", "/", csUser)
	AssetAnyList(ctx)
	body := recorder.Body.String()
	if !strings.Contains(body, "own-asset-marker") {
		t.Fatalf("asset list omitted caller-owned asset: %s", body)
	}
	if strings.Contains(body, "foreign-asset-marker") {
		t.Fatalf("asset list exposed another employee's asset: %s", body)
	}

	ctx, recorder = assetScopeRequest(t, "GET", "/?createUserId=201", csUser)
	AssetAnyList(ctx)
	if strings.Contains(recorder.Body.String(), "foreign-asset-marker") {
		t.Fatalf("forged createUserId exposed another employee's asset: %s", recorder.Body.String())
	}

	ctx, recorder = assetScopeRequest(t, "GET", "/2", csUser)
	ctx.Params = gin.Params{{Key: "id", Value: "2"}}
	AssetGetBy(ctx)
	if strings.Contains(recorder.Body.String(), "foreign-asset-marker") {
		t.Fatalf("direct asset ID exposed another employee's asset: %s", recorder.Body.String())
	}

	superAdmin := &dto.AuthPrincipal{UserID: 101, UserType: enums.UserTypeEmployee, Status: enums.StatusOk, Roles: []string{constants.RoleCodeSuperAdmin}, Permissions: []string{constants.PermissionAssetView.Code}}
	ctx, recorder = assetScopeRequest(t, "GET", "/2", superAdmin)
	ctx.Params = gin.Params{{Key: "id", Value: "2"}}
	AssetGetBy(ctx)
	if !strings.Contains(recorder.Body.String(), "foreign-asset-marker") {
		t.Fatalf("super_admin lost global asset detail access: %s", recorder.Body.String())
	}
}

func TestDeleteAssetScopesWriteToUploaderAndSuperAdmin(t *testing.T) {
	db := setupAssetScopeHandlerDB(t)
	teamLeader := &dto.AuthPrincipal{
		UserID: 101, UserType: enums.UserTypeEmployee, Status: enums.StatusOk,
		Roles: []string{constants.RoleCodeCsTeamLeader},
	}
	if err := services.AssetService.DeleteAsset(2, teamLeader); err == nil {
		t.Fatal("team leader deleted another employee's asset by numeric ID")
	}
	var foreign models.Asset
	if err := db.First(&foreign, 2).Error; err != nil {
		t.Fatal(err)
	}
	if foreign.Status != enums.AssetStatusSuccess {
		t.Fatalf("denied foreign asset deletion changed status to %v", foreign.Status)
	}
	if err := services.AssetService.DeleteAsset(1, teamLeader); err != nil {
		t.Fatalf("team leader could not delete own asset: %v", err)
	}
	var own models.Asset
	if err := db.First(&own, 1).Error; err != nil {
		t.Fatal(err)
	}
	if own.Status != enums.AssetStatusDeleted {
		t.Fatalf("own asset status = %v, want deleted", own.Status)
	}
	superAdmin := &dto.AuthPrincipal{
		UserID: 101, UserType: enums.UserTypeEmployee, Status: enums.StatusOk,
		Roles: []string{constants.RoleCodeSuperAdmin},
	}
	if err := services.AssetService.DeleteAsset(2, superAdmin); err != nil {
		t.Fatalf("super_admin lost global asset delete: %v", err)
	}
	if err := db.First(&foreign, 2).Error; err != nil {
		t.Fatal(err)
	}
	if foreign.Status != enums.AssetStatusDeleted {
		t.Fatalf("super_admin deletion status = %v, want deleted", foreign.Status)
	}
}
