package api

// This file has been modified for the KestrelDesk project.

import (
	"github.com/Swhshd/kestreldesk/internal/pkg/httpx"
	"github.com/Swhshd/kestreldesk/internal/services"

	"github.com/gin-gonic/gin"
)

func SupportConfigGetConfig(ctx *gin.Context) {
	httpx.WriteJSON(ctx, services.SystemConfigService.GetPublicSupportConfig())
}
