import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import Module from "node:module"
import path from "node:path"
import test from "node:test"
import ts from "typescript"
import { fileURLToPath } from "node:url"

const moduleDirectory = path.dirname(fileURLToPath(import.meta.url))

async function loadApiModule(filename) {
  const filePath = path.join(moduleDirectory, filename)
  const source = await readFile(filePath, "utf8")
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.CommonJS,
      target: ts.ScriptTarget.ES2022,
      esModuleInterop: true,
    },
  })
  const requests = []
  const sessionWrites = []
  const sessionReads = []
  const request = async (apiPath, options) => {
    requests.push({ apiPath, options })
    return { message: "If the account exists, we will send a password reset email to its associated address." }
  }
  const loadedModule = new Module(filePath)
  loadedModule.filename = filePath
  loadedModule.paths = Module._nodeModulePaths(path.dirname(filePath))
  const nativeRequire = loadedModule.require.bind(loadedModule)
  loadedModule.require = (specifier) => {
    if (specifier === "@/lib/api/client") return { request }
    if (specifier === "@/lib/auth") {
      return {
        clearSession: () => {},
        readSession: () => {
          sessionReads.push(true)
          return null
        },
        writeSession: (value) => sessionWrites.push(value),
      }
    }
    return nativeRequire(specifier)
  }
  loadedModule._compile(compiled.outputText, filePath)
  return { api: loadedModule.exports, requests, sessionWrites, sessionReads }
}

const expectedPaths = [
  {
    file: "auth.ts",
    requestPath: "/api/auth/password_reset/request",
    resetPath: "/api/auth/password_reset/reset",
  },
  {
    file: "support.ts",
    requestPath: "/api/support/auth/password_reset/request",
    resetPath: "/api/support/auth/password_reset/reset",
  },
]

for (const surface of expectedPaths) {
  test(`${surface.file} sends public recovery requests to its own API surface`, async () => {
    const { api, requests, sessionWrites, sessionReads } = await loadApiModule(surface.file)
    const requestResult = await api.requestPasswordReset({ identifier: "synthetic-account" })
    assert.equal(requestResult.message, "If the account exists, we will send a password reset email to its associated address.")

    const resetPayload = {
      token: "fixture-only-reset-token",
      newPassword: "fixture-only-password",
      confirmPassword: "fixture-only-password",
    }
    await api.resetPassword(resetPayload)

    assert.equal(requests.length, 2, "both recovery actions use the shared API request client")
    assert.equal(requests[0].apiPath, surface.requestPath, "request endpoint belongs to the selected surface")
    assert.equal(requests[1].apiPath, surface.resetPath, "reset endpoint belongs to the selected surface")
    for (const call of requests) {
      assert.equal(call.options.method, "POST", "recovery calls use POST")
      assert.equal(call.options.skipAuth, true, "recovery calls are public and skip auth")
    }
    assert.equal(requests[0].options.body, JSON.stringify({ identifier: "synthetic-account" }), "forgot request sends the identifier DTO")
    const sentReset = JSON.parse(requests[1].options.body)
    assert.ok(sentReset.token === resetPayload.token, "reset forwards the URL token to the matching API")
    assert.ok(sentReset.newPassword === resetPayload.newPassword, "reset forwards the new password")
    assert.ok(sentReset.confirmPassword === resetPayload.confirmPassword, "reset forwards the confirmation")
    assert.equal(sessionWrites.length, 0, "recovery never creates a session")
    assert.equal(sessionReads.length, 0, "public recovery wrappers do not read a session")
  })
}
