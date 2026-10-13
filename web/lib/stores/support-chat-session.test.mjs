import assert from "node:assert/strict"
import test from "node:test"
import { deferred, loadSupportChatHarness } from "../test/customer-session-harness.mjs"

const now = Date.parse("2026-10-07T01:00:00Z")
const presentation = { title: "Customer support", subtitle: "Here to help", themeColor: "#123456" }
const session = (overrides = {}) => ({
  channelId: "channel-C", identityKey: "guest:hint-X",
  customer: { id: 101, name: "A" }, customerSessionToken: "synthetic-proof-A",
  expiresAt: "2026-10-07T02:00:00Z", ...overrides,
})
const conversation = (id) => ({
  id, channelId: 1, customerName: id === 901 ? "A" : "B", status: 1,
  serviceMode: 1, priority: 0, currentAssigneeId: 0, lastMessageId: id + 1,
  customerUnreadCount: 0, agentUnreadCount: 0, customerLastReadMessageId: id + 1,
  agentLastReadMessageId: 0,
})
const message = (id) => ({
  id: id + 1, conversationId: id, senderType: "customer", senderId: id === 901 ? 101 : 202,
  messageType: "text", content: id === 901 ? "A transcript marker" : "B transcript",
  sendStatus: 1, customerRead: true, agentRead: false,
})

const asset = { id: 1, assetId: "fixture-asset", provider: "local", storageKey: "fixture",
  filename: "fixture.txt", fileSize: 7, mimeType: "text/plain", status: 1, url: "/fixture",
  createdAt: "", updatedAt: "", createUserId: 0, createUserName: "", updateUserId: 0, updateUserName: "" }
const history = (content = "late A data") => ({ results: [{ ...message(901), content }],
  page: { page: 1, limit: 50, total: 1 }, cursor: "902", hasMore: true })
const refreshResponse = () => new Response(null, { headers: {
  "X-Customer-Session-Token": "synthetic-late-refresh", "X-Customer-Session-Expires-At": "2026-10-07T03:00:00Z",
} })

// Missing scope checks at any success/catch/finally boundary must fail this full
// state snapshot, including flags that B has independently set while A waits.
function stateSnapshot(h) {
  const { customer, customerChannelId, conversation, messages, messagesCursor, messagesHasMore,
    messagesLoadingMore, initialized, error, sending, uploadingAsset, closingConversation,
    readingMessageId, status, isOpen, isVisible } = h.store.getState()
  return JSON.parse(JSON.stringify({ customer, customerChannelId, conversation, messages, messagesCursor,
    messagesHasMore, messagesLoadingMore, initialized, error, sending, uploadingAsset,
    closingConversation, readingMessageId, status, isOpen, isVisible }))
}

async function switchToB(h) {
  writeSession(h, session({ expiresAt: "2026-10-07T01:00:03Z" }))
  h.store.getState().bootstrap()
  await h.flush()
  assert.equal(h.store.getState().customer.id, 202)
  h.store.setState({ error: "B current error", messagesCursor: "912", messagesHasMore: true,
    messagesLoadingMore: true, sending: true, uploadingAsset: true, closingConversation: true,
    readingMessageId: 912, status: "connected" })
}

async function actionHarness() {
  let gate = null
  const h = await initializedA({
    exchange: async () => session({ customer: { id: 202, name: "B" }, customerSessionToken: "synthetic-proof-B" }),
    intercept: (path, options) => {
      if (!gate || !gate.matches(path)) return undefined
      const active = gate
      gate = null
      active.dispatched.resolve({ path, options })
      return active.completion.promise
    },
  })
  h.hold = (matches = () => true) => {
    const pending = { matches, completion: deferred(), dispatched: deferred() }
    gate = pending
    return pending
  }
  return h
}

const actions = [
  ["refreshMessages", (h) => h.store.getState().refreshMessages(), () => history()],
  ["syncLatestMessages", (h) => h.store.getState().syncLatestMessages(), () => history()],
  ["loadOlderMessages", (h) => { h.store.setState({ messagesHasMore: true, messagesCursor: "902" }); return h.store.getState().loadOlderMessages() }, () => history()],
  ["handleSendMessage", (h) => h.store.getState().handleSendMessage("A draft"), () => ({ ...message(901), id: 950 })],
  ["markConversationRead", (h) => { h.store.setState({ conversation: { ...conversation(901), customerUnreadCount: 1, customerLastReadMessageId: 0 } }); return h.store.getState().markConversationRead() }, () => ({})],
  ["uploadMessageImage", (h) => h.store.getState().uploadMessageImage(new Blob(["fixture"])), () => asset],
  ["sendAttachment", (h) => h.store.getState().sendAttachment(new Blob(["fixture"])), () => asset],
  ["closeConversation", (h) => h.store.getState().closeConversation(), () => ({})],
  ["retry", (h) => h.store.getState().retry(), () => history()],
]

