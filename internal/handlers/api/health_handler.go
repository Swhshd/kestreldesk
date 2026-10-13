package api

// This file has been modified for the KestrelDesk project.

import (
	"github.com/Swhshd/kestreldesk/internal/pkg/httpx"

	"github.com/gin-gonic/gin"
)

type healthResponse struct {
	Status string `json:"status"`
}

func Health(ctx *gin.Context) {
	httpx.WriteJSON(ctx, &healthResponse{Status: "ok"})
}
