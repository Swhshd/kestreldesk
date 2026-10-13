import assert from "node:assert/strict"
import test from "node:test"
import { readFile } from "node:fs/promises"
import { fileURLToPath } from "node:url"
import vm from "node:vm"
import ts from "typescript"

async function loadHelper() {
  const sourcePath = fileURLToPath(new URL("./support-consultation-entry.ts", import.meta.url))
  const source = await readFile(sourcePath, "utf8")
  const compiled = ts.transpileModule(source, {
    compilerOptions: { target: ts.ScriptTarget.ES2017, module: ts.ModuleKind.CommonJS },
    fileName: sourcePath,
  })
  const sandbox = { exports: {}, module: { exports: {} } }
  sandbox.exports = sandbox.module.exports
  vm.runInNewContext(compiled.outputText, sandbox)
  return sandbox.module.exports
}

test("support consultation includes the configured channel as an encoded explicit query", async () => {
  const { getSupportConsultationHref } = await loadHelper()
  assert.equal(getSupportConsultationHref(" web channel/一 "), "/support/chat?channelId=web%20channel%2F%E4%B8%80")
})

test("support consultation has no chat destination when no default is configured", async () => {
  const { getSupportConsultationHref } = await loadHelper()
  assert.equal(getSupportConsultationHref(), null)
  assert.equal(getSupportConsultationHref("  "), null)
})
