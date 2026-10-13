# Eight-minute Go backend interview demo

## Project Positioning

KestrelDesk is a Go-based AI support platform. This walkthrough demonstrates customer identity isolation, realtime authorization, WebSocket lifecycle safety, and retrieval-augmented answers.

## Browser roles: keep these separate

| Browser | Role | Page |
| --- | --- | --- |
| Edge normal window | Administrator | `http://127.0.0.1:8083/dashboard` |
| Separate browser profile | Employee / 客服二号 | `http://127.0.0.1:8083/dashboard/conversations` |
| Edge InPrivate window | Guest A | Customer URL below |
| Chrome incognito, separate profile or another browser | Guest B | Same customer URL / same externalId |

Two Edge InPrivate windows can share an InPrivate session; opening another window alone does not reliably create Guest B. Use independent storage contexts. Do not sign administrator and employee into tabs sharing the same profile: employee authentication state can replace the admin session.

Customer URL (replace only `<channelId>` with the existing local demo channel code):

```text
http://127.0.0.1:8083/support/chat/?channelId=<channelId>&externalId=kestrel-guest-demo
```

Customer guest chat does not require an employee username/password. Keep its possession proof private. Use the existing privately provisioned employee/admin credentials; this guide contains no passwords or tokens. `/support/demo` is the SDK demonstration entry. Do not test authenticated `source=user` or open signing-secret settings.

## Script

| Time | Action and exact browser | Evidence |
| --- | --- | --- |
| 0:00–0:45 | Present product positioning and the architecture diagram | Service architecture and security boundaries |
| 0:45–1:45 | Guest A: send “你们的客服工作时间是什么？” | Grounded service hours; compare with the synthetic policy |
| 1:45–2:30 | Guest A: send “没有订单信息时，能保证全额退款吗？” | Knowledge insufficiency, no invented refund guarantee |
| 2:30–3:15 | Employee workspace: keep open; admin: inspect the matching run in Agent audit | Retrieval evidence and runtime status; do not claim takeover merely because a conversation is visible |
| 3:15–4:15 | Guest A: send “访客A连续性测试”; refresh the same tab without clearing storage | A's history remains while its valid proof is available |
| 4:15–5:15 | Independent Guest B browser: paste the identical URL, send “访客B隔离测试” | B has fresh history and cannot see A's marker; admin monitor can show distinct customer records |
| 5:15–6:30 | Guest A: send “我要转人工客服，请帮我处理一个问题”; employee: select A only and reply “这是给访客A的人工回复” if authorized | A receives its reply; B must not receive A's messages. If capacity/schedule/assignment prevents this, record the limitation instead of changing configuration |
| 6:30–8:00 | Explain CustomerID boundary, epoch guards, RBAC/session revocation, close/enqueue tests and provider-safe aliases | Cite code/tests and the race limitation |

After refresh, confirm A's original marker remains. In B, confirm the marker and any later A-only reply never appear, including after refreshing B. Do not copy A's proof into B: that changes the experiment from isolation without proof to possession.

## Accepted status and stopping points

- RAG, knowledge-insufficiency behavior, Guest Recovery A2 and customer WS isolation: verified in the local demo environment.
- Human handoff: **PARTIAL**. Employee messages have been observed, but dispatch, capacity, routing and lifecycle still need full validation.
- Attachment AssetID/direct URL authorization: **DEFERRED**.
- Windows race detector: **RACE_ENVIRONMENT_BLOCKED**; normal concurrency regressions pass.
- The guide uses a synthetic [employee-workspace capture](../../screenshots/kestreldesk/employee-workspace.jpg). Customer RAG/refund and support-home image captures are not included; retrieval behavior is described in the [pipeline note](rag-pipeline.md). Guest A/B screenshots remain manual follow-up.

Use synthetic conversations only. Capture sanitized visible chat or workspace content; never capture channel/model settings, customer-session proofs, cookies, credentials or private customer details. Close only the synthetic test conversation when appropriate; do not clear the database to tidy a demo.

> This file has been modified for the KestrelDesk project.
