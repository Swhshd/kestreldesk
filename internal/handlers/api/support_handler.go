package api

// This file has been modified for the KestrelDesk project.

import (
	"github.com/Swhshd/kestreldesk/internal/builders"
	"github.com/Swhshd/kestreldesk/internal/models"
	"github.com/Swhshd/kestreldesk/internal/pkg/config"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/request"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
	"github.com/Swhshd/kestreldesk/internal/pkg/enums"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/pkg/httpx"
	"github.com/Swhshd/kestreldesk/internal/pkg/httpx/params"
	"github.com/Swhshd/kestreldesk/internal/repositories"
	"github.com/Swhshd/kestreldesk/internal/services"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/mlogclub/simple/sqls"
	"github.com/mlogclub/simple/web"
	"github.com/spf13/cast"
	"gorm.io/gorm"
)

func SupportAuthPostRegister(ctx *gin.Context) {
	cfg := config.Current()
	if !cfg.Auth.IsPasswordLoginEnabled() {
		httpx.WriteJSON(ctx, errorsx.ForbiddenI18n("error.auth.passwordLoginDisabled"))
		return
	}

	req := request.SupportCustomerRegisterRequest{}
	if err := params.ReadJSON(ctx, &req); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	ret, err := services.SupportService.RegisterUser(req, cfg.Auth, ctx.ClientIP(), ctx.GetHeader("User-Agent"))
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, ret)
}

func SupportAuthPostLogin(ctx *gin.Context) {
	cfg := config.Current()
	if !cfg.Auth.IsPasswordLoginEnabled() {
		httpx.WriteJSON(ctx, errorsx.ForbiddenI18n("error.auth.passwordLoginDisabled"))
		return
	}
	req := request.LoginRequest{}
	if err := params.ReadJSON(ctx, &req); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	ret, err := services.AuthService.LoginSupportCustomer(req, cfg.Auth, ctx.ClientIP(), ctx.GetHeader("User-Agent"))
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, ret)
}

// SupportAuthPasswordResetRequestHandler fixes recovery requests to the
// registered-customer Support surface regardless of any caller-supplied field.
func SupportAuthPasswordResetRequestHandler(controls *PasswordRecoveryControls, service PasswordRecoveryOperations) gin.HandlerFunc {
	return PasswordResetRequestHandler(services.PasswordRecoverySurfaceSupport, controls, service)
}

// SupportAuthPasswordResetHandler fixes reset-token consumption to the Support
// surface selected by the registered route.
func SupportAuthPasswordResetHandler(service PasswordRecoveryOperations) gin.HandlerFunc {
	return PasswordResetHandler(services.PasswordRecoverySurfaceSupport, service)
}

func SupportGetMe(ctx *gin.Context) {
	principal, err := services.SupportService.RequireSupportCustomer(ctx)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	user := supportUser(principal.UserID)
	email := ""
	if user != nil && user.Email != nil {
		email = *user.Email
	}
	httpx.WriteJSON(ctx, response.SupportUserResponse{ID: principal.UserID, Name: supportPrincipalDisplayName(principal.UserID), Email: email, UserType: principal.UserType})
}

func SupportProfilePostUpdate(ctx *gin.Context) {
	if _, err := services.SupportService.RequireSupportCustomer(ctx); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	req := request.UpdateProfileRequest{}
	if err := params.ReadJSON(ctx, &req); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	ret, err := services.AuthService.UpdateProfile(ctx, req)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, ret)
}

func SupportProfilePostAvatarUpload(ctx *gin.Context) {
	principal, err := services.SupportService.RequireSupportCustomer(ctx)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	header, err := ctx.FormFile("file")
	if err != nil {
		httpx.WriteJSON(ctx, httpx.JsonErrorMsg(ctx, "error.e0323"))
		return
	}
	if !strings.HasPrefix(strings.ToLower(header.Header.Get("Content-Type")), "image/") {
		httpx.WriteJSON(ctx, httpx.JsonErrorMsg(ctx, "error.e0090"))
		return
	}
	item, err := services.AssetService.UploadFile(header, "avatars", principal)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, builders.BuildAsset(item))
}

func DocPageAnyList(ctx *gin.Context) {
	cnd := params.NewPagedSqlCnd(ctx,
		params.QueryFilter{ParamName: "parentId"},
		params.QueryFilter{ParamName: "title", Op: params.Like},
	).Eq("status", enums.DocPageStatusPublished).Asc("sort_no").Desc("id")
	if keyword := strings.TrimSpace(ctx.Query("keyword")); keyword != "" {
		pattern := "%" + keyword + "%"
		cnd.Where("(title LIKE ? OR summary LIKE ? OR content LIKE ? OR tags_json LIKE ? OR slug LIKE ?)", pattern, pattern, pattern, pattern, pattern)
	}
	list, paging := repositories.DocPageRepository.FindPageByCnd(sqls.DB(), cnd)
	results := buildDocPageList(list, false)
	httpx.WriteJSON(ctx, &web.PageResult{Results: results, Page: paging})
}