for (const [name, start, response] of actions) {
  for (const completion of ["success", "error", ...(name === "uploadMessageImage" ? ["finally"] : [])]) {
    test(`stale ${name} ${completion} cannot mutate the new customer`, async () => {
      const h = await actionHarness(), pending = h.hold()
      const work = start(h).then((value) => ({ value }), (error) => ({ error }))
      await pending.dispatched.promise
      await switchToB(h)
      const before = stateSnapshot(h), socket = h.store.getState().socket, requestCount = h.requests.length
      if (completion === "error") pending.completion.reject(new Error("late A request failed"))
      else pending.completion.resolve(response())
      const result = await work
      assert.deepEqual(stateSnapshot(h), before, `B state survives A ${name} ${completion}`)
      assert.ok(h.store.getState().socket === socket, "B socket survives completion")
      assert.equal(h.requests.length, requestCount, "obsolete work cannot dispatch a second request or reconnect")
      if (name === "uploadMessageImage") assert.equal(result.value, null, "obsolete upload cannot return an insertable A asset")
    })
  }
}

for (const completion of ["success", "error"]) {
  test(`stale sendAttachment ${completion} cannot mutate the new customer after send dispatch`, async () => {
    const h = await actionHarness(), upload = h.hold()
    const work = h.store.getState().sendAttachment(new Blob(["fixture"])).catch(() => {})
    await upload.dispatched.promise
    const send = h.hold()
    upload.completion.resolve(asset)
    const request = await send.dispatched.promise
    assert.equal(JSON.parse(request.options.body).conversationId, 901)
    await switchToB(h)
    const before = stateSnapshot(h)
    if (completion === "error") send.completion.reject(new Error("late send failed"))
    else send.completion.resolve({ ...message(901), id: 950 })
    await work
    assert.deepEqual(stateSnapshot(h), before)
  })
}

test("stale refreshMessages success cannot mutate the new customer when conversation id is reused", async () => {
  const h = await actionHarness(), pending = h.hold()
  const work = h.store.getState().refreshMessages()
  await pending.dispatched.promise
  await switchToB(h)
  h.store.setState({ conversation: { ...conversation(901), customerName: "B" } })
  const before = stateSnapshot(h)
  pending.completion.resolve(history())
  await work
  assert.deepEqual(stateSnapshot(h), before, "identity epoch must be checked independently of conversation id")
})

test("same customer conversation replacement rejects pending history", async () => {
  const h = await actionHarness(), pending = h.hold()
  const work = h.store.getState().refreshMessages()
  await pending.dispatched.promise
  h.store.setState({ conversation: conversation(911), messages: [message(911)] })
  const before = stateSnapshot(h)
  pending.completion.resolve(history())
  await work
  assert.deepEqual(stateSnapshot(h), before)
})

for (const completion of ["success", "error"]) {
  test(`stale retry ${completion} cannot mutate the new customer after its refresh settles`, async () => {
    const h = await actionHarness(), pending = h.hold(), finished = deferred(), release = deferred()
    const refresh = h.store.getState().refreshMessages
    let first = true
    // Keep the real refresh action; pause its result delivery to retry after
    // refresh has already applied A's success/error while A is still current.
    h.store.setState({ refreshMessages: async () => {
      const hold = first
      first = false
      try { return await refresh() }
      finally { if (hold) { finished.resolve(); await release.promise } }
    } })
    const work = h.store.getState().retry()
    await pending.dispatched.promise
    if (completion === "error") pending.completion.reject(new Error("current A refresh failed"))
    else pending.completion.resolve(history())
    await finished.promise
    await switchToB(h)
    h.store.getState().disconnectSocket()
    const before = stateSnapshot(h), socketCount = h.sockets.length
    release.resolve()
    await work
    assert.deepEqual(stateSnapshot(h), before, "retry's own continuation must preserve B")
    assert.equal(h.sockets.length, socketCount, "obsolete retry cannot reopen B's socket")
  })
}

