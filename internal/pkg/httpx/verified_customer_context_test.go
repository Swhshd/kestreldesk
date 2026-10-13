package httpx

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestVerifiedCustomerContext(t *testing.T) {
	SetVerifiedCustomerID(nil, 41)
	if GetVerifiedCustomerID(nil) != 0 {
		t.Fatal("nil context accepted")
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("GET", "/?customerId=41", nil)
	ctx.Request.Header.Set("X-Customer-ID", "41")
	if GetVerifiedCustomerID(ctx) != 0 {
		t.Fatal("request input became authority")
	}
	for _, raw := range []any{nil, "41", 41, float64(41), int64(0), int64(-1)} {
		ctx.Set("verifiedCustomerID", raw)
		if GetVerifiedCustomerID(ctx) != 0 {
			t.Errorf("untrusted context value %T accepted", raw)
		}
	}
	SetVerifiedCustomerID(ctx, 42)
	if GetVerifiedCustomerID(ctx) != 42 {
		t.Fatal("positive verified ID lost")
	}
	for _, id := range []int64{0, -1} {
		SetVerifiedCustomerID(ctx, 42)
		SetVerifiedCustomerID(ctx, id)
		if GetVerifiedCustomerID(ctx) != 0 {
			t.Error("invalid setter retained stale ID")
		}
	}
}
