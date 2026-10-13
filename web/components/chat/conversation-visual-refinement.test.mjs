import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { createRequire } from "node:module"
import { Script } from "node:vm"
import test from "node:test"

const require = createRequire(import.meta.url)
const React = require("react")
const { renderToStaticMarkup } = require("react-dom/server")
const ts = require("typescript")

function renderBubble(variant) {
  const source = readFileSync(new URL("./conversation-message-bubble.tsx", import.meta.url), "utf8")
  const output = ts.transpileModule(source, { compilerOptions: {
    module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022,
  }}).outputText
  const loadedModule = { exports: {} }
  const bubble = ({ children, variant, ...props }) =>
    React.createElement("div", { ...props, "data-slot": "bubble", "data-variant": variant }, children)
  const bubbleContent = ({ children, ...props }) =>
    React.createElement("div", { ...props, "data-slot": "bubble-content" }, children)
  const imports = (name) => {
    if (name === "react") return React
    if (name === "react/jsx-runtime") return require(name)
    if (name === "@/components/ui/bubble") return { Bubble: bubble, BubbleContent: bubbleContent }
    if (name === "@/lib/utils") return { cn: (...values) => values.filter(Boolean).join(" ") }
    return require(name)
  }
  new Script(`(function(require,moduleRecord,exports){${output}\n})`, { filename: "conversation-message-bubble.tsx" })
    .runInThisContext()(imports, loadedModule, loadedModule.exports)
  return renderToStaticMarkup(React.createElement(loadedModule.exports.ConversationMessageBubble, { variant }, "message"))
}

function renderCustomerAvatar(props = {}) {
  const source = readFileSync(new URL("./customer-avatar.tsx", import.meta.url), "utf8")
  const output = ts.transpileModule(source, { compilerOptions: {
    module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022,
  }}).outputText
  const loadedModule = { exports: {} }
  const avatar = ({ children, ...attributes }) => React.createElement("div", {
    ...attributes,
    "data-slot": "avatar",
  }, children)
  const image = ({ src, ...attributes }) => src
    ? React.createElement("img", { ...attributes, src, "data-slot": "avatar-image" })
    : null
  const fallback = ({ children, ...attributes }) => React.createElement("span", {
    ...attributes,
    "data-slot": "avatar-fallback",
  }, children)
  const imports = (name) => {
    if (name === "react") return React
    if (name === "react/jsx-runtime") return require(name)
    if (name === "@/components/ui/avatar") {
      return { Avatar: avatar, AvatarImage: image, AvatarFallback: fallback }
    }
    if (name === "@/lib/utils") return { cn: (...values) => values.filter(Boolean).join(" ") }
    return require(name)
  }
  new Script(`(function(require,moduleRecord,exports){${output}\n})`, { filename: "customer-avatar.tsx" })
    .runInThisContext()(imports, loadedModule, loadedModule.exports)
  return renderToStaticMarkup(React.createElement(loadedModule.exports.CustomerAvatar, {
    fallback: "K",
    ...props,
  }))
}

function renderSupportAccountAvatar(userType) {
  const source = readFileSync(new URL("../support-chat/support-account-avatar.tsx", import.meta.url), "utf8")
  const output = ts.transpileModule(source, { compilerOptions: {
    module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022,
  }}).outputText
  const loadedModule = { exports: {} }
  const avatar = ({ children, ...attributes }) => React.createElement("div", {
    ...attributes,
    "data-slot": "avatar",
  }, children)
  const image = ({ src, ...attributes }) => React.createElement("img", {
    ...attributes,
    src,
    "data-slot": "avatar-image",
  })
  const fallback = ({ children, ...attributes }) => React.createElement("span", {
    ...attributes,
    "data-slot": "avatar-fallback",
  }, children)
  const customerAvatar = ({ alt, className, fallback: content, src }) => React.createElement("div", {
    className: ["customer-identity-avatar", className].filter(Boolean).join(" "),
    "data-avatar-role": "customer",
  }, React.createElement("img", { src, alt }), content)
  const imports = (name) => {
    if (name === "react") return React
    if (name === "react/jsx-runtime") return require(name)
    if (name === "@/components/ui/avatar") {
      return { Avatar: avatar, AvatarImage: image, AvatarFallback: fallback }
    }
    if (name === "@/components/chat/customer-avatar") return { CustomerAvatar: customerAvatar }
    return require(name)
  }
  new Script(`(function(require,moduleRecord,exports){${output}\n})`, { filename: "support-account-avatar.tsx" })
    .runInThisContext()(imports, loadedModule, loadedModule.exports)
  return renderToStaticMarkup(React.createElement(loadedModule.exports.SupportAccountAvatar, {
    alt: "Account",
    className: "size-8",
    fallback: "A",
    src: "/uploads/account.png",
    userType,
  }))
}