for (const type of ["customer_session.refresh", "message.created", "resyncRequired", "conversation.updated"]) {
  test(type === "customer_session.refresh" ? "stale socket refresh cannot replace current session" : `stale socket ${type} cannot mutate the new customer`, async () => {
    const h = await actionHarness(), socket = h.store.getState().socket
    h.deferRealtime()
    // A queued message deliberately names B's conversation: socket identity is
    // authoritative even if an envelope appears to match the current store.
    const data = type === "customer_session.refresh"
      ? { customerSessionToken: "synthetic-old-socket", expiresAt: "2026-10-07T03:00:00Z" }
      : type === "message.created" ? { conversationId: 911, message: { ...message(911), content: "old socket marker" } }
      : { conversationId: 911, status: 2 }
    socket.emit("message", { data: JSON.stringify({ type, data }) })
    assert.equal(h.realtimeDeliveries.length, 1)
    await switchToB(h)
    const before = stateSnapshot(h), stored = h.window.sessionStorage.getItem("cs_ai_agent_customer_session"), count = h.requests.length
    h.realtimeDeliveries.shift()()
    await h.flush()
    assert.deepEqual(stateSnapshot(h), before)
    assert.ok(h.window.sessionStorage.getItem("cs_ai_agent_customer_session") === stored, "B session storage survives old socket refresh")
    assert.equal(h.requests.length, count, "old resync must not fetch B history")
  })
}

test("stale socket status callbacks cannot mutate the new customer", async () => {
  const h = await actionHarness(), socket = h.store.getState().socket
  await switchToB(h)
  const before = stateSnapshot(h), currentSocket = h.store.getState().socket
  socket.emit("open"); socket.emit("error"); socket.emit("close")
  assert.deepEqual(stateSnapshot(h), before)
  assert.ok(h.store.getState().socket === currentSocket)
})

for (const completion of ["permission", "click"]) {
  test(`stale notification ${completion} cannot expose the old customer`, async () => {
    const h = await actionHarness()
    h.document.visibilityState = "hidden"
    if (completion === "click") h.Notification.permission = "granted"
    h.store.getState().socket.emit("message", { data: JSON.stringify({ type: "message.created",
      data: { conversationId: 901, message: { ...message(901), id: 950, senderType: "agent", content: "private A notification" } } }) })
    await switchToB(h)
    h.store.setState({ isOpen: false, isVisible: false })
    const before = stateSnapshot(h)
    if (completion === "permission") {
      h.notificationPermission.resolve("granted")
      await h.flush()
      assert.equal(h.notifications.length, 0, "permission resolved for departed A must not create a notification")
    } else {
      assert.equal(h.notifications.length, 1)
      h.notifications[0].onclick()
      assert.equal(h.focusCount(), 0, "old notification cannot focus B")
    }
    assert.deepEqual(stateSnapshot(h), before)
  })
}

for (const phase of ["pending", "accepted"]) {
  test(`same-A callbacks captured before renewal remain valid while renewal is ${phase}`, async () => {
    const renewal = deferred(), data = deferred()
    let holdHistory = false
    const h = await initializedA({ exchange: async () => renewal.promise, intercept: (path) => {
      if (holdHistory && path.startsWith("/api/message/list")) { holdHistory = false; return data.promise }
    } })
    const oldScope = h.scope.captureCustomerSessionScope(), socket = h.store.getState().socket
    holdHistory = true
    const work = h.store.getState().refreshMessages()
    const header = h.requests.at(-1).options.onResponse
    h.deferRealtime()
    socket.emit("message", { data: JSON.stringify({ type: "customer_session.refresh",
      data: { customerSessionToken: "synthetic-renewal-socket", expiresAt: "2026-10-07T04:00:00Z" } }) })
    writeSession(h, session({ expiresAt: "2026-10-07T01:00:03Z" }))
    h.store.getState().bootstrap()
    await h.flush()
    if (phase === "accepted") {
      renewal.resolve(session({ customerSessionToken: "synthetic-renewed" }))
      await h.flush()
    }
    data.resolve(history("legitimate A history"))
    await work
    assert.equal(h.store.getState().messages[0].content, "legitimate A history")
    header(refreshResponse())
    assert.ok(h.im.readCustomerSession().customerSessionToken === "synthetic-late-refresh")
    h.realtimeDeliveries.shift()()
    assert.ok(h.im.readCustomerSession().customerSessionToken === "synthetic-renewal-socket")
    assert.equal(h.scope.captureCustomerSessionScope().epoch, oldScope.epoch)
    if (phase === "pending") { renewal.resolve(session()); await h.flush() }
    // Reconnecting the same identity must also capture a usable socket scope.
    socket.emit("close")
    h.runTimers()
    const reconnected = h.store.getState().socket
    assert.ok(reconnected && reconnected !== socket)
    reconnected.emit("message", { data: JSON.stringify({ type: "customer_session.refresh",
      data: { customerSessionToken: "synthetic-reconnected", expiresAt: "2026-10-07T04:00:00Z" } }) })
    h.realtimeDeliveries.shift()()
    assert.ok(h.im.readCustomerSession().customerSessionToken === "synthetic-reconnected")
  })
}

