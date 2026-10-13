package httpx

// This file has been modified for the KestrelDesk project.

import (
	"github.com/Swhshd/kestreldesk/internal/pkg/httpx/params"
	"github.com/Swhshd/kestreldesk/internal/pkg/openidentity"
	"github.com/Swhshd/kestreldesk/internal/pkg/tracex"

	"github.com/gin-gonic/gin"
	"github.com/mlogclub/simple/common/strs"
)

const (
	ctxKeyExternalUser       = "externalUser"
	ctxKeyVerifiedCustomerID = "verifiedCustomerID"
)

// SetVerifiedCustomerID retains only a positive ID established by authentication.
func SetVerifiedCustomerID(ctx *gin.Context, customerID int64) {
	if ctx == nil {
		return
	}
	if customerID <= 0 {
		customerID = 0
	}
	ctx.Set(ctxKeyVerifiedCustomerID, customerID)
}

// GetVerifiedCustomerID never resolves authority from request parameters or headers.
func GetVerifiedCustomerID(ctx *gin.Context) int64 {
	if ctx == nil {
		return 0
	}
	value, exists := ctx.Get(ctxKeyVerifiedCustomerID)
	customerID, valid := value.(int64)
	if !exists || !valid || customerID <= 0 {
		return 0
	}
	return customerID
}

func SetExternalUser(ctx *gin.Context, ext *openidentity.ExternalUser) {
	ctx.Set(ctxKeyExternalUser, ext)
}

func GetExternalUser(ctx *gin.Context) *openidentity.ExternalUser {
	v, _ := ctx.Get(ctxKeyExternalUser)
	ext, _ := v.(*openidentity.ExternalUser)
	return ext
}

func GetChannelID(ctx *gin.Context) string {
	if channelID := ctx.GetHeader("X-Channel-ID"); strs.IsNotBlank(channelID) {
		return channelID
	}
	if channelID, _ := params.Get(ctx, "channelId"); strs.IsNotBlank(channelID) {
		return channelID
	}
	return ""
}

func GetRequestID(ctx *gin.Context) string {
	if ctx == nil {
		return ""
	}
	if value, ok := ctx.Get(tracex.GinRequestIDKey); ok {
		if requestID, ok := value.(string); ok {
			return tracex.NormalizeRequestID(requestID)
		}
	}
	return tracex.NormalizeRequestID(ctx.GetHeader(tracex.RequestIDHeader))
}
