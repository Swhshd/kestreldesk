import assert from "node:assert/strict"
import test from "node:test"
import ts from "typescript"
import { readFile } from "node:fs/promises"
import vm from "node:vm"

async function loadModule() {
  const source = await readFile(new URL("./role-i18n.ts", import.meta.url), "utf8")
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      target: ts.ScriptTarget.ES2017,
      module: ts.ModuleKind.CommonJS,
    },
    fileName: "role-i18n.ts",
  })
  const sandbox = {
    exports: {},
    module: { exports: {} },
  }
  sandbox.exports = sandbox.module.exports
  vm.runInNewContext(compiled.outputText, sandbox)
  return sandbox.module.exports
}

test("localizes seeded role names to English by role code", async () => {
  const { getRoleDisplayName } = await loadModule()

  assert.equal(getRoleDisplayName("super_admin", "\u8d85\u7ea7\u7ba1\u7406\u5458", "en-US"), "Super admin")
  assert.equal(getRoleDisplayName("cs_team_leader", "\u5ba2\u670d\u7ec4\u957f", "en-US"), "Support team lead")
  assert.equal(getRoleDisplayName("cs_user", "\u5ba2\u670d", "en-US"), "Support agent")
})

test("keeps custom or Chinese role names unchanged", async () => {
  const { getRoleDisplayName } = await loadModule()

  assert.equal(getRoleDisplayName("custom", "Ops reviewer", "en-US"), "Ops reviewer")
  assert.equal(getRoleDisplayName("super_admin", "\u8d85\u7ea7\u7ba1\u7406\u5458", "zh-CN"), "\u8d85\u7ea7\u7ba1\u7406\u5458")
})