for (const proof of ["expired", "missing"]) {
  test(`${proof} departure rejects captured A callbacks before fresh response and after failure`, async () => {
    const renewal = deferred(), pending = [deferred(), deferred()]
    let historyIndex = 0, holdHistory = false
    const h = await initializedA({ exchange: async () => renewal.promise, intercept: (path) => {
      if (holdHistory && path.startsWith("/api/message/list")) return pending[historyIndex++].promise
    } })
    holdHistory = true
    const works = [h.store.getState().refreshMessages(), h.store.getState().syncLatestMessages()]
    const headers = h.requests.slice(-2).map((item) => item.options.onResponse)
    h.deferRealtime()
    for (let i = 0; i < 2; i++) h.store.getState().socket.emit("message", { data: JSON.stringify({ type: "customer_session.refresh",
      data: { customerSessionToken: "synthetic-departed", expiresAt: "2026-10-07T04:00:00Z" } }) })
    writeSession(h, proof === "missing" ? null : session({ expiresAt: "2026-10-07T01:00:00Z" }))
    h.store.getState().bootstrap()
    await h.flush()
    const observations = []
    for (let i = 0; i < 2; i++) {
      if (i === 1) { renewal.reject(new Error("fresh exchange failed")); await h.flush() }
      const before = stateSnapshot(h)
      pending[i].resolve(history())
      await works[i]
      headers[i](refreshResponse())
      h.realtimeDeliveries.shift()()
      observations.push({ before, after: stateSnapshot(h), sessionAbsent: h.im.readCustomerSession() === null })
    }
    for (const observation of observations) {
      assert.deepEqual(observation.after, observation.before)
      assert.equal(observation.sessionAbsent, true)
    }
  })
}

for (const completion of ["success", "error"]) {
  test(`older bootstrap cannot overwrite the winner (${completion})`, async () => {
    const older = deferred(), newer = deferred()
    let count = 0
    const h = await initializedA({ exchange: async () => (++count === 1 ? older.promise : newer.promise) })
    writeSession(h, session({ expiresAt: "2026-10-07T01:00:03Z" }))
    h.store.getState().bootstrap(); await h.flush()
    h.store.getState().bootstrap(); await h.flush()
    newer.resolve(session({ customer: { id: 202, name: "B" } })); await h.flush()
    const before = stateSnapshot(h), stored = h.window.sessionStorage.getItem("cs_ai_agent_customer_session")
    if (completion === "error") older.reject(new Error("older bootstrap failed"))
    else older.resolve(session())
    await h.flush()
    assert.deepEqual(stateSnapshot(h), before)
    assert.ok(h.window.sessionStorage.getItem("cs_ai_agent_customer_session") === stored)
  })
}

async function initializedA({ exchange = async () => session(), widget = async () => presentation, match = async (id) => conversation(id), intercept = () => undefined } = {}) {
  let matchCount = 0
  const runtime = { channelId: "channel-C", externalId: "hint-X" }
  const harness = await loadSupportChatHarness({
    config: runtime, session: session(), now, scopeControl: "normal",
    request: async (path, options) => {
      const intercepted = intercept(path, options)
      if (intercepted !== undefined) return intercepted
      if (path.startsWith("/api/channel/config")) return widget(path, options)
      if (path === "/api/customer/session_exchange") return exchange(path, options)
      if (path === "/api/conversation/create_or_match") return match(++matchCount === 1 ? 901 : 911)
      if (path.startsWith("/api/message/list")) {
        const id = Number(new URL(path, "https://fixture.test").searchParams.get("conversationId"))
        return { results: [message(id)], page: { page: 1, limit: 50, total: 1 }, cursor: "older-cursor", hasMore: false }
      }
      throw new Error("Unexpected fixture I/O")
    },
  })
  harness.store.getState().bootstrap()
  await harness.flush()
  assert.equal(harness.store.getState().conversation?.id, 901)
  assert.equal(harness.store.getState().messages[0]?.content, "A transcript marker")
  harness.runtime = runtime
  return harness
}

function writeSession(harness, value) {
  if (value === null) harness.window.sessionStorage.removeItem("cs_ai_agent_customer_session")
  else harness.window.sessionStorage.setItem("cs_ai_agent_customer_session", JSON.stringify(value))
}

test("same guest hint with new customer id clears the transcript", async () => {
  const h = await initializedA({ exchange: async () => session({ customer: { id: 202, name: "B" } }) })
  const previous = h.scope.captureCustomerSessionScope()
  const oldSocket = h.store.getState().socket
  writeSession(h, session({ expiresAt: "2026-10-07T01:00:03Z" }))
  h.store.getState().bootstrap()
  await h.flush()
  const state = h.store.getState()
  assert.notEqual(state.conversation?.id, 901, "B must load its conversation instead of retaining A")
  assert.equal(state.messages.some((item) => item.content === "A transcript marker"), false)
  assert.equal(state.customer?.id, 202)
  assert.equal(state.customerChannelId, "channel-C")
  assert.equal(h.scope.isCustomerSessionScopeCurrent(previous), false)
  assert.equal(oldSocket.closed, true)
  for (const [key, value] of Object.entries(presentation)) assert.equal(state[key], value)
  assert.equal(state.isOpen, true)
  assert.equal(state.isVisible, true)
})

