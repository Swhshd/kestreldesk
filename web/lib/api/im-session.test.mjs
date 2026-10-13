import assert from "node:assert/strict"
import test from "node:test"
import { deferred, loadImHarness, loadSupportChatHarness } from "../test/customer-session-harness.mjs"

const now = Date.parse("2026-10-07T00:00:00Z")
const config = () => ({ channelId: "channel-C", externalId: "hint-X", apiBaseUrl: "https://api.test" })
const session = (overrides = {}) => ({
  customerSessionToken: "synthetic-proof-A", expiresAt: "2026-10-07T01:00:00Z",
  identityKey: "guest:hint-X", customer: { id: 101, name: "A" }, channelId: "channel-C", ...overrides,
})
const response = (overrides = {}) => {
  const { channelId: _channelId, ...value } = session(overrides)
  return value
}
const options = (overrides = {}) => ({
  config: config(), session: session(), request: async () => response(), now, scopeControl: "normal", ...overrides,
})
// Sensitivity control only changes observed transport in tests, never production.
const selectHeaders = process.env.CUSTOMER_SESSION_HEADER_CONTROL === "remove-proof"
  ? (headers) => Object.fromEntries(Object.entries(headers).filter(([key]) => key !== "X-Customer-Session-Token"))
  : (headers) => headers

const refreshed = (token) => new Response(null, { headers: {
  "X-Customer-Session-Token": token, "X-Customer-Session-Expires-At": "2026-10-07T03:00:00Z",
} })

test("stale REST refresh cannot replace current session", async () => {
  const pending = deferred()
  const h = await loadImHarness(options({ request: async (path) => path === "/api/customer/session_exchange"
    ? response({ customer: { id: 202, name: "B" }, customerSessionToken: "synthetic-proof-B" }) : pending.promise }))
  await h.im.ensureCustomerSession()
  const work = h.im.fetchImMessages({ conversationId: 901 })
  const onResponse = h.requests.at(-1).options.onResponse
  await h.im.exchangeCustomerSession()
  const stored = h.window.sessionStorage.getItem("cs_ai_agent_customer_session")
  onResponse(refreshed("synthetic-old-rest"))
  pending.resolve({ results: [] })
  await work
  assert.ok(h.window.sessionStorage.getItem("cs_ai_agent_customer_session") === stored, "stale A response must preserve B storage")
})

test("same customer concurrent refresh retains the epoch", async () => {
  const pending = [deferred(), deferred()]
  let index = 0
  const h = await loadImHarness(options({ request: async () => pending[index++].promise }))
  await h.im.ensureCustomerSession()
  const scope = h.scope.captureCustomerSessionScope()
  const works = [h.im.fetchImMessages({ conversationId: 901 }), h.im.fetchImMessages({ conversationId: 901 })]
  h.im.beginCustomerSessionBootstrap()
  for (let i = 0; i < 2; i++) {
    h.requests[i].options.onResponse(refreshed(`synthetic-concurrent-${i}`))
    pending[i].resolve({ results: [] })
    await works[i]
    assert.ok(h.im.readCustomerSession().customerSessionToken === `synthetic-concurrent-${i}`, "same-A refresh survives other token and attempt changes")
    assert.equal(h.scope.captureCustomerSessionScope().epoch, scope.epoch)
  }
})

test("stale REST refresh cannot replace current session after identity departure and reactivation", async () => {
  const pending = deferred()
  const h = await loadImHarness(options({ request: async (path) => path === "/api/customer/session_exchange"
    ? response({ customerSessionToken: "synthetic-reactivated" }) : pending.promise }))
  await h.im.ensureCustomerSession()
  const oldScope = h.scope.captureCustomerSessionScope()
  const work = h.im.fetchImMessages({ conversationId: 901 }), callback = h.requests.at(-1).options.onResponse
  h.im.departCustomerSessionIdentity()
  await h.im.exchangeCustomerSession()
  assert.notEqual(h.scope.captureCustomerSessionScope().epoch, oldScope.epoch)
  const stored = h.window.sessionStorage.getItem("cs_ai_agent_customer_session")
  callback(refreshed("synthetic-departed-rest"))
  pending.resolve({ results: [] }); await work
  assert.ok(h.window.sessionStorage.getItem("cs_ai_agent_customer_session") === stored, "departed epoch cannot refresh even a reactivated customer id")
})

