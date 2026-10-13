import assert from "node:assert/strict"
import test from "node:test"
import ts from "typescript"
import { readFile } from "node:fs/promises"
import vm from "node:vm"

async function loadDemoNavigation() {
  const source = await readFile(new URL("./demo-navigation.ts", import.meta.url), "utf8")
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      target: ts.ScriptTarget.ES2017,
      module: ts.ModuleKind.CommonJS,
    },
    fileName: "demo-navigation.ts",
  })
  const sandbox = {
    exports: {},
    module: { exports: {} },
  }
  sandbox.exports = sandbox.module.exports
  vm.runInNewContext(compiled.outputText, sandbox)
  return sandbox.module.exports
}

test("widget demo lives below support so /support remains available", async () => {
  const { getWidgetDemoPath } = await loadDemoNavigation()

  assert.equal(getWidgetDemoPath(), "/support/demo")
})
