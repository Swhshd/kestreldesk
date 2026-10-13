import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import test from "node:test"

const webRoot = new URL("../", import.meta.url)
const source = async (file) => {
  try {
    return await readFile(new URL(file, webRoot), "utf8")
  } catch (error) {
    if (error?.code === "ENOENT") return ""
    throw error
  }
}
const messages = async (locale) =>
  JSON.parse(await readFile(new URL(`../messages/${locale}.json`, import.meta.url), "utf8"))
const expectSource = (value, pattern, message) => assert.ok(pattern.test(value), message)

test("Dashboard and Support login pages link to their own forgot-password route", async () => {
  const [dashboardLogin, supportLogin] = await Promise.all([
    source("components/login-form.tsx"),
    source("app/(support)/support/login/_components/login-page.tsx"),
  ])
  expectSource(dashboardLogin, /href="\/dashboard\/forgot-password"/, "Dashboard login links to Dashboard recovery")
  expectSource(supportLogin, /href="\/support\/forgot-password"/, "Support login links to Support recovery")
})

test("both forgot pages show one account-neutral confirmation after a successful request", async () => {
  const [dashboardForgotPage, supportForgotPage, dashboardForgotForm, supportForgotForm] = await Promise.all([
    source("app/(dashboard)/dashboard/forgot-password/page.tsx"),
    source("app/(support)/support/forgot-password/page.tsx"),
    source("components/login-form.tsx"),
    source("app/(support)/support/login/_components/login-page.tsx"),
  ])
  expectSource(dashboardForgotPage, /DashboardForgotPasswordForm/, "Dashboard forgot route renders its surface form")
  expectSource(supportForgotPage, /SupportForgotPasswordForm/, "Support forgot route renders its surface form")
  for (const [surface, form] of [["Dashboard", dashboardForgotForm], ["Support", supportForgotForm]]) {
    expectSource(form, /requestPasswordReset/, `${surface} forgot page calls its API wrapper`)
    expectSource(form, /setSubmitted\(true\)/, `${surface} forgot page reaches its confirmation state`)
    expectSource(form, /auth\.passwordRecovery\.requestConfirmation/, `${surface} uses the shared generic copy`)
    assert.ok(!/data\.message|accountExists|userExists|isEligible/.test(form), `${surface} confirmation does not branch on account state`)
  }
})

test("generic recovery confirmation stays equivalent and does not reveal lookup state in both locales", async () => {
  const [zh, en] = await Promise.all([messages("zh-CN"), messages("en-US")])
  assert.equal(zh.auth.passwordRecovery.requestConfirmation, "如果该账号存在，我们会向绑定邮箱发送密码重置邮件。")
  assert.equal(en.auth.passwordRecovery.requestConfirmation, "If the account exists, we will send a password reset email to its associated address.")
  assert.doesNotMatch(zh.auth.passwordRecovery.requestConfirmation, /不存在|没有该账号|已发送/)
  assert.doesNotMatch(en.auth.passwordRecovery.requestConfirmation, /does not exist|not found|has been sent/i)
})

test("both reset pages validate the token, required fields, and matching confirmation", async () => {
  const [dashboard, support] = await Promise.all([
    source("components/login-form.tsx"),
    source("app/(support)/support/login/_components/login-page.tsx"),
  ])
  for (const [surface, component] of [["Dashboard", dashboard], ["Support", support]]) {
    expectSource(component, /!token\.trim\(\)/, `${surface} reset requires a URL token`)
    expectSource(component, /!newPassword\.trim\(\)/, `${surface} reset requires a new password`)
    expectSource(component, /!confirmPassword\.trim\(\)/, `${surface} reset requires confirmation`)
    expectSource(component, /newPassword\s*!==\s*confirmPassword/, `${surface} reset rejects mismatched passwords`)
    expectSource(component, /newPassword\.length\s*<\s*8/, `${surface} reset follows the server password minimum`)
  }
})

test("reset success returns to the matching login and invalid-token errors stay safe", async () => {
  const [dashboard, support] = await Promise.all([
    source("components/login-form.tsx"),
    source("app/(support)/support/login/_components/login-page.tsx"),
  ])
  expectSource(dashboard, /router\.replace\("\/dashboard\/login"\)/, "Dashboard reset returns only to Dashboard login")
  expectSource(support, /router\.replace\("\/support\/login"\)/, "Support reset returns only to Support login")
  for (const [surface, component] of [["Dashboard", dashboard], ["Support", support]]) {
    expectSource(component, /error instanceof Error \? error\.message/, `${surface} shows the safe localized API token error`)
    expectSource(component, /auth\.passwordRecovery\.resetFailed/, `${surface} has a safe fallback error`)
  }
})

test("reset pages set no-referrer metadata", async () => {
  const [dashboard, support] = await Promise.all([
    source("app/(dashboard)/dashboard/reset-password/page.tsx"),
    source("app/(support)/support/reset-password/page.tsx"),
  ])
  expectSource(dashboard, /referrer:\s*"no-referrer"/, "Dashboard reset page suppresses referrers")
  expectSource(support, /referrer:\s*"no-referrer"/, "Support reset page suppresses referrers")
})

test("Dashboard recovery routes are public through both authentication guards", async () => {
  const [policy, layout, provider] = await Promise.all([
    source("lib/auth-route-policy.mjs"),
    source("app/(dashboard)/dashboard/layout.tsx"),
    source("components/auth-provider.tsx"),
  ])
  expectSource(policy, /export function isDashboardPublicAuthPath/, "Dashboard public-auth policy exists")
  expectSource(layout, /isDashboardPublicAuthPath\(pathname\)/, "Dashboard layout uses public-auth policy")
  expectSource(provider, /isDashboardPublicAuthPath\(pathname\)/, "Auth provider uses public-auth policy")
  const { isDashboardPublicAuthPath } = await import("../lib/auth-route-policy.mjs")
  assert.equal(isDashboardPublicAuthPath("/dashboard/login"), true)
  assert.equal(isDashboardPublicAuthPath("/dashboard/forgot-password"), true)
  assert.equal(isDashboardPublicAuthPath("/dashboard/reset-password"), true)
  assert.equal(isDashboardPublicAuthPath("/dashboard/users"), false)
})
