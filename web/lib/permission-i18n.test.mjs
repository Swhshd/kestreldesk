import assert from "node:assert/strict"
import test from "node:test"
import ts from "typescript"
import { readFile } from "node:fs/promises"
import vm from "node:vm"

async function loadModule() {
  const source = await readFile(new URL("./permission-i18n.ts", import.meta.url), "utf8")
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      target: ts.ScriptTarget.ES2017,
      module: ts.ModuleKind.CommonJS,
    },
    fileName: "permission-i18n.ts",
  })
  const sandbox = {
    exports: {},
    module: { exports: {} },
  }
  sandbox.exports = sandbox.module.exports
  vm.runInNewContext(compiled.outputText, sandbox)
  return sandbox.module.exports
}

test("localizes seeded permission display names to English", async () => {
  const { getPermissionDisplayName, getPermissionGroupName } = await loadModule()

  assert.equal(getPermissionDisplayName("user.assignRole", "\u5206\u914d\u7528\u6237\u89d2\u8272", "en-US"), "Assign user roles")
  assert.equal(getPermissionDisplayName("agentTeamSchedule.batchGenerate", "\u6279\u91cf\u751f\u6210\u5ba2\u670d\u7ec4\u6392\u73ed", "en-US"), "Batch generate agent team schedules")
  assert.equal(getPermissionGroupName("agentTeamSchedule", "en-US"), "Agent team schedules")
})

test("keeps original permission names for Chinese locale", async () => {
  const { getPermissionDisplayName, getPermissionGroupName } = await loadModule()

  assert.equal(getPermissionDisplayName("user.view", "\u67e5\u770b\u7528\u6237", "zh-CN"), "\u67e5\u770b\u7528\u6237")
  assert.equal(getPermissionGroupName("agentTeam", "zh-CN"), "agentTeam")
})
