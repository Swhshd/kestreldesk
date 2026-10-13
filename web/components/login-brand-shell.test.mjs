import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { Script } from "node:vm"
import test from "node:test"

const require = createRequire(import.meta.url)
const React = require("react")
const { renderToStaticMarkup } = require("react-dom/server")
const ts = require("typescript")

function renderLogin(locale) {
  const messages = JSON.parse(readFileSync(new URL(`../messages/${locale}.json`, import.meta.url), "utf8"))
  const t = (key, values = {}) => {
    let text = key.split(".").reduce((value, part) => value?.[part], messages) ?? key
    for (const [key, value] of Object.entries(values)) text = text.replaceAll(`{${key}}`, value)
    return text
  }
  let stateIndex = 0
  function element(tag) {
    function MockElement({ children, ...props }) {
      return React.createElement(tag, props, children)
    }
    MockElement.displayName = `MockElement(${String(tag)})`
    return MockElement
  }
  const ui = {
    Button: element("button"), Card: element("section"), CardContent: element("div"),
    Field: element("div"), FieldDescription: element("p"), FieldGroup: element("div"),
    FieldLabel: element("label"), FieldSeparator: element("div"), Input: element("input"),
  }
  function load(file) {
    const source = readFileSync(new URL(file, import.meta.url), "utf8")
    const output = ts.transpileModule(source, { compilerOptions: {
      module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022,
    }}).outputText
    const loadedModule = { exports: {} }
    const imports = (name) => {
      if (name === "react") return { ...React, useEffect: () => {}, useState: (initial) => {
        stateIndex += 1
        return [stateIndex === 3 ? { passwordLoginEnabled: true, companyName: "KestrelDesk" } : initial, () => {}]
      }}
      if (name === "next/navigation") return { useRouter: () => ({}), useSearchParams: () => new URLSearchParams() }
      if (name === "next/image") return { __esModule: true, default: element("img") }
      if (name === "next/link") return { __esModule: true, default: element("a") }
      if (name === "@/components/auth-provider") return { useAuth: () => ({ ready: true, session: null }) }
      if (name === "@/i18n/provider") return { useI18n: () => t }
      if (name === "@/lib/identity-boundary.mjs") return { getDashboardLoginDestination: () => "/dashboard" }
      if (name === "@/lib/utils") return { cn: (...values) => values.filter(Boolean).join(" ") }
      if (name.startsWith("@/components/ui/")) return ui
      if (name === "@/components/login-brand-panel") return load("./login-brand-panel.tsx")
      if (name.startsWith("@/lib/api/")) return {}
      return require(name)
    }
    new Script(`(function(require,moduleRecord,exports){${output}\n})`, { filename: file })
      .runInThisContext()(imports, loadedModule, loadedModule.exports)
    return loadedModule.exports
  }
  return renderToStaticMarkup(React.createElement(load("./login-form.tsx").LoginForm))
}

for (const locale of ["zh-CN", "en-US"]) {
  test(`login shows the KestrelDesk brand panel before the unchanged password form (${locale})`, () => {
    const html = renderLogin(locale)
    assert.match(html, /<h2[^>]*>KestrelDesk<\/h2>/)
    assert.match(html, /src="\/images\/logo.svg"/)
    assert.doesNotMatch(html, /login-illustration.svg/)
    assert.ok(html.indexOf("<h2") < html.indexOf("<form"), "brand region precedes form")
    assert.match(html, /<input(?=[^>]*name="username")(?=[^>]*required)[^>]*>/)
    assert.match(html, /<input(?=[^>]*name="password")(?=[^>]*type="password")(?=[^>]*required)[^>]*>/)
    assert.match(html, /href="\/dashboard\/forgot-password"/)
    assert.match(html, /<button[^>]*type="submit"/)
    assert.match(html, /href="\/legal\/terms"/)
    assert.match(html, /href="\/legal\/privacy"/)
    assert.doesNotMatch(html, /app\.metadataTitle|app\.metadataDescription/)

    if (locale === "zh-CN") {
      const visibleText = html.replace(/<[^>]*>/g, "")
      assert.ok(visibleText.includes("继续即表示同意《服务条款》并已阅读《隐私政策》。"))
    }
  })
}
