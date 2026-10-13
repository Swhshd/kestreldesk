# Guest identity and continuity

## Threat model

A client can choose or learn externalId, including a hint such as `guest:X`. Knowing that hint is not possession of Customer A. Neither a URL parameter, a display name nor a matching external identity string may choose another customer's conversation scope.

| Field | Role | Authority |
| --- | --- | --- |
| externalId | Client-controlled identity hint | None by itself |
| CustomerSessionToken | Server-signed continuity / possession proof | Only after signature, claims, binding and customer checks |
| CustomerID | Verified server-side identity | REST authorization and realtime routing boundary |

Tokens are bearer proofs: protect their storage and transport. This model is not an encryption claim, nor protection against a compromised browser or stolen proof.

## Exchange state machine

[CustomerSessionService.Exchange](../../internal/services/customer_session_service.go) classifies guest proof before creating or continuing a customer. Non-guest authenticated identity is a separate path and is not part of this guest demo.

| Input | Result |
| --- | --- |
| No proof | Fresh CustomerID, even if the same externalId already exists |
| Valid proof with matching channel/source/hint and valid customer association | Exact signed CustomerID continuation |
| Authentic expiry-only proof | Fresh CustomerID after signature, binding, customer/identity and non-expiry checks; no recovery by hint |
| Malformed or tampered proof | Reject; no downgrade to a fresh guest |
| Wrong channel, source or identity hint | Reject, even if expired |
| Missing/deleted customer or missing exact identity association | Reject |
| DB error, missing signing configuration or internal failure | Fail closed; do not treat an operational failure as missing proof |

`parsePossessionProof` verifies the signature before trusting claims. Token type, positive IDs, channel code, identity key and expiry presence are checked. `classifyGuestProof` verifies channel and identity binding and the persisted association, then non-expiry claims, before classifying expiry. Thus “expired” is not a catch-all for invalid tokens.

The exchange signs a new proof after a successful transaction. The fresh path creates a new customer instead of locating an old one by hint. Proof continuation loads the exact primary-key customer. The exact identity lookup includes CustomerID, source and external identity, so duplicate guest external IDs can coexist without selecting the first matching customer.

## REST and WebSocket authority

Customer middleware retains the verified CustomerID in request context. Conversation operations compare ownership to that verified ID. The WebSocket service installs the verified ID on the session and uses it for default customer topics, conversation subscription checks and publication routing. Client query fields and duplicated externalId values cannot select Customer A's scope for Customer B.

An expired proof's fresh-exchange classification does not authorize REST or a socket as the expired customer: ordinary request verification rejects expired proofs. A successful exchange is needed first.

## Frontend identity epoch

[im-session-scope.ts](../../web/lib/api/im-session-scope.ts) separates:

- Attempt generation: a superseded bootstrap attempt cannot install a later response.
- Active identity epoch: switching CustomerID/channel or invalidating a scope makes captured scopes stale.
- Same-customer refresh: preserves continuity without unnecessarily resetting the epoch.

REST refresh, store updates and realtime callbacks must belong to the active scope. Even returning to a formerly active CustomerID cannot make a callback from its departed epoch current. These guards prevent stale UI/storage updates; server checks remain the authorization boundary.

## Existing regression evidence

- [Session service tests](../../internal/services/customer_session_service_test.go): no-proof isolation, valid continuation and proof classification precedence.
- [Exact lookup tests](../../internal/repositories/customer_session_lookup_test.go): duplicate guest identities and customer-bound association.
- [Customer REST tests](../../internal/handlers/api/): Customer B cannot operate on Customer A's conversation.
- [Realtime tests](../../internal/services/): customer conversation ownership, verified-ID routing and external-ID collision isolation.
- [Frontend session tests](../../web/lib/api/im-session.test.mjs), [store scope tests](../../web/lib/stores/support-chat-session.test.mjs) and [realtime tests](../../web/lib/stores/support-chat-realtime.test.mjs): stale callback isolation and continuity.

The final acceptance focused regressions pass. Visual Guest Recovery A2 and customer WS isolation were observed. Do not disclose proofs, session headers or private customer data when demonstrating this.

## Deferred attachment boundary

**Attachment AssetID/direct URL authorization = DEFERRED / OUT OF SCOPE.** Conversation ownership checks are not evidence that all independently addressable attachment URLs or AssetIDs have equivalent access controls. This release does not fix or claim to fix that finding.

> This file has been modified for the KestrelDesk project.
