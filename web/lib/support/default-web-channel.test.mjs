import assert from "node:assert/strict"
import test from "node:test"
import { readFile } from "node:fs/promises"
import { fileURLToPath } from "node:url"
import path from "node:path"
import vm from "node:vm"
import ts from "typescript"

async function loadHelper() {
  const sourcePath = fileURLToPath(new URL("./default-web-channel.ts", import.meta.url))
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

test("default Web channel options include only identified, enabled Web channels", async () => {
  const { getEligibleDefaultWebChannels } = await loadHelper()
  const channels = [
    { channelId: "web-A", name: "Web A", channelType: "web", status: 0 },
    { channelId: "web-disabled", name: "Disabled", channelType: "web", status: 1 },
    { channelId: "wx-work", name: "WeCom", channelType: "wxwork_kf", status: 0 },
    { channelId: "", name: "Missing ID", channelType: "web", status: 0 },
    { channelId: "web-B", name: "Web B", channelType: "web", status: 0 },
  ]

  assert.deepEqual(
    JSON.parse(JSON.stringify(getEligibleDefaultWebChannels(channels))),
    [channels[0], channels[4]]
  )
})

test("no eligible Web channels produces an empty selection list", async () => {
  const { getEligibleDefaultWebChannels } = await loadHelper()
  assert.deepEqual(getEligibleDefaultWebChannels([]), [])
})