test("channel replacement invalidates captured REST refresh scope", async () => {
  const pending = deferred(), runtime = config()
  const h = await loadImHarness(options({ config: runtime, request: async (path) => path === "/api/customer/session_exchange" ? response() : pending.promise }))
  await h.im.ensureCustomerSession()
  const work = h.im.fetchImMessages({ conversationId: 901 }), callback = h.requests.at(-1).options.onResponse
  runtime.channelId = "channel-D"
  await h.im.exchangeCustomerSession()
  const stored = h.window.sessionStorage.getItem("cs_ai_agent_customer_session")
  callback(refreshed("synthetic-old-channel"))
  pending.resolve({ results: [] }); await work
  assert.ok(h.window.sessionStorage.getItem("cs_ai_agent_customer_session") === stored)
})

test("cached guest session avoids exchange", async () => {
  const { im, requests } = await loadImHarness(options())
  const result = await im.ensureCustomerSession()
  assert.equal(result.customer.id, 101)
  assert.equal(requests.length, 0)
})

test("guest exchange sends matching continuity header", async () => {
  const { im, requests } = await loadImHarness(options())
  await im.exchangeCustomerSession()
  assert.equal(requests.length, 1)
  const headers = selectHeaders(requests[0].options.headers)
  assert.ok(headers["X-Customer-Session-Token"] === "synthetic-proof-A", "matching guest proof must be sent")
  assert.ok(!("Authorization" in headers), "guest proof is not host Authorization")
})

test("bootstrap attempts supersede tickets without changing the active identity epoch", async () => {
  const { im, scope } = await loadImHarness(options())
  const active = scope.activateCustomerSessionScope(session())
  const first = scope.beginCustomerSessionAttempt("private-fixture-key")
  const second = scope.beginCustomerSessionAttempt("private-fixture-key")
  const bootstrap = im.beginCustomerSessionBootstrap()
  assert.equal(second.generation, first.generation + 1)
  assert.equal(bootstrap.generation, second.generation + 1)
  assert.equal(scope.isCustomerSessionAttemptCurrent(first), false)
  assert.equal(scope.isCustomerSessionAttemptCurrent(bootstrap), true)
  assert.equal(scope.captureCustomerSessionScope().epoch, active.epoch)
  assert.equal(scope.isCustomerSessionScopeCurrent(active), true)
})

test("same customer and channel activation retains epoch; replacement and invalidation reject old scopes", async () => {
  const { scope } = await loadImHarness(options())
  assert.equal(scope.captureCustomerSessionScope(), null)
  assert.equal(scope.isCustomerSessionScopeCurrent(null), false)
  const first = scope.activateCustomerSessionScope(session())
  const same = scope.activateCustomerSessionScope(session({ identityKey: "guest:updated-metadata" }))
  assert.equal(same.epoch, first.epoch)
  assert.equal(scope.isCustomerSessionScopeCurrent(first), true)
  const replacement = scope.activateCustomerSessionScope(session({ customer: { id: 202, name: "B" } }))
  assert.equal(replacement.epoch, first.epoch + 1)
  assert.equal(scope.isCustomerSessionScopeCurrent(first), false)
  const otherChannel = scope.activateCustomerSessionScope(session({ customer: { id: 202, name: "B" }, channelId: "channel-D" }))
  assert.equal(otherChannel.epoch, replacement.epoch + 1)
  const ticket = scope.beginCustomerSessionAttempt("private-fixture-key")
  scope.invalidateCustomerSessionScope()
  assert.equal(scope.captureCustomerSessionScope(), null)
  assert.equal(scope.isCustomerSessionAttemptCurrent(ticket), true)
  const afterDeparture = scope.activateCustomerSessionScope(session())
  assert.equal(afterDeparture.epoch, otherChannel.epoch + 1)
})

