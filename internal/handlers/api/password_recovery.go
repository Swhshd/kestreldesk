package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Swhshd/kestreldesk/internal/pkg/dto/request"
	"github.com/Swhshd/kestreldesk/internal/pkg/dto/response"
	"github.com/Swhshd/kestreldesk/internal/pkg/errorsx"
	"github.com/Swhshd/kestreldesk/internal/pkg/httpx"
	"github.com/Swhshd/kestreldesk/internal/pkg/httpx/params"
	"github.com/Swhshd/kestreldesk/internal/pkg/i18nx"
	"github.com/Swhshd/kestreldesk/internal/services"

	"github.com/gin-gonic/gin"
)

const (
	passwordRecoveryIPLimit            = 20
	passwordRecoveryAccountLimit       = 5
	passwordRecoveryLimitWindow        = 15 * time.Minute
	passwordRecoveryMailCooldown       = time.Minute
	passwordRecoveryIPKeyCapacity      = 4096
	passwordRecoveryResetIPKeyCapacity = passwordRecoveryIPKeyCapacity
	passwordRecoveryAccountKeyCapacity = 50000
	passwordRecoverySweepInterval      = time.Minute
	passwordRecoveryWorkTimeout        = 10 * time.Second
	passwordRecoveryWorkCapacity       = 64
	// Match the service floor so limiter suppressions do not expose fast paths.
	passwordRecoveryRequestFloor = 250 * time.Millisecond
)

var passwordRecoveryRequestSlots = make(chan struct{}, passwordRecoveryWorkCapacity)

// PasswordRecoveryOperations is the narrow service contract used by public
// recovery handlers.
type PasswordRecoveryOperations interface {
	RequestResetWithAdmissionCallback(ctx context.Context, identifier, surface, sourceIP string, admit func(int64) (func(), bool), onDelivered func(int64)) error
	ResetPassword(ctx context.Context, token, surface, password, confirmation string) error
}

type passwordRecoveryIPState struct {
	windowStarted time.Time
	count         int
}

type passwordRecoveryAccountState struct {
	windowStarted time.Time
	count         int
	cooldownUntil time.Time
}

// PasswordRecoveryControls applies bounded, process-local abuse controls:
// separate 20-per-IP, 15-minute budgets for forgot requests and reset attempts;
// 5 requests per canonical account per 15 minutes; and a 60-second
// successful-delivery cooldown. New keys fail closed when a hard capacity is
// full; live entries are never evicted to make room.
type PasswordRecoveryControls struct {
	mu sync.Mutex

	requestIPs map[string]passwordRecoveryIPState
	resetIPs   map[string]passwordRecoveryIPState
	accounts   map[int64]passwordRecoveryAccountState

	ipKeyCapacity      int
	resetIPKeyCapacity int
	accountKeyCapacity int
	now                func() time.Time

	lastRequestIPSweep time.Time
	requestIPSwept     bool
	lastResetIPSweep   time.Time
	resetIPSwept       bool
	lastAccountSweep   time.Time
	accountSwept       bool
}

func NewPasswordRecoveryControls() *PasswordRecoveryControls {
	return newPasswordRecoveryControlsWithCapacity(passwordRecoveryIPKeyCapacity, passwordRecoveryResetIPKeyCapacity, passwordRecoveryAccountKeyCapacity, time.Now)
}

func newPasswordRecoveryControlsWithCapacity(ipKeyCapacity, resetIPKeyCapacity, accountKeyCapacity int, now func() time.Time) *PasswordRecoveryControls {
	if ipKeyCapacity < 1 {
		ipKeyCapacity = 1
	}
	if resetIPKeyCapacity < 1 {
		resetIPKeyCapacity = 1
	}
	if accountKeyCapacity < 1 {
		accountKeyCapacity = 1
	}
	if now == nil {
		now = time.Now
	}
	return &PasswordRecoveryControls{
		requestIPs:         make(map[string]passwordRecoveryIPState),
		resetIPs:           make(map[string]passwordRecoveryIPState),
		accounts:           make(map[int64]passwordRecoveryAccountState),
		ipKeyCapacity:      ipKeyCapacity,
		resetIPKeyCapacity: resetIPKeyCapacity,
		accountKeyCapacity: accountKeyCapacity,
		now:                now,
	}
}

