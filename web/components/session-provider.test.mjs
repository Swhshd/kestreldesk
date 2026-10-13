import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import test from "node:test"
import vm from "node:vm"
import ts from "../node_modules/.pnpm/typescript@6.0.3/node_modules/typescript/lib/typescript.js"

const sourcePath = new URL("./session-provider.tsx", import.meta.url)
const source = await readFile(sourcePath, "utf8")

function renderSessionProvider({ stored, profile, profileError } = {}) {
  const calls = { writes: [], clears: 0, fetches: 0 }
  let sessionStorage = stored ?? null
  const states = []
  const context = { Provider: Symbol("SessionContext.Provider") }
  const react = {
    createContext: () => context,
    useCallback: (fn) => fn,
    useContext: (value) => value,
    useEffect: () => {},
    useState: (initial) => {
      const index = states.length
      states.push(initial)
      return [states[index], (value) => { states[index] = value }]
    },
  }
  const jsxRuntime = {
    jsx: (type, props) => ({ type, props }),
    jsxs: (type, props) => ({ type, props }),
  }
  const mocks = {
    react,
    "react/jsx-runtime": jsxRuntime,
    "@/lib/api/auth": {
      fetchProfile: async () => {
        calls.fetches += 1
        if (profileError) throw profileError
        return profile
      },
      logout: async () => {},
    },
    "@/lib/auth": {
      AUTH_SESSION_CHANGED_EVENT: "session-changed",
      AUTH_SESSION_EXPIRED_EVENT: "session-expired",
      clearSession: () => { calls.clears += 1; sessionStorage = null },
      readSession: () => sessionStorage,
      writeSession: (value) => { calls.writes.push(value); sessionStorage = value },
    },
  }
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      jsx: ts.JsxEmit.ReactJSX,
      target: ts.ScriptTarget.ES2022,
      esModuleInterop: true,
    },
  }).outputText
  const moduleRecord = { exports: {} }
  const require = (name) => {
    assert.ok(name in mocks, `unexpected import: ${name}`)
    return mocks[name]
  }
  vm.runInNewContext(`(function(exports, require, moduleRecord) { ${compiled}\n})`, { Promise, Error })(
    moduleRecord.exports,
    require,
    moduleRecord,
  )
  const element = moduleRecord.exports.SessionProvider({ children: "synthetic-child" })
  return { value: element.props.value, calls, getSession: () => sessionStorage, getSessionState: () => states[0] }
}

test("refreshSession characterization preserves stored token and expiry", async () => {
  const stored = {
    accessToken: "synthetic-token",
    expiresAt: "synthetic-expiry",
    user: { id: 7 },
    permissions: ["old"],
    roles: ["old-role"],
  }
  const provider = renderSessionProvider({
    stored,
    profile: { user: { id: 7 }, permissions: ["new"], roles: ["new-role"] },
  })
  await provider.value.refreshSession()
  assert.deepEqual(JSON.parse(JSON.stringify(provider.calls.writes[0])), {
    ...stored,
    user: { id: 7 },
    permissions: ["new"],
    roles: ["new-role"],
  })
  assert.equal(provider.calls.fetches, 1)
})

test("valid profile returns valid after writing refreshed permissions", async () => {
  const provider = renderSessionProvider({
    stored: { accessToken: "synthetic-token", expiresAt: "synthetic-expiry", user: { id: 7 } },
    profile: { user: { id: 7 }, permissions: ["new"], roles: ["agent"] },
  })
  assert.equal(await provider.value.validateRealtimeProfile(), "valid")
  assert.deepEqual(provider.calls.writes[0].permissions, ["new"])
  assert.equal(provider.calls.writes[0].accessToken, "synthetic-token")
  assert.equal(provider.calls.writes[0].expiresAt, "synthetic-expiry")
})

test("explicit session errors return invalid and clear stored session", async () => {
  for (const errorCode of [3000, 3002]) {
    const provider = renderSessionProvider({
      stored: { accessToken: "synthetic-token", user: { id: 7 } },
      profileError: Object.assign(new Error("synthetic auth error"), { errorCode }),
    })
    assert.equal(await provider.value.validateRealtimeProfile(), "invalid")
    assert.equal(provider.calls.clears, 1)
    assert.equal(provider.getSessionState(), null)
  }
})

test("transient profile error retains stored session and returns transient", async () => {
  const stored = { accessToken: "synthetic-token", user: { id: 7 } }
  const provider = renderSessionProvider({
    stored,
    profileError: new Error("synthetic network error"),
  })
  assert.equal(await provider.value.validateRealtimeProfile(), "transient")
  assert.deepEqual(JSON.parse(JSON.stringify(provider.getSession())), stored)
  assert.deepEqual(JSON.parse(JSON.stringify(provider.getSessionState())), stored)
  assert.equal(provider.calls.clears, 0)
})

test("missing stored session returns invalid without fetching", async () => {
  const provider = renderSessionProvider()
  assert.equal(await provider.value.validateRealtimeProfile(), "invalid")
  assert.equal(provider.calls.fetches, 0)
})