test("conversation bubbles retain their role as a styling hook", () => {
  const html = renderBubble("agent")

  assert.match(html, /data-message-variant="agent"/)
  assert.match(html, /data-variant="default"/)
  assert.match(html, />message<\/div>/)
})

test("conversation workbench keeps three columns with separate visual surfaces", () => {
  const workbench = readFileSync(new URL("../../app/(dashboard)/dashboard/conversations/_components/conversation-workbench.tsx", import.meta.url), "utf8")
  const infoPanel = readFileSync(new URL("../../app/(dashboard)/dashboard/conversations/_components/conversation-info-panel.tsx", import.meta.url), "utf8")
  const groupStart = workbench.indexOf('<ResizablePanelGroup orientation="horizontal">')
  const groupEnd = workbench.indexOf("</ResizablePanelGroup>", groupStart)
  const desktopColumns = workbench.slice(groupStart, groupEnd)

  assert.ok(groupStart >= 0 && groupEnd > groupStart, "desktop resizable group exists")
  assert.equal((desktopColumns.match(/<ResizablePanel(?:\s|>)/g) ?? []).length, 3)
  assert.equal((desktopColumns.match(/<ResizableHandle withHandle \/>/g) ?? []).length, 2)
  assert.ok(workbench.includes('data-visual-surface="conversation-list"'))
  assert.ok(workbench.includes('data-visual-surface="conversation-chat"'))
  assert.ok(infoPanel.includes('data-visual-surface="conversation-details"'))
})

test("plain dark conversation surfaces do not override alternative palette themes", () => {
  const themeCss = readFileSync(new URL("../../styles/kestreldesk-theme.css", import.meta.url), "utf8")
  const plainPaletteSelector = (selector) =>
    new RegExp(
      `:root\\.dark:not\\(\\[data-palette\\]\\) ${selector},\\s*:root\\.dark\\[data-palette="plain"\\] ${selector}`,
    )

  for (const selector of [
    '\\[data-visual-surface="conversation-list"\\]',
    '\\[data-visual-surface="conversation-chat"\\]',
    '\\[data-visual-surface="conversation-details"\\]',
    '\\[data-message-variant="ai"\\] > \\[data-slot="bubble-content"\\]',
    '\\[data-message-variant="agent"\\] > \\[data-slot="bubble-content"\\]',
  ]) {
    assert.match(themeCss, plainPaletteSelector(selector))
  }
})

test("customer send button uses the channel-scoped primary color", () => {
  const supportChat = readFileSync(new URL("../support-chat/chat-shell.tsx", import.meta.url), "utf8")
  const editor = readFileSync(new URL("./shared-message-editor.tsx", import.meta.url), "utf8")
  const sendButton = editor.match(/aria-label=\{t\("conversation\.send"\)\}[\s\S]*?className="([^"]+)"/)

  assert.match(supportChat, /style=\{\{ "--primary": themeColor \} as CSSProperties\}/)
  assert.ok(sendButton, "customer send button is present")
  assert.match(sendButton[1], /\bbg-primary\b/)
  assert.match(sendButton[1], /\bhover:bg-primary\b/)
  assert.doesNotMatch(sendButton[1], /--action/)
})