func DocPageGetNavigation(ctx *gin.Context) {
	list := services.SupportService.FindPublicDocNavigation()
	httpx.WriteJSON(ctx, builders.BuildDocPageNavigationTree(list))
}

func DocPageGetBy(ctx *gin.Context) {
	id, ok := httpx.GetPathInt64(ctx, "id")
	if !ok {
		return
	}
	item := repositories.DocPageRepository.Get(sqls.DB(), id)
	if item == nil || item.Status != enums.DocPageStatusPublished {
		httpx.WriteJSON(ctx, httpx.JsonErrorMsg(ctx, "error.notFound"))
		return
	}
	_ = repositories.DocPageRepository.UpdateColumn(sqls.DB(), item.ID, "view_count", gorm.Expr("view_count + ?", 1))
	httpx.WriteJSON(ctx, builders.BuildDocPage(item, true))
}

func DocPagePostFeedback(ctx *gin.Context) {
	req := request.DocPageFeedbackRequest{}
	if err := params.ReadJSON(ctx, &req); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, services.SupportService.FeedbackDocPage(req))
}

func CategoryAnyList(ctx *gin.Context) {
	list := repositories.CategoryRepository.Find(sqls.DB(), sqls.NewCnd().Eq("status", enums.StatusOk).Asc("sort_no").Desc("id"))
	httpx.WriteJSON(ctx, builders.BuildPostCategories(list))
}

func PostAnyList(ctx *gin.Context) {
	cursor, _ := params.GetInt64(ctx, "cursor")
	limit, _ := params.GetInt(ctx, "limit")
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	cnd := params.NewSqlCnd(ctx,
		params.QueryFilter{ParamName: "categoryId"},
		params.QueryFilter{ParamName: "userId"},
		params.QueryFilter{ParamName: "status"},
		params.QueryFilter{ParamName: "title", Op: params.Like},
	).Where("status NOT IN ?", []enums.PostStatus{enums.PostStatusHidden, enums.PostStatusDeleted}).Desc("id").Limit(limit + 1)
	if cursor > 0 {
		cnd.Lt("id", cursor)
	}
	list := repositories.PostRepository.Find(sqls.DB(), cnd)
	hasMore := len(list) > limit
	if hasMore {
		list = list[:limit]
	}
	nextCursor := ""
	if hasMore && len(list) > 0 {
		nextCursor = cast.ToString(list[len(list)-1].ID)
	}
	httpx.WriteJSON(ctx, httpx.CursorData(buildPostList(list), nextCursor, hasMore))
}

func PostGetBy(ctx *gin.Context) {
	id, ok := httpx.GetPathInt64(ctx, "id")
	if !ok {
		return
	}
	post := repositories.PostRepository.Get(sqls.DB(), id)
	if post == nil || post.Status == enums.PostStatusHidden || post.Status == enums.PostStatusDeleted {
		httpx.WriteJSON(ctx, httpx.JsonErrorMsg(ctx, "error.notFound"))
		return
	}
	_ = repositories.PostRepository.UpdateColumn(sqls.DB(), post.ID, "view_count", gorm.Expr("view_count + ?", 1))
	comments, err := services.SupportService.ListPostComments(id, 0, "default", 1, 20)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, response.PostDetailResponse{Post: *builders.BuildPost(post, supportCategoryName(post.CategoryID), supportUser(post.UserID)), Comments: buildCommentListWithReplies(comments.Comments, comments.Replies)})
}

func PostPostCreate(ctx *gin.Context) {
	principal, err := services.SupportService.RequireSupportCustomer(ctx)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	req := request.CreatePostRequest{}
	if err := params.ReadJSON(ctx, &req); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	item, err := services.SupportService.CreatePost(req, principal)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, builders.BuildPost(item, supportCategoryName(item.CategoryID), supportUser(principal.UserID)))
}

func PostPostUpdate(ctx *gin.Context) {
	principal, err := services.SupportService.RequireSupportCustomer(ctx)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	req := request.UpdatePostRequest{}
	if err := params.ReadJSON(ctx, &req); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, services.SupportService.UpdatePost(req, principal))
}

