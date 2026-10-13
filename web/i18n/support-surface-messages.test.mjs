import assert from "node:assert/strict"
import test from "node:test"
import { readdir, readFile } from "node:fs/promises"
import { join } from "node:path"
import { fileURLToPath } from "node:url"

const locales = ["zh-CN", "en-US"]
const supportPublicRoot = fileURLToPath(new URL("../app/(support)/support", import.meta.url))
const dynamicSupportPublicKeys = [
  "supportPublic.comment.sort.default",
  "supportPublic.comment.sort.latest",
  "supportPublic.comment.sort.hot",
  "supportPublic.help.previous",
  "supportPublic.help.next",
]

const surfaces = [
  {
    name: "support login",
    file: "../app/(support)/support/login/_components/login-page.tsx",
    matches: (key) => key.startsWith("supportPublic.login."),
  },
  {
    name: "create post",
    file: "../app/(support)/support/community/posts/new/_components/create-post.tsx",
    matches: (key) =>
      key.startsWith("supportPublic.createPost.") ||
      key === "supportPublic.actions.publishPost",
  },
  {
    name: "support community category admin",
    file: "../app/(dashboard)/dashboard/support/_components/support-admin.tsx",
    matches: (key) => key.startsWith("supportCommunityCategory."),
  },
  {
    name: "support center dashboard navigation",
    file: "../lib/navigation.tsx",
    pattern: /\btitleKey:\s*"([^"]+)"/g,
    matches: (key) =>
      key === "nav.supportDocs" ||
      key === "nav.supportCommunity" ||
      key === "nav.supportCommunityCategories" ||
      key === "nav.supportConfig",
  },
]

async function loadMessages(locale) {
  const source = await readFile(
    new URL(`../messages/${locale}.json`, import.meta.url),
    "utf8",
  )
  return JSON.parse(source)
}

async function referencedKeys(surface) {
  const source = await readFile(new URL(surface.file, import.meta.url), "utf8")
  return [...source.matchAll(surface.pattern ?? /\bt\("([^"]+)"\)/g)]
    .map((match) => match[1])
    .filter(surface.matches)
}

async function supportSources(directory = supportPublicRoot) {
  const entries = await readdir(directory, { withFileTypes: true })
  const sources = await Promise.all(entries.map(async (entry) => {
    const path = join(directory, entry.name)
    if (entry.isDirectory()) return supportSources(path)
    if (!/\.(?:ts|tsx|js|jsx)$/.test(entry.name)) return []
    return [await readFile(path, "utf8")]
  }))
  return sources.flat()
}

async function supportPublicKeys() {
  const keys = new Set(dynamicSupportPublicKeys)
  for (const source of await supportSources()) {
    for (const match of source.matchAll(/["'`]((?:supportPublic)\.[\w.]+)["'`]/g)) {
      keys.add(match[1])
    }
  }
  return [...keys]
}

function resolveMessage(messages, key) {
  return key.split(".").reduce((value, part) => value?.[part], messages)
}

for (const locale of locales) {
  test(`${locale} resolves every support surface and navigation label`, async () => {
    const messages = await loadMessages(locale)

    for (const surface of surfaces) {
      const keys = await referencedKeys(surface)
      assert.ok(keys.length > 0, `${surface.name} has no discovered message keys`)

      for (const key of keys) {
        const message = resolveMessage(messages, key)
        assert.equal(typeof message, "string", `${locale} is missing ${key}`)
        assert.notEqual(message, key, `${locale} falls back to the raw key ${key}`)
        assert.notEqual(message.trim(), "", `${locale} has an empty translation for ${key}`)
      }
    }
  })

  test(`${locale} resolves every public Support Center message key`, async () => {
    const messages = await loadMessages(locale)

    for (const key of await supportPublicKeys()) {
      const message = resolveMessage(messages, key)
      assert.equal(typeof message, "string", `${locale} is missing ${key}`)
      assert.notEqual(message, key, `${locale} falls back to the raw key ${key}`)
      assert.notEqual(message.trim(), "", `${locale} has an empty translation for ${key}`)
    }
  })
}