test("channel config bootstrap failure is terminal before session or conversation initialization", async () => {
  const h = await loadSupportChatHarness({
    config: { channelId: "missing-channel", externalId: "hint-X" },
    session: null,
    now,
    scopeControl: "normal",
    request: async (path) => {
      if (path.startsWith("/api/channel/config")) throw new Error("channel unavailable")
      throw new Error(`Unexpected request: ${path}`)
    },
  })
  h.store.getState().bootstrap()
  await h.flush()
  const state = h.store.getState()

  assert.equal(state.bootstrapUnavailable, true)
  assert.equal(state.initialized, false)
  assert.equal(state.customer, null)
  assert.equal(state.conversation, null)
  assert.equal(h.requests.some((request) => request.path === "/api/customer/session_exchange"), false)
  assert.equal(h.requests.some((request) => request.path === "/api/conversation/create_or_match"), false)
})

test("initialized chat socket disconnect stays on the existing reconnect path", async () => {
  const h = await initializedA()
  const state = h.store.getState()

  assert.equal(state.bootstrapUnavailable, false)
  assert.equal(state.initialized, true)
  assert.ok(state.conversation)
  assert.ok(state.socket)

  const disconnectedSocket = state.socket
  disconnectedSocket.emit("close")

  assert.equal(h.store.getState().bootstrapUnavailable, false)
  assert.equal(h.store.getState().status, "connecting")
  assert.ok(h.store.getState().conversation)
  h.runTimers()
  assert.ok(h.store.getState().socket)
  assert.notEqual(h.store.getState().socket, disconnectedSocket)
})

function assertDeparted(h, oldScope, oldSocket, channelId = "channel-C") {
  const state = h.store.getState()
  assert.equal(state.conversation, null, "departed transcript is removed before awaiting bootstrap")
  assert.equal(state.messages.length, 0)
  assert.equal(state.messagesCursor, "")
  for (const key of ["messagesHasMore", "messagesLoadingMore", "initialized", "sending", "uploadingAsset", "closingConversation"]) {
    assert.equal(state[key], false, `${key} resets on departure`)
  }
  assert.equal(state.customer, null)
  assert.equal(state.customerChannelId, channelId)
  assert.equal(state.error, "")
  assert.equal(state.readingMessageId, 0)
  assert.equal(state.socket, null)
  assert.equal(state.status, "connecting")
  assert.equal(oldSocket.closed, true)
  assert.equal(h.scope.isCustomerSessionScopeCurrent(oldScope), false)
  oldSocket.emit("message", { data: JSON.stringify({ type: "message.created", data: message(901) }) })
  assert.equal(h.store.getState().messages.length, 0, "departed socket cannot repopulate A transcript")
  for (const [key, value] of Object.entries(presentation)) assert.equal(state[key], value)
  assert.equal(state.isOpen, true)
  assert.equal(state.isVisible, true)
}

function dirtyA(h) {
  h.store.setState({ messagesCursor: "902", messagesHasMore: true, messagesLoadingMore: true,
    sending: true, uploadingAsset: true, closingConversation: true, readingMessageId: 902, error: "old error" })
}

function observeAttempts(h) {
  const attempts = []
  const begin = h.im.beginCustomerSessionBootstrap
  h.im.beginCustomerSessionBootstrap = () => { const ticket = begin(); attempts.push(ticket); return ticket }
  return attempts
}

