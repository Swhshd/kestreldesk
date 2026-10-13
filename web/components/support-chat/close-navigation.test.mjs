import assert from "node:assert/strict"
import test from "node:test"
import ts from "typescript"
import { readFile } from "node:fs/promises"
import vm from "node:vm"

async function loadCloseNavigation() {
  const source = await readFile(new URL("./close-navigation.ts", import.meta.url), "utf8")
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      target: ts.ScriptTarget.ES2017,
      module: ts.ModuleKind.CommonJS,
    },
    fileName: "close-navigation.ts",
  })
  const sandbox = {
    exports: {},
    module: { exports: {} },
  }
  sandbox.exports = sandbox.module.exports
  vm.runInNewContext(compiled.outputText, sandbox)
  return sandbox.module.exports
}

test("standalone close navigates to the non-bootstrapping closed page", async () => {
  const { getStandaloneClosedUrl } = await loadCloseNavigation()

  assert.equal(getStandaloneClosedUrl(), "/support/chat/closed")
})