test("near-expiry valid proof renews the same customer without identity reset", async () => {
  const pending = deferred()
  const { im, scope, store, requests } = await loadSupportChatHarness(options({
    session: session({ expiresAt: "2026-10-07T00:00:04Z" }), request: async () => pending.promise,
  }))
  const active = scope.activateCustomerSessionScope(im.readCustomerSession())
  store.setState({ conversation: { id: 901 }, messages: [{ id: 902, content: "A transcript" }], messagesCursor: "902" })
  assert.equal(im.canReuseCustomerSession(), false)
  assert.equal(im.isCustomerSessionContinuityLost(), false)
  const previous = im.beginCustomerSessionBootstrap()
  const attempt = im.beginCustomerSessionBootstrap()
  assert.equal(attempt.generation, previous.generation + 1)
  const work = im.ensureCustomerSession(attempt)
  assert.equal(requests.length, 1)
  assert.ok(selectHeaders(requests[0].options.headers)["X-Customer-Session-Token"] === "synthetic-proof-A")
  assert.equal(scope.isCustomerSessionAttemptCurrent(attempt), true, "ensure must not begin a second ticket")
  assert.equal(scope.captureCustomerSessionScope().epoch, active.epoch)
  assert.equal(store.getState().messages[0].content, "A transcript")
  pending.resolve(response({ customerSessionToken: "synthetic-renewed-proof" }))
  await work
  assert.equal(scope.captureCustomerSessionScope().epoch, active.epoch)
  assert.equal(scope.isCustomerSessionScopeCurrent(active), true)
  assert.equal(store.getState().conversation.id, 901)
  assert.equal(store.getState().messagesCursor, "902")
  assert.ok(im.readCustomerSession().customerSessionToken === "synthetic-renewed-proof")
})

for (const [name, cached] of [
  ["locally expired", session({ expiresAt: "2026-10-07T00:00:00Z" })],
  ["missing", null],
  ["missing token", session({ customerSessionToken: "" })],
  ["invalid expiry", session({ expiresAt: "invalid" })],
  ["nonpositive customer", session({ customer: { id: 0, name: "invalid" } })],
]) {
  test(`${name} proof departs old scope before fresh exchange dispatch`, async () => {
    const pending = deferred()
    let harness
    harness = await loadImHarness(options({ session: cached, request: async (_path, init) => {
      assert.equal(harness.scope.captureCustomerSessionScope(), null)
      assert.equal(harness.im.readCustomerSession(), null)
      assert.ok(!("X-Customer-Session-Token" in init.headers))
      return pending.promise
    } }))
    const { im, scope, requests } = harness
    const active = scope.activateCustomerSessionScope(session())
    assert.equal(im.canReuseCustomerSession(), false)
    assert.equal(im.isCustomerSessionContinuityLost(), true)
    const attempt = im.beginCustomerSessionBootstrap()
    assert.equal(scope.isCustomerSessionScopeCurrent(active), true, "starting a ticket alone preserves scope")
    const work = im.ensureCustomerSession(attempt)
    assert.equal(requests.length, 1)
    assert.equal(scope.isCustomerSessionScopeCurrent(active), false)
    assert.equal(scope.isCustomerSessionAttemptCurrent(attempt), true)
    pending.resolve(response({ customer: { id: 202, name: "B" } }))
    await work
    assert.equal(scope.captureCustomerSessionScope().epoch, active.epoch + 1)
    assert.equal(scope.captureCustomerSessionScope().customerId, 202)
  })
}

for (const [name, change] of [
  ["channel", { channelId: "channel-D" }], ["hint", { externalId: "hint-Y" }], ["mode", { userToken: "synthetic-host-proof" }],
]) {
  test(`explicit ${name} switch omits incompatible guest proof and departs before dispatch`, async () => {
    const runtime = config(), pending = deferred()
    const { im, scope, requests } = await loadImHarness(options({ config: runtime, request: async () => pending.promise }))
    const active = scope.activateCustomerSessionScope(session())
    Object.assign(runtime, change)
    assert.equal(im.isCustomerSessionContinuityLost(), true)
    const work = im.exchangeCustomerSession()
    assert.equal(scope.isCustomerSessionScopeCurrent(active), false)
    assert.ok(!("X-Customer-Session-Token" in requests[0].options.headers))
    pending.resolve(response({ identityKey: name === "mode" ? "user:host" : name === "hint" ? "guest:hint-Y" : "guest:hint-X", customer: { id: 202, name: "B" } }))
    await work
    assert.equal(scope.captureCustomerSessionScope().epoch, active.epoch + 1)
  })
}