test("near-expiry valid proof renews the same customer without identity reset", async () => {
  const pending = deferred()
  const h = await initializedA({ exchange: async () => pending.promise })
  const oldScope = h.scope.captureCustomerSessionScope(), oldSocket = h.store.getState().socket
  const attempts = observeAttempts(h)
  const before = h.store.getState()
  writeSession(h, session({ expiresAt: "2026-10-07T01:00:03Z" }))
  h.store.getState().bootstrap()
  await h.flush()
  const exchanges = h.requests.filter((item) => item.path === "/api/customer/session_exchange")
  assert.equal(exchanges.length, 1)
  assert.ok(exchanges[0].options.headers["X-Customer-Session-Token"] === "synthetic-proof-A", "valid matching proof is presented")
  assert.equal(h.scope.captureCustomerSessionScope().epoch, oldScope.epoch)
  assert.equal(h.scope.isCustomerSessionScopeCurrent(oldScope), true, "pre-renewal A callback remains current while exchange waits")
  assert.equal(h.store.getState().conversation, before.conversation)
  assert.equal(h.store.getState().messages, before.messages)
  assert.ok(h.store.getState().socket === oldSocket, "same-customer socket is retained")
  assert.equal(attempts.length, 1)
  assert.equal(h.scope.isCustomerSessionAttemptCurrent(attempts[0]), true)
  pending.resolve(session({ customer: { id: 101, name: "A renewed" }, customerSessionToken: "synthetic-proof-renewed" }))
  await h.flush()
  assert.equal(h.scope.captureCustomerSessionScope().epoch, oldScope.epoch)
  assert.equal(h.scope.isCustomerSessionScopeCurrent(oldScope), true)
  assert.equal(h.store.getState().conversation, before.conversation)
  assert.equal(h.store.getState().messages[0].content, "A transcript marker")
  assert.ok(h.store.getState().socket === oldSocket, "same-customer socket is retained")
  assert.equal(oldSocket.closed, false)
  assert.equal(attempts.length, 1)
  assert.equal(h.scope.isCustomerSessionAttemptCurrent(attempts[0]), true)
  assert.equal(h.store.getState().customer.name, "A renewed")
  assert.ok(h.im.readCustomerSession().customerSessionToken === "synthetic-proof-renewed")
})

test("cached same customer and channel retains initialized conversation and socket", async () => {
  const h = await initializedA()
  const before = h.store.getState(), oldScope = h.scope.captureCustomerSessionScope()
  h.store.getState().bootstrap()
  await h.flush()
  assert.equal(h.store.getState().conversation, before.conversation)
  assert.equal(h.store.getState().initialized, true)
  assert.ok(h.store.getState().socket === before.socket, "cached same-customer socket is retained")
  assert.equal(h.scope.isCustomerSessionScopeCurrent(oldScope), true)
  assert.equal(h.requests.filter((item) => item.path === "/api/conversation/create_or_match").length, 1)
  assert.equal(h.requests.filter((item) => item.path === "/api/customer/session_exchange").length, 0)
})

test("same-customer renewal preserves connected status on an already-open socket", async () => {
  const pending = deferred()
  const h = await initializedA({ exchange: async () => pending.promise })
  const socket = h.store.getState().socket
  socket.emit("open")
  assert.equal(h.store.getState().status, "connected")
  writeSession(h, session({ expiresAt: "2026-10-07T01:00:03Z" }))
  h.store.getState().bootstrap()
  await h.flush()
  assert.equal(h.store.getState().status, "connected", "retained open socket remains connected while renewing")
  pending.resolve(session())
  await h.flush()
  assert.equal(h.store.getState().status, "connected")
  assert.ok(h.store.getState().socket === socket)
})

for (const [name, change, channelId, identityKey] of [
  ["locally expired proof", (h) => writeSession(h, session({ expiresAt: "2026-10-07T01:00:00Z" })), "channel-C", "guest:hint-X"],
  ["missing proof", (h) => writeSession(h, null), "channel-C", "guest:hint-X"],
  ["channel switch", (h) => { h.runtime.channelId = "channel-D" }, "channel-D", "guest:hint-X"],
  ["hint switch", (h) => { h.runtime.externalId = "hint-Y" }, "channel-C", "guest:hint-Y"],
]) {
  for (const reject of [false, true]) {
    test(`${name} invalidates old identity before fresh bootstrap${reject ? " and failure never restores A" : ""}`, async () => {
      const pending = deferred()
      const h = await initializedA({ exchange: async () => pending.promise })
      const oldScope = h.scope.captureCustomerSessionScope(), oldSocket = h.store.getState().socket
      dirtyA(h)
      change(h)
      h.store.getState().bootstrap()
      assertDeparted(h, oldScope, oldSocket, channelId)
      await h.flush()
      const exchanges = h.requests.filter((item) => item.path === "/api/customer/session_exchange")
      assert.equal(exchanges.length, 1)
      assert.equal("X-Customer-Session-Token" in exchanges[0].options.headers, false, "fresh bootstrap omits departed proof")
      assertDeparted(h, oldScope, oldSocket, channelId)
      if (reject) pending.reject(new Error("fresh bootstrap rejected"))
      else pending.resolve(session({ channelId, identityKey, customer: { id: 202, name: "B" } }))
      await h.flush()
      const state = h.store.getState()
      assert.equal(h.scope.isCustomerSessionScopeCurrent(oldScope), false)
      assert.equal(state.messages.some((item) => item.content === "A transcript marker"), false)
      if (reject) {
        assert.equal(state.customer, null)
        assert.equal(state.conversation, null)
        assert.equal(state.initialized, false)
        assert.equal(state.socket, null)
        assert.equal(state.status, "disconnected")
        // The external request fixture's Error crosses the VM realm, so the
        // store uses its existing localized fallback for this fixture rejection.
        assert.equal(state.error, "supportChat.initFailed")
      } else {
        assert.equal(state.customer?.id, 202)
        assert.equal(state.customerChannelId, channelId)
        assert.equal(state.conversation.id, 911)
      }
      assert.equal(h.requests.filter((item) => item.path === "/api/customer/session_exchange").length, 1)
    })
  }
}

