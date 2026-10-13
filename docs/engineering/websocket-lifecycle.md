# WebSocket lifecycle safety

## Enqueue and close boundary

[ClientSession](../../internal/services/ws_realtime_types.go) contains an atomic closed flag, send mutex and close-once guard. Enqueue rejects nil/closed sessions, acquires the send lock, rechecks terminal state and employee delivery eligibility, then attempts a bounded nonblocking queue send. A preliminary open check is insufficient: close can win before enqueue obtains the lock.

The [session manager](../../internal/services/ws_connection_manager.go) synchronizes terminal close against enqueue, removes session/topic registration and prevents stale activation/subscription from restoring a closed connection. Close is terminal and idempotent. Repeated pump cleanup and lifecycle callbacks may request close without reopening the queue or leaving subscriptions active.

Queue-full rejection is separate from successful delivery. The service can terminate an unhealthy session. Successfully enqueued bytes do not alone prove that a browser received them; the writer can subsequently encounter a network error. This change does not promise exactly-once delivery or unlimited throughput.

## Revocation and stale operations

Employee sessions carry persisted login-session identity and a lifecycle deadline. The actual enqueue boundary rechecks eligibility after lock waits. An expired rejection is closed outside the send lock, avoiding recursive lock acquisition. Pending registrations are revalidated before becoming active. Stale timer callbacks and close/subscribe operations cannot resurrect a terminal session.

## Regression evidence

The PR #4 tests live in [internal/services/ws_session_concurrency_test.go](../../internal/services/ws_session_concurrency_test.go):

- `TestClientSessionEnqueueCloseLinearization`
- `TestWsSessionConcurrentEnqueueClose`
- `TestWsSessionCloseRejectsStaleOperations`

Run from the repository root:

```powershell
go test -count=1 -tags dev ./internal/services -run '^(TestClientSessionEnqueueCloseLinearization|TestWsSessionConcurrentEnqueueClose|TestWsSessionCloseRejectsStaleOperations)$'
```

Final acceptance: **normal focused concurrency regressions PASS; full Go tests PASS; go vet PASS**. The focused Windows race-enabled test process exits `0xc0000139`; an unrelated race probe previously reproduced the environment failure. Classification: **RACE_ENVIRONMENT_BLOCKED; CAN_CLAIM_RACE_PASS = NO**.

The same test selection with `-race` should be rerun in a working race-runtime environment before claiming detector coverage. No race toolchain repair or race pass is included in this release.

> This file has been modified for the KestrelDesk project.