// PasswordRecoveryRequestIPLimit is mounted before the request handler so an
// over-budget IP cannot trigger account lookup or password hashing. It keeps the
// generic successful response used by every other forgot-password outcome.
func PasswordRecoveryRequestIPLimit(controls *PasswordRecoveryControls) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		started := time.Now()
		if controls == nil || controls.allowRequestIP(ctx.ClientIP()) {
			ctx.Next()
			return
		}
		writePasswordRecoveryRequestResponse(ctx)
		waitForPasswordRecoveryHandlerFloor(started)
		ctx.Abort()
	}
}

// PasswordRecoveryResetIPLimit applies a separate bounded IP budget to reset
// attempts. A nil controls value disables the limiter with the global setting.
func PasswordRecoveryResetIPLimit(controls *PasswordRecoveryControls) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if controls == nil {
			ctx.Next()
			return
		}
		allowed, retryAfter := controls.allowResetIP(ctx.ClientIP())
		if allowed {
			ctx.Next()
			return
		}
		writePasswordRecoveryRateLimitResponse(ctx, retryAfter)
		ctx.Abort()
	}
}

func PasswordResetRequestHandler(surface string, controls *PasswordRecoveryControls, service PasswordRecoveryOperations) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		started := time.Now()
		req := request.PasswordResetRequest{}
		if err := params.ReadJSON(ctx, &req); err != nil {
			writePasswordRecoveryRequestResponse(ctx)
			return
		}
		defer waitForPasswordRecoveryHandlerFloor(started)

		if service != nil && (surface == services.PasswordRecoverySurfaceDashboard || surface == services.PasswordRecoverySurfaceSupport) {
			admit := func(userID int64) (func(), bool) {
				if controls != nil {
					return controls.reserveAccount(userID)
				}
				return nil, true
			}
			delivered := func(userID int64) {
				if controls != nil {
					controls.markDelivered(userID)
				}
			}
			// Account lookup, token persistence, and mail scheduling can take longer
			// for an eligible account. Run that work outside the HTTP response path
			// so database latency cannot reveal account eligibility.
			schedulePasswordRecoveryRequest(service, req.Identifier, surface, ctx.ClientIP(), admit, delivered)
		}
		writePasswordRecoveryRequestResponse(ctx)
	}
}

func schedulePasswordRecoveryRequest(service PasswordRecoveryOperations, identifier, surface, sourceIP string, admit func(int64) (func(), bool), delivered func(int64)) {
	select {
	case passwordRecoveryRequestSlots <- struct{}{}:
		go func() {
			defer func() { <-passwordRecoveryRequestSlots }()
			workCtx, cancel := context.WithTimeout(context.Background(), passwordRecoveryWorkTimeout)
			defer cancel()
			// Persistence and delivery failures remain internal; the HTTP endpoint
			// has already returned the same generic response for every account.
			_ = service.RequestResetWithAdmissionCallback(workCtx, identifier, surface, sourceIP, admit, delivered)
		}()
	default:
		// Saturation returns the same public response and fails closed by skipping
		// this recovery attempt. The bounded worker count protects the database.
	}
}

