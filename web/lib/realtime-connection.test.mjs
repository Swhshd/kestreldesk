import assert from "node:assert/strict"
import test from "node:test"

import { createRealtimeConnectionManager } from "./realtime-connection.ts"

class FakeSocket extends EventTarget {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSED = 3
  readyState = FakeSocket.CONNECTING
  url = "ws://synthetic"
  sent = []
  close() { this.readyState = FakeSocket.CLOSED }
  send(value) { this.sent.push(value) }
  emit(type, detail = {}) {
    this.dispatchEvent(Object.assign(new Event(type), detail))
  }
}

function setup() {
  const timers = new Map()
  const delays = []
  let nextTimer = 1
  globalThis.WebSocket = FakeSocket
  globalThis.window = {
    setTimeout(callback, delay) {
      const id = nextTimer++
      timers.set(id, callback)
      delays.push(delay)
      return id
    },
    clearTimeout(id) { timers.delete(id) },
    setInterval() { return nextTimer++ },
    clearInterval() {},
  }
  const sockets = []
  const createSocket = () => {
    const socket = new FakeSocket()
    sockets.push(socket)
    return socket
  }
  const fireTimer = async () => {
    const entry = timers.entries().next().value
    assert.ok(entry, "a retry timer is pending")
    timers.delete(entry[0])
    entry[1]()
    await flushPromises()
  }
  return { timers, delays, sockets, createSocket, fireTimer }
}

function deferred() {
  let resolve
  const promise = new Promise((done) => { resolve = done })
  return { promise, resolve }
}

async function flushPromises() {
  for (let index = 0; index < 6; index += 1) await Promise.resolve()
}

test("invalid profile stops reconnect and cancels retry", async () => {
  const env = setup()
  const manager = createRealtimeConnectionManager({
    createSocket: env.createSocket,
    beforeReconnect: async () => "invalid",
  })
  manager.connect()
  const before = env.sockets.length
  env.sockets[0].emit("close", { code: 1006 })
  await flushPromises()
  assert.equal(env.sockets.length, before)
  assert.equal(env.timers.size, 0)
})

test("invalid profile closes a socket still open after its error event", async () => {
  const env = setup()
  const manager = createRealtimeConnectionManager({
    createSocket: env.createSocket,
    beforeReconnect: async () => "invalid",
  })
  manager.connect()
  env.sockets[0].readyState = FakeSocket.OPEN
  env.sockets[0].emit("error")
  await flushPromises()
  assert.equal(env.sockets[0].readyState, FakeSocket.CLOSED)
  assert.equal(env.timers.size, 0)
})

test("error and close share one validation cycle", async () => {
  const env = setup()
  let calls = 0
  const gate = deferred()
  const manager = createRealtimeConnectionManager({
    createSocket: env.createSocket,
    beforeReconnect: () => { calls += 1; return gate.promise },
  })
  manager.connect()
  env.sockets[0].emit("error")
  env.sockets[0].emit("close", { code: 1006 })
  await Promise.resolve()
  assert.equal(calls, 1)
  gate.resolve("valid")
  await flushPromises()
  assert.equal(env.delays[0], 2000)
})

test("disconnect cancels an unresolved validation", async () => {
  const env = setup()
  const gate = deferred()
  const manager = createRealtimeConnectionManager({
    createSocket: env.createSocket,
    beforeReconnect: () => gate.promise,
  })
  manager.connect()
  env.sockets[0].emit("close", { code: 1006 })
  manager.disconnect()
  const countAtDisconnect = env.sockets.length
  gate.resolve("valid")
  await flushPromises()
  assert.equal(env.sockets.length, countAtDisconnect)
  assert.equal(env.timers.size, 0)
})

test("valid profile is applied before next socket", async () => {
  const env = setup()
  let profileVersion = 0
  const manager = createRealtimeConnectionManager({
    createSocket: env.createSocket,
    beforeReconnect: async () => { profileVersion = 2; return "valid" },
  })
  manager.connect()
  env.sockets[0].emit("close", { code: 1006 })
  await flushPromises()
  await env.fireTimer()
  assert.equal(profileVersion, 2)
  assert.equal(env.sockets.length, 2)
})

test("transient preserves session and retry", async () => {
  const env = setup()
  const manager = createRealtimeConnectionManager({
    createSocket: env.createSocket,
    beforeReconnect: async () => "transient",
  })
  manager.connect()
  env.sockets[0].emit("close", { code: 1006 })
  await flushPromises()
  assert.equal(env.delays[0], 2000)
  await env.fireTimer()
  assert.equal(env.sockets.length, 2)
})

test("valid and transient preserve exponential backoff and cap", async () => {
  const env = setup()
  const manager = createRealtimeConnectionManager({
    createSocket: env.createSocket,
    beforeReconnect: async () => "transient",
    reconnectBaseDelayMs: 2000,
    reconnectMaxDelayMs: 30000,
  })
  manager.connect()
  for (const expected of [2000, 4000, 8000, 16000, 30000, 30000]) {
    const socket = env.sockets.at(-1)
    socket.emit("close", { code: 1006 })
    await flushPromises()
    assert.equal(env.delays.at(-1), expected)
    await env.fireTimer()
  }
})

test("open resets attempt and next failure gets a new cycle", async () => {
  const env = setup()
  let calls = 0
  const manager = createRealtimeConnectionManager({
    createSocket: env.createSocket,
    beforeReconnect: async () => { calls += 1; return "valid" },
  })
  manager.connect()
  env.sockets[0].emit("close", { code: 1006 })
  await flushPromises()
  await env.fireTimer()
  env.sockets[1].readyState = FakeSocket.OPEN
  env.sockets[1].emit("open")
  env.sockets[1].emit("close", { code: 1006 })
  await flushPromises()
  assert.equal(calls, 2)
  assert.equal(env.delays.at(-1), 2000)
})

test("createSocket throw validates once", async () => {
  const env = setup()
  let calls = 0
  const manager = createRealtimeConnectionManager({
    createSocket() { if (calls === 0) throw new Error("synthetic"); return env.createSocket() },
    beforeReconnect: async () => { calls += 1; return "valid" },
  })
  manager.connect()
  await Promise.resolve()
  assert.equal(calls, 1)
  await flushPromises()
  assert.equal(env.delays[0], 2000)
})

test("late stale socket events cannot gate or restart a newer generation", async () => {
  const env = setup()
  let calls = 0
  const manager = createRealtimeConnectionManager({
    createSocket: env.createSocket,
    beforeReconnect: async () => { calls += 1; return "valid" },
  })
  manager.connect()
  const stale = env.sockets[0]
  stale.emit("close", { code: 1006 })
  await flushPromises()
  await env.fireTimer()
  assert.equal(env.sockets.length, 2)
  stale.emit("error")
  stale.emit("close", { code: 1006 })
  await flushPromises()
  assert.equal(calls, 1)
  assert.equal(env.timers.size, 0)
  env.sockets[1].emit("close", { code: 1006 })
  await flushPromises()
  assert.equal(calls, 2)
})

test("consumer without validator preserves previous schedule", async () => {
  const env = setup()
  const manager = createRealtimeConnectionManager({ createSocket: env.createSocket })
  manager.connect()
  env.sockets[0].emit("close", { code: 1006 })
  assert.equal(env.delays[0], 2000)
  await env.fireTimer()
  assert.equal(env.sockets.length, 2)
})
