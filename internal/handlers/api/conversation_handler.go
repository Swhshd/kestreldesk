package api

// This file has been modified for the KestrelDesk project.

import (
	"github.com/Swhshd/kestreldesk/internal/builders"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/request"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
	"github.com/Swhshd/kestreldesk/internal/pkg/httpx"
	"github.com/Swhshd/kestreldesk/internal/pkg/i18nx"
	"github.com/Swhshd/kestreldesk/internal/services"

	"github.com/Swhshd/kestreldesk/internal/pkg/httpx/params"

	"github.com/gin-gonic/gin"
)

func ConversationGetBy(ctx *gin.Context) {
	id, ok := httpx.GetPathInt64(ctx, "id")
	if !ok {
		return
	}
	if services.ChannelService.GetEnabledChannel(ctx) == nil {
		httpx.WriteJSON(ctx, httpx.JsonErrorMsg(ctx, "error.e0211"))
		return
	}
	external := httpx.GetExternalUser(ctx)
	customerID := httpx.GetVerifiedCustomerID(ctx)
	if external == nil || customerID <= 0 {
		httpx.WriteJSON(ctx, httpx.JsonErrorMsg(ctx, "error.e0150"))
		return
	}

	item := services.ConversationService.Get(id)
	if item == nil {
		httpx.WriteJSON(ctx, httpx.JsonErrorMsg(ctx, "error.e0116"))
		return
	}
	if !services.ConversationService.IsVerifiedCustomerConversationOwner(item, customerID) {
		httpx.WriteJSON(ctx, httpx.JsonErrorMsg(ctx, "error.e0222"))
		return
	}

	detail := response.ConversationDetailResponse{
		ConversationResponse: builders.BuildConversationWithLocale(item, i18nx.Locale(ctx)),
		Participants:         builders.BuildParticipantResponses(id),
	}
	httpx.WriteJSON(ctx, detail)
}

func ConversationPostCreate_or_match(ctx *gin.Context) {
	channel := services.ChannelService.GetEnabledChannel(ctx)
	if channel == nil {
		httpx.WriteJSON(ctx, httpx.JsonErrorMsg(ctx, "error.e0211"))
		return
	}
	external := httpx.GetExternalUser(ctx)
	customerID := httpx.GetVerifiedCustomerID(ctx)
	if external == nil || customerID <= 0 {
		httpx.WriteJSON(ctx, httpx.JsonErrorMsg(ctx, "error.e0150"))
		return
	}

	item, err := services.ConversationService.CreateForCustomer(customerID, *external, channel.ID, channel.AIAgentID)
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, builders.BuildConversationWithLocale(item, i18nx.Locale(ctx)))
}

func ConversationPostClose(ctx *gin.Context) {
	if services.ChannelService.GetEnabledChannel(ctx) == nil {
		httpx.WriteJSON(ctx, httpx.JsonErrorMsg(ctx, "error.e0211"))
		return
	}
	external := httpx.GetExternalUser(ctx)
	customerID := httpx.GetVerifiedCustomerID(ctx)
	if external == nil || customerID <= 0 {
		httpx.WriteJSON(ctx, httpx.JsonErrorMsg(ctx, "error.e0150"))
		return
	}

	req := request.CloseConversationRequest{}
	if err := params.ReadJSON(ctx, &req); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	if err := services.ConversationService.CloseVerifiedCustomerConversation(req.ConversationID, customerID); err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, nil)
}
