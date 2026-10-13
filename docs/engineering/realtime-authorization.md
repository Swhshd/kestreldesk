# Realtime authorization

## Server-side authority

Employee realtime access uses the effective server-side RBAC principal, not frontend role labels or requested topic names. [ws_service.go](../../internal/services/ws_service.go) filters subscriptions and protects delivery. Employee defaults and conversation visibility derive from permission decisions; customer defaults derive from verified CustomerID. Client subscription input is normalized and checked before registration.

Authentication at the WebSocket handshake **does not mean authorization forever**. A socket must remain bound to an eligible employee login session and the relevant effective permissions. LoginSessionID and EmployeeID are captured from authenticated server context, not arbitrary client input.

## Employee session lifecycle

Activation proceeds through pending registration, server session revalidation, lifecycle timer installation and activation with a fresh snapshot. If revocation or expiry wins that sequence, the socket cannot activate afterward. Lifecycle deadlines include login session expiry and applicable authorization changes. Delivery checks cover the actual send, including time spent waiting for the send lock.

Logout/revocation invalidates the exact login session; permission changes trigger realtime invalidation. Closing removes subscriptions and stops further protected enqueue. Tests distinguish one revoked session from other eligible sessions; revoking a session is not a blanket permission grant or a client-managed offline flag.

## Customer routing

The signed proof's verified CustomerID is installed on the customer socket. Conversation subscriptions require matching `conversation.CustomerID`. Publication uses server customer identity; externalId collisions cannot fan out into another customer's scope. Employee and customer topic namespaces remain internal compatibility identifiers.

## Evidence and limits

Existing `TestRBACEffectivePermissionFiltering`, `TestEmployeeLogoutInvalidatesExactSession`, `TestEmployeeSessionRuntimeAcceptance`, `TestEmployeeSessionRevokeIsolation` and `TestEmployeeTimerCallbackClosesInstalledSession` pass in focused acceptance. Customer ownership, routing collision and verified query-authority tests also pass. See the [session service](../../internal/services/login_session_service.go), [socket tests](../../internal/services/ws_session_concurrency_test.go) and [guest model](guest-identity-security.md).

This documents implemented checks and local regressions. It does not assert arbitrary topic access is safe, future permission changes are harmless, or all attachment surfaces are authorized. The deferred attachment finding remains open.

> This file has been modified for the KestrelDesk project.
