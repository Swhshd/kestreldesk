package dashboard

// This file has been modified for the KestrelDesk project.

import (
	"github.com/Swhshd/kestreldesk/internal/builders"
	"github.com/Swhshd/kestreldesk/internal/pkg/constants"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/request"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/httpx"
	"github.com/Swhshd/kestreldesk/internal/services"
	"strings"

	"github.com/Swhshd/kestreldesk/internal/pkg/httpx/params"

	"github.com/gin-gonic/gin"
	"github.com/mlogclub/simple/web"
)

func AssetAnyList(ctx *gin.Context) {
	operator, err := services.AuthService.RequirePermission(ctx, constants.PermissionAssetView)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}

	cnd := params.NewPagedSqlCnd(ctx,
		params.QueryFilter{ParamName: "provider"},
		params.QueryFilter{ParamName: "status"},
		params.QueryFilter{ParamName: "createUserId"},
		params.QueryFilter{ParamName: "filename", Op: params.Like},
	).Desc("id")
	if strings.TrimSpace(ctx.Query("status")) == "" {
		cnd = cnd.Eq("status", enums.AssetStatusSuccess)
	}

	list, paging, err := services.AssetService.FindPageForDashboard(operator, cnd)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	results := make([]response.AssetResponse, 0, len(list))
	for _, item := range list {
		results = append(results, builders.BuildAsset(&item))
	}
	httpx.WriteJSON(ctx, &web.PageResult{Results: results, Page: paging})
}

func AssetGetBy(ctx *gin.Context) {
	id, ok := httpx.GetPathInt64(ctx, "id")
	if !ok {
		return
	}
	operator, err := services.AuthService.RequirePermission(ctx, constants.PermissionAssetView)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	item, err := services.AssetService.GetForDashboard(operator, id)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	if item == nil {
		httpx.WriteJSON(ctx, httpx.JsonErrorMsg(ctx, "error.e0214"))
		return
	}
	httpx.WriteJSON(ctx, builders.BuildAsset(item))
}

func AssetPostCreate(ctx *gin.Context) {
	operator, err := services.AuthService.RequirePermission(ctx, constants.PermissionAssetCreate)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}

	req := request.CreateAssetRequest{}
	if err := params.ReadForm(ctx, &req); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	header, err := ctx.FormFile("file")
	if err != nil {
		httpx.WriteJSON(ctx, httpx.JsonErrorMsg(ctx, "error.e0323"))
		return
	}
	item, err := services.AssetService.UploadFile(header, req.Prefix, operator)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, builders.BuildAsset(item))
}

func AssetPostDelete(ctx *gin.Context) {
	operator, err := services.AuthService.RequirePermission(ctx, constants.PermissionAssetDelete)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}

	req := request.DeleteAssetRequest{}
	if err := params.ReadJSON(ctx, &req); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	if err := services.AssetService.DeleteAsset(req.ID, operator); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, nil)
}
