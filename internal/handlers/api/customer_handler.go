package api

// This file has been modified for the KestrelDesk project.

import (
	"github.com/Swhshd/kestreldesk/internal/pkg/httpx"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"github.com/Swhshd/kestreldesk/internal/services"

	"github.com/gin-gonic/gin"
)

func CustomerPostSession_exchange(ctx *gin.Context) {
	channel := services.ChannelService.GetEnabledChannel(ctx)
	if channel == nil {
		httpx.WriteJSON(ctx, httpx.JsonErrorMsg(ctx, "error.e0209"))
		return
	}
	externalUser, err := openidentity.GetExternalUser(ctx, services.ChannelService.GetUserTokenSecret(channel))
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	resp, err := services.CustomerSessionService.Exchange(channel, *externalUser, ctx.GetHeader("X-Customer-Session-Token"))
	if err != nil {
		httpx.WriteJSON(ctx, err)
		return
	}
	httpx.WriteJSON(ctx, resp)
}
