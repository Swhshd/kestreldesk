import { readFileSync, existsSync } from "node:fs"
import { createRequire } from "node:module"
import path from "node:path"
import { fileURLToPath } from "node:url"
import vm from "node:vm"
import ts from "typescript"

const webRoot = fileURLToPath(new URL("../../", import.meta.url))
const requireDependency = createRequire(new URL("../../package.json", import.meta.url))

export function deferred() {
  let resolve, reject
  const promise = new Promise((accept, fail) => { resolve = accept; reject = fail })
  return { promise, resolve, reject }
}

function memoryStorage() {
  const values = new Map()
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, String(value)),
    removeItem: (key) => values.delete(key),
    clear: () => values.clear(),
  }
}

async function loadHarness({ config, session, request, now, scopeControl }, withStore) {
  if (scopeControl !== "normal" && scopeControl !== "bypass") {
    throw new Error("Choose an explicit harness scope control")
  }
  const requests = [], sockets = [], timers = new Map(), modules = new Map()
  const realtimeDeliveries = [], notifications = []
  const notificationPermission = deferred()
  let deferRealtime = false, focusCount = 0
  let timerId = 0, uuidId = 0, runtimeConfig = config
  const window = {
    localStorage: memoryStorage(), sessionStorage: memoryStorage(),
    location: { origin: "https://widget.test", protocol: "https:", host: "widget.test", search: "" },
    setTimeout: (callback) => { timers.set(++timerId, callback); return timerId },
    clearTimeout: (id) => timers.delete(id),
    setInterval: (callback) => { timers.set(++timerId, callback); return timerId },
    clearInterval: (id) => timers.delete(id),
    focus() { focusCount += 1 },
  }
  window.self = window.top = window
  if (session !== null) {
    window.sessionStorage.setItem("cs_ai_agent_customer_session", JSON.stringify(session))
  }
  class FixedDate extends Date {
    constructor(...args) { super(...(args.length ? args : [now])) }
    static now() { return now }
  }
  class FakeSocket {
    static CONNECTING = 0
    static OPEN = 1
    static CLOSING = 2
    static CLOSED = 3
    readyState = FakeSocket.CONNECTING
    listeners = new Map()
    sent = []
    closed = false
    constructor(url) { this.url = url; sockets.push(this) }
    addEventListener(type, callback) {
      const callbacks = this.listeners.get(type) ?? []
      callbacks.push(callback); this.listeners.set(type, callbacks)
    }
    emit(type, event = {}) {
      if (type === "open") this.readyState = FakeSocket.OPEN
      if (type === "close") this.readyState = FakeSocket.CLOSED
      for (const callback of this.listeners.get(type) ?? []) callback(event)
    }
    send(value) { this.sent.push(value) }
    close() { this.closed = true; this.readyState = FakeSocket.CLOSED; this.emit("close") }
  }
  class FakeNotification {
    static permission = "default"
    static requestPermission() { return notificationPermission.promise }
    constructor(title, options) { this.title = title; this.options = options; notifications.push(this) }
    close() { this.closed = true }
  }
  window.Notification = FakeNotification
  const document = { visibilityState: "visible" }
  const stubs = {
    "@/lib/api/client": { request: async (apiPath, options) => {
      requests.push({ path: apiPath, options })
      return request(apiPath, options)
    } },
    "@/i18n/messages": { translateCurrentMessage: (key) => key },
    "@/lib/utils": { generateUUID: () => `fixture-${++uuidId}` },
    "@/lib/sdk/runtime-config": {
      readSupportChatRuntimeConfig: () => runtimeConfig,
      setSupportChatRuntimeConfig: (next) => { runtimeConfig = next },
    },
  }
  const context = vm.createContext({
    window, document, Notification: FakeNotification, Date: FixedDate,
    WebSocket: FakeSocket, URLSearchParams, URL, FormData, Headers, Response,
    console, process: { env: {} },
    setTimeout: window.setTimeout, clearTimeout: window.clearTimeout,
    setInterval: window.setInterval, clearInterval: window.clearInterval,
  })
  function loadModule(filename) {
    if (modules.has(filename)) return modules.get(filename).exports
    const moduleRecord = { exports: {} }
    modules.set(filename, moduleRecord)
    const source = readFileSync(filename, "utf8")
    const compiled = ts.transpileModule(source, {
      fileName: filename,
      compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, esModuleInterop: true },
    }).outputText
    const localRequire = (specifier) => {
      if (stubs[specifier]) return stubs[specifier]
      if (!specifier.startsWith("@/") && !specifier.startsWith(".")) return requireDependency(specifier)
      const target = specifier.startsWith("@/")
        ? path.join(webRoot, specifier.slice(2))
        : path.resolve(path.dirname(filename), specifier)
      return loadModule(existsSync(target) ? target : `${target}.ts`)
    }
    new vm.Script(`(function(require,moduleRecord,exports){${compiled}\n})`, { filename })
      .runInContext(context)(localRequire, moduleRecord, moduleRecord.exports)
    // Hold delivery after the real manager accepts a socket event, so the store
    // must still reject a queued callback after identity departure/disconnect.
    if (filename.endsWith("realtime-connection.ts")) {
      const createManager = moduleRecord.exports.createRealtimeConnectionManager
      moduleRecord.exports.createRealtimeConnectionManager = (options) => createManager({
        ...options,
        onMessage: (event, socket) => {
          const deliver = () => options.onMessage?.(event, socket)
          if (deferRealtime) realtimeDeliveries.push(deliver)
          else deliver()
        },
      })
    }
    if (scopeControl === "normal" && process.env.CUSTOMER_SESSION_SCOPE_CONTROL === "always-current" && filename.endsWith("im-session-scope.ts")) {
      // Test-only sensitivity mutation; attempt generation remains real.
      moduleRecord.exports.isCustomerSessionScopeCurrent = () => true
    }
    if (scopeControl === "bypass" && filename.endsWith("im-session-scope.ts")) {
      moduleRecord.exports.isCustomerSessionScopeCurrent = () => true
      moduleRecord.exports.isCustomerSessionAttemptCurrent = () => true
    }
    return moduleRecord.exports
  }
  const im = loadModule(path.join(webRoot, "lib/api/im.ts"))
  const result = { im, window, requests, document, notifications, notificationPermission,
    Notification: FakeNotification, realtimeDeliveries,
    deferRealtime: () => { deferRealtime = true }, focusCount: () => focusCount,
    runTimers: () => { const callbacks = [...timers.values()]; timers.clear(); callbacks.forEach((callback) => callback()) },
  }
  // Expose the actual shared scope module through the same VM/cache as API and store.
  const scopePath = path.join(webRoot, "lib/api/im-session-scope.ts")
  if (existsSync(scopePath)) result.scope = loadModule(scopePath)
  if (withStore) {
    result.store = loadModule(path.join(webRoot, "lib/stores/support-chat.ts")).useSupportChatStore
    result.sockets = sockets
    result.flush = async () => { for (let i = 0; i < 30; i++) await Promise.resolve() }
  }
  return result
}

export async function loadImHarness(options) { return loadHarness(options, false) }
export async function loadSupportChatHarness(options) { return loadHarness(options, true) }
