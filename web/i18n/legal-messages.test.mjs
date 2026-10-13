// This file has been modified for the KestrelDesk project.

import assert from "node:assert/strict"
import test from "node:test"
import { readFile } from "node:fs/promises"

async function loadMessages(locale) {
  const source = await readFile(new URL(`../messages/${locale}.json`, import.meta.url), "utf8")
  return JSON.parse(source)
}

async function loadLoginForm() {
  return readFile(new URL("../components/login-form.tsx", import.meta.url), "utf8")
}

test("legal pages have localized document content", async () => {
  for (const locale of ["zh-CN", "en-US"]) {
    const messages = await loadMessages(locale)

    assert.equal(typeof messages.legal.terms.title, "string")
    assert.equal(typeof messages.legal.privacy.title, "string")
    assert.ok(messages.legal.terms.sections.length >= 6)
    assert.ok(messages.legal.privacy.sections.length >= 6)

    for (const page of [messages.legal.terms, messages.legal.privacy]) {
      assert.equal(typeof page.updatedAt, "string")
      assert.equal(typeof page.relatedLabel, "string")
      assert.equal(typeof page.relatedLink, "string")
      for (const section of page.sections) {
        assert.equal(typeof section.title, "string")
        assert.equal(typeof section.body, "string")
      }
    }
  }
})

test("login footer links to the canonical KestrelDesk legal pages in both locales", async () => {
  const source = await loadLoginForm()

  assert.match(source, /<Link href="\/legal\/terms"/)
  assert.match(source, /<Link href="\/legal\/privacy"/)
  assert.match(source, /t\("auth\.termsSuffix"\)/)

  for (const locale of ["zh-CN", "en-US"]) {
    const messages = await loadMessages(locale)
    assert.ok(messages.auth.termsOfService.length > 0)
    assert.ok(messages.auth.privacyPolicy.length > 0)
    assert.equal(messages.auth.termsSuffix, locale === "zh-CN" ? "。" : ".")

    if (locale === "zh-CN") {
      assert.equal(
        messages.auth.termsPrefix +
          messages.auth.termsOfService +
          messages.auth.termsJoiner +
          messages.auth.privacyPolicy +
          messages.auth.termsSuffix,
        "继续即表示同意《服务条款》并已阅读《隐私政策》。",
      )
    } else {
      assert.match(messages.auth.termsPrefix, /KestrelDesk/i)
    }
  }
})

test("KestrelDesk policies replace the generic draft with dated pre-release content", async () => {
  for (const locale of ["zh-CN", "en-US"]) {
    const messages = await loadMessages(locale)
    const terms = messages.legal.terms
    const privacy = messages.legal.privacy
    const combined = [terms, privacy]
      .flatMap((page) => [page.title, page.description, page.updatedAt, ...page.sections.flatMap((section) => [section.title, section.body])])
      .join("\n")

    assert.equal(terms.updatedAt.includes("2026-10-12"), true)
    assert.equal(privacy.updatedAt.includes("2026-10-12"), true)
    assert.match(combined, /KestrelDesk/i)
    assert.match(combined, /Pre-release|预发布/i)
    assert.doesNotMatch(combined, /2026-05-30/)
    assert.doesNotMatch(combined, /may be replaced by formal legal (terms|text)|formal legal (terms|text) may be replaced/i)
    assert.doesNotMatch(combined, /正式法律文本可由服务提供方另行替换/)
    assert.doesNotMatch(combined, /GDPR compliant|CCPA compliant|SOC 2|ISO certified|HIPAA compliant/i)
    assert.ok(terms.sections.length >= 16)
    assert.ok(privacy.sections.length >= 20)
    assert.match(combined, /Apache License 2\.0/)
    assert.match(combined, /self-hosted|自托管/i)
    assert.match(combined, /SMTP/i)
    assert.ok(combined.includes("localStorage"))
    assert.ok(combined.includes("sessionStorage"))
  }
})