test("departure is idempotent and does not supersede the current attempt", async () => {
  const { im, scope } = await loadImHarness(options())
  const active = scope.activateCustomerSessionScope(session())
  const attempt = im.beginCustomerSessionBootstrap()
  im.departCustomerSessionIdentity()
  im.departCustomerSessionIdentity()
  assert.equal(im.readCustomerSession(), null)
  assert.equal(scope.isCustomerSessionAttemptCurrent(attempt), true)
  const installed = scope.activateCustomerSessionScope(session())
  assert.equal(installed.epoch, active.epoch + 1)
})

test("rejected matching proof has one request and never authorizes anonymous fallback or departure", async () => {
  const { im, scope, requests, window } = await loadImHarness(options({ request: async () => { throw new Error("proof rejected") } }))
  const active = scope.activateCustomerSessionScope(session())
  const before = window.sessionStorage.getItem("cs_ai_agent_customer_session")
  await assert.rejects(im.exchangeCustomerSession(), /proof rejected/)
  assert.equal(requests.length, 1)
  assert.ok(selectHeaders(requests[0].options.headers)["X-Customer-Session-Token"] === "synthetic-proof-A")
  assert.equal(scope.isCustomerSessionScopeCurrent(active), true)
  assert.ok(window.sessionStorage.getItem("cs_ai_agent_customer_session") === before)
})

test("failed fresh exchange never restores departed scope or session", async () => {
  const { im, scope, requests } = await loadImHarness(options({ session: null, request: async () => { throw new Error("exchange unavailable") } }))
  const active = scope.activateCustomerSessionScope(session())
  await assert.rejects(im.ensureCustomerSession(), /exchange unavailable/)
  assert.equal(requests.length, 1)
  assert.equal(im.readCustomerSession(), null)
  assert.equal(scope.isCustomerSessionScopeCurrent(active), false)
})

test("host user Authorization and entry token reuse are preserved without guest header", async () => {
  const runtime = { ...config(), userToken: "synthetic-host-proof" }
  const { im, scope, requests, window } = await loadImHarness(options({ config: runtime, session: session({ identityKey: "user:host" }), request: async () => response({ identityKey: "user:host" }) }))
  const active = scope.activateCustomerSessionScope(im.readCustomerSession())
  assert.equal(im.canReuseCustomerSession(), false, "first host entry still exchanges")
  assert.equal(im.isCustomerSessionContinuityLost(), false, "host token exchange alone is not known departure")
  await im.ensureCustomerSession()
  assert.equal(scope.isCustomerSessionScopeCurrent(active), true)
  assert.ok(requests[0].options.headers.Authorization === "Bearer synthetic-host-proof")
  assert.ok(!("X-Customer-Session-Token" in requests[0].options.headers))
  assert.equal(im.canReuseCustomerSession(), true)
  await im.ensureCustomerSession()
  assert.equal(requests.length, 1)
  runtime.userToken = "synthetic-next-host-proof"
  assert.equal(im.canReuseCustomerSession(), false)
  assert.equal(im.isCustomerSessionContinuityLost(), false)
  await im.ensureCustomerSession()
  assert.equal(requests.length, 2)
  assert.equal(scope.isCustomerSessionScopeCurrent(active), true)
  assert.ok(requests[1].options.headers.Authorization === "Bearer synthetic-next-host-proof")
  im.departCustomerSessionIdentity()
  window.sessionStorage.setItem("cs_ai_agent_customer_session", JSON.stringify(session({ identityKey: "user:host" })))
  assert.equal(im.canReuseCustomerSession(), false, "departure clears host entry reuse key")
})

test("user to guest mode switch is known departure", async () => {
  const { im, scope, requests } = await loadImHarness(options({ session: session({ identityKey: "user:host" }) }))
  const active = scope.activateCustomerSessionScope(im.readCustomerSession())
  assert.equal(im.isCustomerSessionContinuityLost(), true)
  await im.ensureCustomerSession()
  assert.equal(scope.isCustomerSessionScopeCurrent(active), false)
  assert.ok(!("X-Customer-Session-Token" in requests[0].options.headers))
})