func PasswordResetHandler(surface string, service PasswordRecoveryOperations) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		req := request.PasswordResetConfirmRequest{}
		if err := params.ReadJSON(ctx, &req); err != nil {
			httpx.WriteJSON(ctx, errorsx.InvalidTokenI18n("error.passwordReset.invalidToken"))
			return
		}
		if service == nil || (surface != services.PasswordRecoverySurfaceDashboard && surface != services.PasswordRecoverySurfaceSupport) {
			httpx.WriteJSON(ctx, errorsx.BusinessErrorI18n(1, "error.passwordReset.failed"))
			return
		}
		err := service.ResetPassword(ctx, req.Token, surface, req.NewPassword, req.ConfirmPassword)
		if err == nil {
			httpx.WriteJSON(ctx, nil)
			return
		}
		var localized *errorsx.I18nError
		if errors.As(err, &localized) {
			httpx.WriteJSON(ctx, err)
			return
		}
		httpx.WriteJSON(ctx, errorsx.BusinessErrorI18n(1, "error.passwordReset.failed"))
	}
}

func writePasswordRecoveryRequestResponse(ctx *gin.Context) {
	httpx.WriteJSON(ctx, response.PasswordResetRequestResponse{
		Message: i18nx.T(ctx, "auth.passwordReset.requestGeneric"),
	})
}

func waitForPasswordRecoveryHandlerFloor(started time.Time) {
	if remaining := passwordRecoveryRequestFloor - time.Since(started); remaining > 0 {
		time.Sleep(remaining)
	}
}

func (controls *PasswordRecoveryControls) allowRequestIP(ip string) bool {
	if controls == nil {
		return true
	}
	controls.mu.Lock()
	defer controls.mu.Unlock()
	now := controls.now()
	state, exists := controls.requestIPs[ip]
	if exists {
		if now.Before(state.windowStarted.Add(passwordRecoveryLimitWindow)) {
			if state.count >= passwordRecoveryIPLimit {
				return false
			}
			state.count++
			controls.requestIPs[ip] = state
			return true
		}
		controls.requestIPs[ip] = passwordRecoveryIPState{windowStarted: now, count: 1}
		return true
	}
	if len(controls.requestIPs) >= controls.ipKeyCapacity {
		controls.sweepRequestIPsAtCapacityLocked(now)
		if len(controls.requestIPs) >= controls.ipKeyCapacity {
			return false
		}
	}
	controls.requestIPs[ip] = passwordRecoveryIPState{windowStarted: now, count: 1}
	return true
}

func (controls *PasswordRecoveryControls) allowResetIP(ip string) (bool, time.Duration) {
	if controls == nil {
		return true, 0
	}
	controls.mu.Lock()
	defer controls.mu.Unlock()
	now := controls.now()
	state, exists := controls.resetIPs[ip]
	if exists {
		resetAt := state.windowStarted.Add(passwordRecoveryLimitWindow)
		if now.Before(resetAt) {
			if state.count >= passwordRecoveryIPLimit {
				return false, resetAt.Sub(now)
			}
			state.count++
			controls.resetIPs[ip] = state
			return true, 0
		}
		controls.resetIPs[ip] = passwordRecoveryIPState{windowStarted: now, count: 1}
		return true, 0
	}
	if len(controls.resetIPs) >= controls.resetIPKeyCapacity {
		controls.sweepResetIPsAtCapacityLocked(now)
		if len(controls.resetIPs) >= controls.resetIPKeyCapacity {
			return false, passwordRecoverySweepInterval
		}
	}
	controls.resetIPs[ip] = passwordRecoveryIPState{windowStarted: now, count: 1}
	return true, 0
}

func (controls *PasswordRecoveryControls) reserveAccount(userID int64) (func(), bool) {
	if controls == nil {
		return nil, true
	}
	if userID <= 0 {
		return nil, false
	}

	controls.mu.Lock()
	defer controls.mu.Unlock()
	now := controls.now()
	state, exists := controls.accounts[userID]
	if exists {
		if now.Before(state.cooldownUntil) {
			return nil, false
		}
		state.cooldownUntil = time.Time{}
		if !now.Before(state.windowStarted.Add(passwordRecoveryLimitWindow)) {
			state = passwordRecoveryAccountState{windowStarted: now}
		}
	} else {
		if len(controls.accounts) >= controls.accountKeyCapacity {
			controls.sweepAccountsAtCapacityLocked(now)
			if len(controls.accounts) >= controls.accountKeyCapacity {
				return nil, false
			}
		}
		state.windowStarted = now
	}
	if state.count >= passwordRecoveryAccountLimit {
		return nil, false
	}
	state.count++
	controls.accounts[userID] = state
	windowStarted := state.windowStarted
	var releaseOnce sync.Once
	return func() {
		releaseOnce.Do(func() { controls.releaseAccountReservation(userID, windowStarted) })
	}, true
}

