import assert from "node:assert/strict"
import test from "node:test"
import { readFile } from "node:fs/promises"

async function loadMessages(locale) {
  const source = await readFile(new URL(`../messages/${locale}.json`, import.meta.url), "utf8")
  return JSON.parse(source)
}

function readPath(messages, key) {
  return key.split(".").reduce((value, part) => value?.[part], messages)
}

test("support navigation and mobile menu labels resolve in both locales", async () => {
  const keys = [
    "nav.supportDocs",
    "nav.supportCommunity",
    "nav.supportCommunityCategories",
    "nav.supportConfig",
    "supportPublic.a11y.openMenu",
    "supportPublic.a11y.closeMenu",
  ]

  for (const locale of ["zh-CN", "en-US"]) {
    const messages = await loadMessages(locale)

    for (const key of keys) {
      const value = readPath(messages, key)
      assert.equal(typeof value, "string", `${locale} must define ${key}`)
      assert.ok(value.trim().length > 0, `${locale} must not leave ${key} blank`)
      assert.notEqual(value, key, `${locale} must not render ${key} literally`)
    }
  }
})