test("stale supplied attempts reject before request, departure, guest-key or session mutation", async () => {
  const runtime = config()
  const { im, scope, requests, window } = await loadImHarness(options({ config: runtime }))
  const active = scope.activateCustomerSessionScope(session())
  const stale = im.beginCustomerSessionBootstrap()
  const winner = im.beginCustomerSessionBootstrap()
  runtime.externalId = ""
  const before = window.sessionStorage.getItem("cs_ai_agent_customer_session")
  await assert.rejects(im.ensureCustomerSession(stale))
  await assert.rejects(im.exchangeCustomerSession(stale))
  assert.equal(requests.length, 0)
  assert.equal(scope.isCustomerSessionScopeCurrent(active), true)
  assert.equal(scope.isCustomerSessionAttemptCurrent(winner), true)
  assert.equal(window.localStorage.getItem("cs_ai_agent_im_guest_id"), null)
  assert.ok(window.sessionStorage.getItem("cs_ai_agent_customer_session") === before)
})

test("older exchange response cannot overwrite the winner even with the same hint", async () => {
  const first = deferred(), second = deferred()
  let count = 0
  const { im, scope } = await loadImHarness(options({ request: async () => (++count === 1 ? first.promise : second.promise) }))
  const active = scope.activateCustomerSessionScope(session())
  const oldWork = im.exchangeCustomerSession()
  const oldRejected = assert.rejects(oldWork)
  const newWork = im.exchangeCustomerSession()
  second.resolve(response({ customer: { id: 202, name: "B" }, customerSessionToken: "synthetic-proof-B" }))
  await newWork
  const winnerScope = scope.captureCustomerSessionScope()
  first.resolve(response())
  await oldRejected
  assert.equal(im.readCustomerSession().customer.id, 202)
  assert.ok(im.readCustomerSession().customerSessionToken === "synthetic-proof-B")
  assert.equal(scope.isCustomerSessionScopeCurrent(winnerScope), true)
  assert.equal(scope.isCustomerSessionScopeCurrent(active), false)
})

test("config change without a new ticket rejects pending response before acceptance", async () => {
  const runtime = config(), pending = deferred()
  const { im, scope, window } = await loadImHarness(options({ config: runtime, request: async () => pending.promise }))
  const active = scope.activateCustomerSessionScope(session())
  const before = window.sessionStorage.getItem("cs_ai_agent_customer_session")
  const work = im.exchangeCustomerSession()
  const rejected = assert.rejects(work)
  runtime.externalId = "hint-Y"
  pending.resolve(response({ customer: { id: 202, name: "B" } }))
  await rejected
  assert.ok(window.sessionStorage.getItem("cs_ai_agent_customer_session") === before)
  assert.equal(scope.isCustomerSessionScopeCurrent(active), true)
})

test("current ticket for an old binding rejects before departure or dispatch", async () => {
  const runtime = config()
  const { im, scope, requests } = await loadImHarness(options({ config: runtime }))
  const active = scope.activateCustomerSessionScope(session())
  const attempt = im.beginCustomerSessionBootstrap()
  runtime.channelId = "channel-D"
  await assert.rejects(im.exchangeCustomerSession(attempt))
  assert.equal(requests.length, 0)
  assert.equal(scope.isCustomerSessionScopeCurrent(active), true)
  assert.equal(im.readCustomerSession().customer.id, 101)
})

test("current ticket for a removed hint rejects without creating a new guest key", async () => {
  const runtime = config()
  const { im, scope, requests, window } = await loadImHarness(options({ config: runtime }))
  const active = scope.activateCustomerSessionScope(session())
  const attempt = im.beginCustomerSessionBootstrap()
  runtime.externalId = ""
  const before = window.sessionStorage.getItem("cs_ai_agent_customer_session")
  await assert.rejects(im.ensureCustomerSession(attempt))
  assert.equal(requests.length, 0)
  assert.equal(window.localStorage.getItem("cs_ai_agent_im_guest_id"), null)
  assert.ok(window.sessionStorage.getItem("cs_ai_agent_customer_session") === before)
  assert.equal(scope.isCustomerSessionScopeCurrent(active), true)
})

test("config base URL change rejects a pending response without accepting a new scope", async () => {
  const runtime = config(), pending = deferred()
  const { im, scope, window } = await loadImHarness(options({ config: runtime, request: async () => pending.promise }))
  const active = scope.activateCustomerSessionScope(session())
  const before = window.sessionStorage.getItem("cs_ai_agent_customer_session")
  const work = im.exchangeCustomerSession()
  const rejected = assert.rejects(work)
  runtime.apiBaseUrl = "https://changed-api.test"
  pending.resolve(response({ customer: { id: 202, name: "B" } }))
  await rejected
  assert.ok(window.sessionStorage.getItem("cs_ai_agent_customer_session") === before)
  assert.equal(scope.isCustomerSessionScopeCurrent(active), true)
})

