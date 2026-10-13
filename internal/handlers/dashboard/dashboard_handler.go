package dashboard

// This file has been modified for the KestrelDesk project.

import (
	"github.com/Swhshd/kestreldesk/internal/pkg/httpx"
	"github.com/Swhshd/kestreldesk/internal/services"

	"github.com/Swhshd/kestreldesk/internal/pkg/httpx/params"
	"github.com/Swhshd/kestreldesk/internal/pkg/i18nx"

	"github.com/gin-gonic/gin"
)

func DashboardGetOverview(ctx *gin.Context) {
	rangeValue, _ := params.Get(ctx, "range")
	httpx.WriteJSON(ctx, services.DashboardService.GetOverview(rangeValue, i18nx.Locale(ctx)))
}