test("unexpected different customer advances epoch once and atomically installs cleared state", async () => {
  const pending = deferred(), conversationPending = deferred()
  // Hold B's external I/O while retaining the real conversation API call.
  const h = await initializedA({ exchange: async () => pending.promise,
    match: async (id) => id === 911 ? conversationPending.promise : conversation(id) })
  const oldScope = h.scope.captureCustomerSessionScope(), oldSocket = h.store.getState().socket
  dirtyA(h)
  const resetStates = []
  const unsubscribe = h.store.subscribe((state) => resetStates.push(state))
  writeSession(h, session({ expiresAt: "2026-10-07T01:00:03Z" }))
  h.store.getState().bootstrap()
  await h.flush()
  assert.equal(h.scope.isCustomerSessionScopeCurrent(oldScope), true)
  assert.equal(oldSocket.closed, false)
  resetStates.length = 0
  pending.resolve(session({ customer: { id: 202, name: "B" } }))
  await h.flush()
  assert.equal(h.scope.captureCustomerSessionScope().epoch, oldScope.epoch + 1)
  const state = h.store.getState()
  assert.equal(state.conversation, null)
  assert.equal(state.messages.length, 0)
  assert.equal(state.messagesCursor, "")
  for (const key of ["messagesHasMore", "messagesLoadingMore", "initialized", "sending", "uploadingAsset", "closingConversation"]) assert.equal(state[key], false)
  assert.equal(state.readingMessageId, 0)
  assert.equal(state.error, "")
  assert.equal(state.socket, null)
  assert.equal(state.status, "connecting")
  assert.equal(oldSocket.closed, true)
  assert.equal(state.customer?.id, 202)
  assert.equal(state.customerChannelId, "channel-C")
  assert.equal(resetStates.length, 1, "accepted new identity reset uses one store update")
  assert.equal(resetStates[0].customer?.id, 202)
  assert.equal(resetStates[0].customerChannelId, "channel-C")
  assert.equal(resetStates[0].messages.length, 0)
  assert.equal(resetStates[0].conversation, null)
  unsubscribe()
  conversationPending.resolve(conversation(911))
  await h.flush()
})

test("proof disappearing during widget config clears A before fresh exchange and rejection", async () => {
  const widgetPending = deferred(), exchangePending = deferred()
  let deferWidget = false, dispatchState = null, dispatchScopeCurrent = true
  const h = await initializedA({
    widget: async () => deferWidget ? widgetPending.promise : presentation,
    exchange: async () => {
      dispatchState = h.store.getState()
      dispatchScopeCurrent = h.scope.isCustomerSessionScopeCurrent(oldScope)
      return exchangePending.promise
    },
  })
  const oldScope = h.scope.captureCustomerSessionScope(), oldSocket = h.store.getState().socket
  oldSocket.emit("open")
  deferWidget = true
  h.store.getState().bootstrap()
  await h.flush()
  assert.equal(h.store.getState().conversation.id, 901, "A remains valid while widget config waits")
  assert.equal(h.scope.isCustomerSessionScopeCurrent(oldScope), true)
  assert.ok(h.store.getState().socket === oldSocket)
  assert.equal(h.requests.filter((item) => item.path === "/api/customer/session_exchange").length, 0)
  writeSession(h, null)
  widgetPending.resolve(presentation)
  await h.flush()
  assert.ok(dispatchState, "fresh exchange was dispatched")
  assert.equal(dispatchState.conversation, null, "A transcript must be cleared before proofless exchange dispatch")
  assert.equal(dispatchState.messages.length, 0)
  assert.equal(dispatchState.customer, null)
  assert.equal(dispatchState.socket, null)
  assert.equal(dispatchScopeCurrent, false)
  assertDeparted(h, oldScope, oldSocket)
  const exchanges = h.requests.filter((item) => item.path === "/api/customer/session_exchange")
  assert.equal(exchanges.length, 1)
  assert.equal("X-Customer-Session-Token" in exchanges[0].options.headers, false)
  exchangePending.reject(new Error("fresh bootstrap rejected"))
  await h.flush()
  const state = h.store.getState()
  assert.equal(state.customer, null)
  assert.equal(state.conversation, null)
  assert.equal(state.messages.length, 0)
  assert.equal(state.socket, null)
  assert.equal(state.initialized, false)
  assert.equal(state.status, "disconnected")
  assert.equal(state.error, "supportChat.initFailed")
  assert.equal(h.scope.isCustomerSessionScopeCurrent(oldScope), false)
  assert.equal(h.requests.filter((item) => item.path === "/api/customer/session_exchange").length, 1)
})

