import assert from "node:assert/strict"
import test from "node:test"
import { readFile } from "node:fs/promises"

const read = (path) => readFile(new URL(path, import.meta.url), "utf8")

test("support home renders a non-link fallback until a configured chat destination exists", async () => {
  const [page, zh, en] = await Promise.all([
    read("./page-client.tsx"),
    read("../../../messages/zh-CN.json").then(JSON.parse),
    read("../../../messages/en-US.json").then(JSON.parse),
  ])

  assert.match(page, /supportPublic\.home\.chatUnavailable/)
  assert.match(page, /useState<string \| null \| undefined>\(undefined\)/)
  assert.match(page, /href=\{consultationHref \?\? undefined\}/)
  assert.match(page, /if \(!href\)\s*\{\s*return <div aria-disabled="true"/)
  assert.doesNotMatch(page, /useState\("\/support\/chat"\)/)
  assert.doesNotMatch(page, /getSupportConsultationHref\(\)/)
  assert.equal(zh.supportPublic.home.chatUnavailable, "在线咨询暂未配置，请使用其他支持方式。")
  assert.equal(en.supportPublic.home.chatUnavailable, "Live support is not currently configured. Please use another support option.")
})