test("losing exchange rejection leaves accepted winner session and epoch intact", async () => {
  const first = deferred(), second = deferred()
  let count = 0
  const { im, scope } = await loadImHarness(options({ request: async () => (++count === 1 ? first.promise : second.promise) }))
  scope.activateCustomerSessionScope(session())
  const oldWork = im.exchangeCustomerSession()
  const rejected = assert.rejects(oldWork, /older exchange rejected/)
  const newWork = im.exchangeCustomerSession()
  second.resolve(response({ customer: { id: 202, name: "B" } }))
  await newWork
  const winner = scope.captureCustomerSessionScope()
  first.reject(new Error("older exchange rejected"))
  await rejected
  assert.equal(im.readCustomerSession().customer.id, 202)
  assert.equal(scope.isCustomerSessionScopeCurrent(winner), true)
  assert.equal(count, 2)
})

for (const [name, malformed] of [
  ["zero ID", { customer: { id: 0, name: "invalid" } }],
  ["negative ID", { customer: { id: -1, name: "invalid" } }],
  ["missing customer", { customer: undefined }],
  ["missing name", { customer: { id: 101 } }],
  ["missing token", { customerSessionToken: undefined }],
  ["empty token", { customerSessionToken: " " }],
  ["missing identity", { identityKey: undefined }],
  ["wrong guest binding", { identityKey: "guest:other-hint" }],
  ["wrong source", { identityKey: "user:host" }],
  ["invalid expiry", { expiresAt: "invalid" }],
  ["expired response", { expiresAt: "2026-10-07T00:00:00Z" }],
]) {
  test(`invalid response ${name} is rejected without storage or scope acceptance`, async () => {
    const { im, scope, window } = await loadImHarness(options({ request: async () => response(malformed) }))
    // Allows the pre-scope implementation to fail on acceptance behavior itself.
    const active = scope?.activateCustomerSessionScope(session())
    const before = window.sessionStorage.getItem("cs_ai_agent_customer_session")
    await assert.rejects(im.exchangeCustomerSession())
    assert.ok(window.sessionStorage.getItem("cs_ai_agent_customer_session") === before)
    assert.equal(scope.isCustomerSessionScopeCurrent(active), true)
  })
}

test("guest key and public session response shape remain unchanged", async () => {
  const { im, window, requests } = await loadImHarness(options({ config: { channelId: "channel-C" }, session: null,
    request: async () => response({ identityKey: "guest:guest_fixture-1" }),
  }))
  const result = await im.ensureCustomerSession()
  assert.equal(window.localStorage.getItem("cs_ai_agent_im_guest_id"), "guest_fixture-1")
  assert.equal(requests[0].options.headers["X-External-Id"], "guest_fixture-1")
  assert.deepEqual(Object.keys(result).sort(), ["channelId", "customer", "customerSessionToken", "expiresAt", "identityKey"])
  assert.ok(im.readCustomerSession().customerSessionToken === result.customerSessionToken)
})

test("support chat harness executes real store bootstrap and realtime transport", async () => {
  const { im, store, sockets, flush, requests } = await loadSupportChatHarness(options({ request: async (apiPath) => {
    if (apiPath.startsWith("/api/channel/config")) return { title: "Fixture chat" }
    if (apiPath === "/api/conversation/create_or_match") return { id: 901 }
    if (apiPath.startsWith("/api/message/list")) return { results: [{ id: 902, content: "A transcript" }], page: { page: 1, limit: 50, total: 1 } }
    throw new Error("Unexpected fixture I/O")
  } }))
  store.getState().bootstrap()
  await flush()
  assert.equal(store.getState().title, "Fixture chat")
  assert.equal(store.getState().conversation.id, 901)
  assert.equal(store.getState().messages[0].id, 902)
  assert.equal(sockets.length, 1)
  assert.equal(store.getState().socket, sockets[0])
  assert.equal(requests.some((item) => item.path === "/api/customer/session_exchange"), false)
  assert.ok(im.readCustomerSession().customerSessionToken === "synthetic-proof-A")
  store.getState().disconnectSocket()
  assert.equal(sockets[0].closed, true)
})