test("matching still-unexpired proof rejection performs one exchange without fresh fallback", async () => {
  const h = await initializedA({ exchange: async () => { throw new Error("presented proof rejected") } })
  const before = h.store.getState(), oldScope = h.scope.captureCustomerSessionScope()
  writeSession(h, session({ expiresAt: "2026-10-07T01:00:03Z" }))
  h.store.getState().bootstrap()
  await h.flush()
  assert.equal(h.requests.filter((item) => item.path === "/api/customer/session_exchange").length, 1)
  assert.equal(h.scope.isCustomerSessionScopeCurrent(oldScope), true)
  assert.equal(h.store.getState().conversation, before.conversation)
  assert.equal(h.store.getState().messages, before.messages)
  assert.equal(h.store.getState().customer?.id, 101)
  assert.equal(h.store.getState().error, "supportChat.initFailed")
})

test("deliberate close and hide with usable proof do not create a fresh identity", async () => {
  const h = await initializedA()
  const oldScope = h.scope.captureCustomerSessionScope(), before = h.store.getState()
  h.store.getState().setIsOpen(false)
  h.store.getState().setIsVisible(false)
  h.store.getState().bootstrap()
  await h.flush()
  assert.equal(h.scope.isCustomerSessionScopeCurrent(oldScope), true)
  assert.equal(h.store.getState().conversation, before.conversation)
  assert.equal(h.store.getState().messages, before.messages)
  assert.equal(h.store.getState().isOpen, false)
  assert.equal(h.store.getState().isVisible, false)
  h.store.getState().setIsOpen(true)
  h.store.getState().bootstrap()
  await h.flush()
  assert.equal(h.scope.isCustomerSessionScopeCurrent(oldScope), true)
  assert.equal(h.requests.filter((item) => item.path === "/api/customer/session_exchange").length, 0)
})

test("widget channel change departs identity and starts a ticket before exchange", async () => {
  const pending = deferred()
  let changed = false
  const h = await initializedA({ widget: async () => ({ ...presentation, ...(changed ? { channelId: "channel-D" } : {}) }), exchange: async () => pending.promise })
  const oldScope = h.scope.captureCustomerSessionScope(), oldSocket = h.store.getState().socket
  const attempts = observeAttempts(h)
  changed = true
  h.store.getState().bootstrap()
  await h.flush()
  assertDeparted(h, oldScope, oldSocket, "channel-D")
  assert.equal(attempts.length, 2)
  assert.equal(h.scope.isCustomerSessionAttemptCurrent(attempts[0]), false)
  assert.equal(h.scope.isCustomerSessionAttemptCurrent(attempts[1]), true)
  const exchange = h.requests.find((item) => item.path === "/api/customer/session_exchange")
  assert.equal("X-Customer-Session-Token" in exchange.options.headers, false)
  pending.resolve(session({ customer: { id: 202, name: "B" } }))
  await h.flush()
  assert.equal(h.store.getState().customerChannelId, "channel-D")
})

test("superseded widget result cannot change the winner channel or presentation", async () => {
  const pending = deferred()
  let widgetCalls = 0
  const h = await initializedA({ widget: async () => (++widgetCalls === 2 ? pending.promise : presentation) })
  h.store.getState().bootstrap()
  h.store.getState().bootstrap()
  await h.flush()
  pending.resolve({ channelId: "channel-D", title: "stale widget" })
  await h.flush()
  assert.equal(h.store.getState().title, presentation.title)
  assert.equal(h.store.getState().conversation.id, 901)
  assert.equal(h.requests.filter((item) => item.path === "/api/customer/session_exchange").length, 0)
})

test("losing bootstrap rejection cannot overwrite accepted winner state", async () => {
  const older = deferred(), winner = deferred()
  let exchanges = 0
  const h = await initializedA({ exchange: async () => (++exchanges === 1 ? older.promise : winner.promise) })
  writeSession(h, session({ expiresAt: "2026-10-07T01:00:03Z" }))
  h.store.getState().bootstrap()
  await h.flush()
  h.store.getState().bootstrap()
  await h.flush()
  winner.resolve(session({ customer: { id: 202, name: "B" } }))
  await h.flush()
  const accepted = h.store.getState()
  assert.equal(accepted.conversation?.id, 911, "winner B conversation is loaded")
  older.reject(new Error("older exchange rejected"))
  await h.flush()
  assert.equal(h.store.getState().conversation, accepted.conversation)
  assert.equal(h.store.getState().messages, accepted.messages)
  assert.equal(h.store.getState().customer?.id, 202)
  assert.equal(h.store.getState().error, "")
})