test("customer identity avatars are isolated from agent, AI, and admin avatars", () => {
  const conversationCustomerSurfaces = [
    "../../app/(dashboard)/dashboard/conversations/_components/conversation-list.tsx",
    "../../app/(dashboard)/dashboard/conversations/_components/conversation-workbench.tsx",
    "../../app/(dashboard)/dashboard/conversations/_components/chat-panel.tsx",
  ].map((path) => readFileSync(new URL(path, import.meta.url), "utf8"))
  const supportAccountSurfaces = [
    "../../app/(support)/support/_components/support-header.tsx",
    "../../app/(support)/support/profile/_components/profile-page.tsx",
  ].map((path) => readFileSync(new URL(path, import.meta.url), "utf8"))
  const dashboardUserAvatars = readFileSync(new URL("../nav-user.tsx", import.meta.url), "utf8")
  const dashboardHeaderAvatars = readFileSync(new URL("../workbench-header.tsx", import.meta.url), "utf8")
  const appSidebarAvatars = readFileSync(new URL("../app-sidebar.tsx", import.meta.url), "utf8")
  const agentMessageAvatars = readFileSync(new URL("../support-chat/message-list.tsx", import.meta.url), "utf8")
  const conversationMessages = readFileSync(new URL("../../app/(dashboard)/dashboard/conversations/_components/chat-panel.tsx", import.meta.url), "utf8")

  for (const source of conversationCustomerSurfaces) {
    assert.match(source, /<CustomerAvatar\b/, "customer and visitor surfaces use the customer-only avatar")
  }
  for (const source of supportAccountSurfaces) {
    assert.match(source, /<SupportAccountAvatar\b/, "public account avatars must resolve the account role")
  }
  assert.doesNotMatch(dashboardUserAvatars, /CustomerAvatar|customer-identity-avatar/)
  assert.doesNotMatch(dashboardHeaderAvatars, /CustomerAvatar|customer-identity-avatar/)
  assert.doesNotMatch(appSidebarAvatars, /CustomerAvatar|customer-identity-avatar/)
  assert.doesNotMatch(agentMessageAvatars, /CustomerAvatar|customer-identity-avatar/)

  const staffAvatarClass = conversationMessages.match(/const avatarClassName =([\s\S]*?)const recalledBubbleClassName/)
  assert.ok(staffAvatarClass, "agent and AI avatar variants are defined")
  assert.doesNotMatch(staffAvatarClass[1], /CustomerAvatar|customer-identity-avatar/)
})

test("customer avatar accent decorates its frame and fallback while preserving the real image", () => {
  const fallbackMarkup = renderCustomerAvatar({ className: "size-8" })
  assert.match(fallbackMarkup, /data-avatar-role="customer"/)
  assert.match(fallbackMarkup, /customer-identity-avatar/)
  assert.match(fallbackMarkup, /customer-identity-avatar-fallback/)

  const imageMarkup = renderCustomerAvatar({
    src: "/uploads/customer-photo.png",
    alt: "Customer profile",
  })
  assert.match(imageMarkup, /src="\/uploads\/customer-photo\.png"/)
  assert.match(imageMarkup, /alt="Customer profile"/)
  assert.match(imageMarkup, /customer-identity-avatar/)
})

test("public support account avatars get gold only for ordinary customer accounts", () => {
  const customerMarkup = renderSupportAccountAvatar("user")
  assert.match(customerMarkup, /data-avatar-role="customer"/)
  assert.match(customerMarkup, /customer-identity-avatar/)

  const employeeMarkup = renderSupportAccountAvatar("employee")
  assert.doesNotMatch(employeeMarkup, /data-avatar-role="customer"|customer-identity-avatar/)
  assert.match(employeeMarkup, /src="\/uploads\/account\.png"/)
})
