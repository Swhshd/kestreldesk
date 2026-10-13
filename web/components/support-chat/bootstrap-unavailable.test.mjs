import assert from "node:assert/strict"
import test from "node:test"
import { readFile } from "node:fs/promises"

const read = (path) => readFile(new URL(path, import.meta.url), "utf8")

test("channel bootstrap unavailable state hides the entire message editor", async () => {
  const source = await read("./chat-shell.tsx")

  assert.match(source, /bootstrapUnavailable/)
  assert.match(source, /\{bootstrapUnavailable\s*\?\s*\([\s\S]*?\)\s*:\s*\([\s\S]*?<CustomerMessageEditor/)
  assert.match(source, /supportChat\.unavailableTitle/)
  assert.match(source, /supportChat\.unavailableDescription/)
  assert.match(source, /supportChat\.retryBootstrap/)
  assert.match(source, /supportChat\.returnToSupport/)
})

test("terminal channel-unavailable copy is localized", async () => {
  const [zh, en] = await Promise.all([
    read("../../messages/zh-CN.json").then(JSON.parse),
    read("../../messages/en-US.json").then(JSON.parse),
  ])

  assert.equal(zh.supportChat.unavailableTitle, "在线咨询暂不可用")
  assert.equal(zh.supportChat.unavailableDescription, "当前在线客服入口不可用，请稍后重试或返回支持中心。")
  assert.equal(zh.supportChat.retryBootstrap, "重新尝试")
  assert.equal(zh.supportChat.returnToSupport, "返回支持中心")
  assert.equal(en.supportChat.unavailableTitle, "Live support is unavailable")
  assert.equal(en.supportChat.unavailableDescription, "This support entry is unavailable. Try again later or return to the Support Center.")
  assert.equal(en.supportChat.retryBootstrap, "Try again")
  assert.equal(en.supportChat.returnToSupport, "Back to Support Center")
})