func PostPostDelete(ctx *gin.Context) {
	principal, err := services.SupportService.RequireSupportCustomer(ctx)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	req := request.IDRequest{}
	if err := params.ReadJSON(ctx, &req); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, services.SupportService.DeletePost(req.ID, principal))
}

func PostPostAcceptComment(ctx *gin.Context) {
	principal, err := services.SupportService.RequireSupportCustomer(ctx)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	req := request.AcceptCommentRequest{}
	if err := params.ReadJSON(ctx, &req); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, services.SupportService.AcceptComment(req, principal, nil))
}

func CommentPostCreate(ctx *gin.Context) {
	principal, err := services.SupportService.RequireSupportCommunityCommentActor(ctx)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	req := request.CreateCommentRequest{}
	if err := params.ReadJSON(ctx, &req); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	item, err := services.SupportService.CreateCustomerComment(req, principal)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, builders.BuildComment(item, supportPrincipalDisplayName(principal.UserID)))
}

func CommentAnyList(ctx *gin.Context) {
	postID, _ := params.GetInt64(ctx, "postId")
	parentID, _ := params.GetInt64(ctx, "parentId")
	page, _ := params.GetInt(ctx, "page")
	limit, _ := params.GetInt(ctx, "limit")
	sort, _ := params.Get(ctx, "sort")
	result, err := services.SupportService.ListPostComments(postID, parentID, sort, page, limit)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, &web.PageResult{Results: buildCommentListWithReplies(result.Comments, result.Replies), Page: result.Paging})
}

func CommentPostUpdate(ctx *gin.Context) {
	principal, err := services.SupportService.RequireSupportCommunityCommentActor(ctx)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	req := request.UpdateCommentRequest{}
	if err := params.ReadJSON(ctx, &req); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, services.SupportService.UpdateComment(req, principal))
}

func CommentPostDelete(ctx *gin.Context) {
	principal, err := services.SupportService.RequireSupportCommunityCommentActor(ctx)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	req := request.IDRequest{}
	if err := params.ReadJSON(ctx, &req); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, services.SupportService.DeleteComment(req.ID, principal))
}

func CommentPostReport(ctx *gin.Context) {
	principal, err := services.SupportService.RequireSupportCustomer(ctx)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	req := request.ReportCommentRequest{}
	if err := params.ReadJSON(ctx, &req); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, services.SupportService.ReportComment(req, principal))
}

func ReactionPostToggle(ctx *gin.Context) {
	principal, err := services.SupportService.RequireSupportCustomer(ctx)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	req := request.ReactionRequest{}
	if err := params.ReadJSON(ctx, &req); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, services.SupportService.ToggleReaction(req.TargetType, req.TargetID, req.ReactionType, principal))
}

func buildDocPageList(list []models.DocPage, includeContent bool) []response.DocPageResponse {
	results := make([]response.DocPageResponse, 0, len(list))
	for _, item := range list {
		if resp := builders.BuildDocPage(&item, includeContent); resp != nil {
			results = append(results, *resp)
		}
	}
	return results
}

func buildPostList(list []models.Post) []response.PostResponse {
	results := make([]response.PostResponse, 0, len(list))
	for _, item := range list {
		if resp := builders.BuildPost(&item, supportCategoryName(item.CategoryID), supportUser(item.UserID)); resp != nil {
			results = append(results, *resp)
		}
	}
	return results
}

func buildCommentList(list []models.Comment) []response.CommentResponse {
	return buildCommentListWithReplies(list, nil)
}

func buildCommentListWithReplies(list []models.Comment, replies map[int64][]models.Comment) []response.CommentResponse {
	results := make([]response.CommentResponse, 0, len(list))
	for _, item := range list {
		if resp := builders.BuildComment(&item, supportCommentAuthorName(item)); resp != nil {
			if len(replies[item.ID]) > 0 {
				resp.Replies = buildCommentListWithReplies(replies[item.ID], nil)
			}
			results = append(results, *resp)
		}
	}
	return results
}

func supportCategoryName(id int64) string {
	item := repositories.CategoryRepository.Get(sqls.DB(), id)
	if item == nil {
		return ""
	}
	return item.Name
}

func supportUser(id int64) *models.User {
	return repositories.UserRepository.Get(sqls.DB(), id)
}

func supportPrincipalDisplayName(id int64) string {
	user := supportUser(id)
	if user == nil {
		return ""
	}
	if user.Nickname != "" {
		return user.Nickname
	}
	return user.Username
}

func supportCommentAuthorName(item models.Comment) string {
	user := repositories.UserRepository.Get(sqls.DB(), item.AuthorID)
	if user == nil {
		return ""
	}
	if user.Nickname != "" {
		return user.Nickname
	}
	return user.Username
}
