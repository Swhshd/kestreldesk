import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import test from "node:test"
import ts from "../../../../node_modules/.pnpm/typescript@6.0.3/node_modules/typescript/lib/typescript.js"

const routePath = new URL("./page.tsx", import.meta.url)
const route = await readFile(routePath, "utf8")
const sourceFile = ts.createSourceFile("page.tsx", route, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
let effectText
function findReconnectEffect(node) {
  if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && node.expression.text === "useEffect") {
    const callback = node.arguments[0]
    if (callback && callback.getText(sourceFile).includes("scheduleReconnect") && callback.getText(sourceFile).includes("connect()")) {
      effectText = callback.getText(sourceFile)
    }
  }
  ts.forEachChild(node, findReconnectEffect)
}
findReconnectEffect(sourceFile)
assert.ok(effectText, "extract the route's actual WebSocket reconnect effect with TypeScript AST")
const compiledEffect = ts.transpile(`const extractedEffect = ${effectText};`, { target: ts.ScriptTarget.ES2022 })

async function flushPromises() {
  for (let index = 0; index < 6; index += 1) await Promise.resolve()
}

class MonitorSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSED = 3
  readyState = MonitorSocket.CONNECTING
  onopen = null
  onmessage = null
  onclose = null
  sent = []
  constructor() { MonitorSocket.instances.push(this) }
  close() { this.readyState = MonitorSocket.CLOSED }
  send(value) { this.sent.push(value) }
  emit(type, event = {}) { this[`on${type}`]?.(event) }
  fail() { this.onclose?.({ code: 1006 }) }
}
MonitorSocket.instances = []

function setup(validator) {
  const timers = new Map()
  let next = 1
  globalThis.WebSocket = MonitorSocket
  globalThis.window = {
    setTimeout(callback, delay) { const id = next++; timers.set(id, { callback, delay }); return id },
    clearTimeout(id) { timers.delete(id) },
    setInterval() { return next++ },
    clearInterval() {},
  }
  MonitorSocket.instances = []
  const current = (value = null) => ({ current: value })
  const websocketRef = current()
  const reconnectTimerRef = current()
  const pingTimerRef = current()
  const reconnectAttemptRef = current(0)
  const detailItemRef = current()
  const subscribedConversationIdRef = current()
  let validations = 0
  const validateRealtimeProfile = validator ? async () => { validations++; return validator() } : undefined
  const t = (key) => key
  const loadConversations = async () => {}
  const loadDetail = async () => {}
  const toast = { error() {} }
  const createAdminWebSocketUrl = () => "ws://synthetic"
  const makeEffect = new Function(
    "websocketRef", "reconnectTimerRef", "pingTimerRef", "reconnectAttemptRef",
    "detailItemRef", "subscribedConversationIdRef", "validateRealtimeProfile",
    "t", "loadConversations", "loadDetail", "toast", "createAdminWebSocketUrl",
    "WebSocket", "window", "RECONNECT_BASE_DELAY", "RECONNECT_MAX_DELAY", `${compiledEffect}\nreturn extractedEffect;`,
  )(
    websocketRef, reconnectTimerRef, pingTimerRef, reconnectAttemptRef,
    detailItemRef, subscribedConversationIdRef, validateRealtimeProfile,
    t, loadConversations, loadDetail, toast, createAdminWebSocketUrl,
    MonitorSocket, globalThis.window, 2000, 30000,
  )
  const cleanup = makeEffect()
  const fireRetry = async () => {
    const entry = timers.entries().next().value
    assert.ok(entry, "a Monitor retry timer is pending")
    timers.delete(entry[0])
    entry[1].callback()
    await flushPromises()
  }
  return { timers, cleanup, fireRetry, get validations() { return validations }, sockets: MonitorSocket.instances }
}

test("invalid profile stops Monitor reconnect and cancels retry", async () => {
  const env = setup(async () => "invalid")
  env.sockets[0].fail()
  await flushPromises()
  assert.equal(env.validations, 1)
  assert.equal(env.timers.size, 0)
  assert.equal(env.sockets.length, 1)
  env.cleanup()
})

test("Monitor wires the shared AuthProvider profile validator", () => {
  assert.match(route, /import\s*\{\s*useAuth\s*\}\s*from\s*"@\/components\/auth-provider"/)
  assert.match(route, /const\s*\{\s*validateRealtimeProfile\s*\}\s*=\s*useAuth\(\)/)
  assert.match(effectText, /validateRealtimeProfile\(\)/)
})

test("transient Monitor validation preserves retry schedule", async () => {
  const env = setup(async () => "transient")
  env.sockets[0].fail()
  await flushPromises()
  assert.equal(env.timers.size, 1)
  assert.equal([...env.timers.values()][0].delay, 2000)
  env.cleanup()
})

test("valid Monitor profile schedules and starts the next socket", async () => {
  const env = setup(async () => "valid")
  env.sockets[0].fail()
  await flushPromises()
  assert.equal(env.timers.size, 1)
  assert.equal([...env.timers.values()][0].delay, 2000)
  await env.fireRetry()
  assert.equal(env.sockets.length, 2)
  env.cleanup()
})

test("Monitor error and repeated close signals share one pending validation cycle", async () => {
  let resolve
  const pending = new Promise((done) => { resolve = done })
  const env = setup(() => pending)
  env.sockets[0].emit("error", { error: "synthetic" })
  env.sockets[0].fail()
  env.sockets[0].fail()
  await flushPromises()
  assert.equal(env.validations, 1)
  assert.equal(env.timers.size, 0)
  resolve("valid")
  await flushPromises()
  assert.equal(env.timers.size, 1)
  assert.equal(env.validations, 1)
  env.cleanup()
})

test("Monitor unmount cancels unresolved validation and pending timer", async () => {
  let resolve
  const pending = new Promise((done) => { resolve = done })
  const env = setup(() => pending)
  env.sockets[0].fail()
  env.cleanup()
  resolve("valid")
  await flushPromises()
  assert.equal(env.timers.size, 0)
  assert.equal(env.sockets.length, 1)
})

test("Monitor unmount cancels an already-pending retry timer", async () => {
  const env = setup(async () => "valid")
  env.sockets[0].fail()
  await flushPromises()
  assert.equal(env.timers.size, 1)
  env.cleanup()
  assert.equal(env.timers.size, 0)
  assert.equal(env.sockets.length, 1)
})