func (controls *PasswordRecoveryControls) markDelivered(userID int64) {
	if controls == nil || userID <= 0 {
		return
	}
	controls.mu.Lock()
	defer controls.mu.Unlock()
	now := controls.now()
	state, exists := controls.accounts[userID]
	if !exists {
		if len(controls.accounts) >= controls.accountKeyCapacity {
			controls.sweepAccountsAtCapacityLocked(now)
			if len(controls.accounts) >= controls.accountKeyCapacity {
				return
			}
		}
		state.windowStarted = now
	}
	state.cooldownUntil = now.Add(passwordRecoveryMailCooldown)
	controls.accounts[userID] = state
}

func (controls *PasswordRecoveryControls) sweepRequestIPsAtCapacityLocked(now time.Time) {
	if controls.requestIPSwept && now.Sub(controls.lastRequestIPSweep) < passwordRecoverySweepInterval {
		return
	}
	controls.requestIPSwept = true
	controls.lastRequestIPSweep = now
	for ip, state := range controls.requestIPs {
		if !now.Before(state.windowStarted.Add(passwordRecoveryLimitWindow)) {
			delete(controls.requestIPs, ip)
		}
	}
}

func (controls *PasswordRecoveryControls) sweepResetIPsAtCapacityLocked(now time.Time) {
	if controls.resetIPSwept && now.Sub(controls.lastResetIPSweep) < passwordRecoverySweepInterval {
		return
	}
	controls.resetIPSwept = true
	controls.lastResetIPSweep = now
	for ip, state := range controls.resetIPs {
		if !now.Before(state.windowStarted.Add(passwordRecoveryLimitWindow)) {
			delete(controls.resetIPs, ip)
		}
	}
}

func (controls *PasswordRecoveryControls) sweepAccountsAtCapacityLocked(now time.Time) {
	if controls.accountSwept && now.Sub(controls.lastAccountSweep) < passwordRecoverySweepInterval {
		return
	}
	controls.accountSwept = true
	controls.lastAccountSweep = now
	for userID, state := range controls.accounts {
		windowExpired := !now.Before(state.windowStarted.Add(passwordRecoveryLimitWindow))
		cooldownExpired := state.cooldownUntil.IsZero() || !now.Before(state.cooldownUntil)
		if windowExpired && cooldownExpired {
			delete(controls.accounts, userID)
		}
	}
}

func (controls *PasswordRecoveryControls) releaseAccountReservation(userID int64, windowStarted time.Time) {
	controls.mu.Lock()
	defer controls.mu.Unlock()
	state, ok := controls.accounts[userID]
	if !ok || !state.windowStarted.Equal(windowStarted) || state.count == 0 {
		return
	}
	state.count--
	if state.count == 0 && !controls.now().Before(state.cooldownUntil) {
		delete(controls.accounts, userID)
		return
	}
	controls.accounts[userID] = state
}

func writePasswordRecoveryRateLimitResponse(ctx *gin.Context, retryAfter time.Duration) {
	seconds := int64(retryAfter / time.Second)
	if time.Duration(seconds)*time.Second < retryAfter {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	ctx.Header("Retry-After", strconv.FormatInt(seconds, 10))
	httpx.WriteHttpStatusJSON(ctx, http.StatusTooManyRequests, errorsx.InvalidParamI18n("error.e0354"))
}
