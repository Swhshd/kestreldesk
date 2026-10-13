// This file has been modified for the KestrelDesk project.

import assert from "node:assert/strict"
import test from "node:test"
import ts from "typescript"
import { readFile } from "node:fs/promises"
import vm from "node:vm"

function compileTypeScript(source, fileName, require) {
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      target: ts.ScriptTarget.ES2017,
      module: ts.ModuleKind.CommonJS,
    },
    fileName,
  })
  const moduleRecord = { exports: {} }
  const sandbox = {
    exports: moduleRecord.exports,
    module: moduleRecord,
    require,
  }
  vm.runInNewContext(compiled.outputText, sandbox)
  return moduleRecord.exports
}

async function loadModule() {
  const configSource = await readFile(new URL("../i18n/config.ts", import.meta.url), "utf8")
  const config = compileTypeScript(configSource, "config.ts", () => {
    throw new Error("unexpected i18n config dependency")
  })
  const messageSource = await readFile(new URL("../i18n/messages.ts", import.meta.url), "utf8")
  const locales = Object.fromEntries(await Promise.all(["en-US", "zh-CN"].map(async (locale) => [
    locale,
    JSON.parse(await readFile(new URL(`../messages/${locale}.json`, import.meta.url), "utf8")),
  ])))
  const messages = compileTypeScript(messageSource, "messages.ts", (id) => {
    if (id === "@/i18n/config") {
      return config
    }
    if (id === "@/messages/en-US.json") {
      return { __esModule: true, default: locales["en-US"] }
    }
    if (id === "@/messages/zh-CN.json") {
      return { __esModule: true, default: locales["zh-CN"] }
    }
    throw new Error(`unexpected i18n messages dependency ${id}`)
  })
  const source = await readFile(new URL("./notification-i18n.ts", import.meta.url), "utf8")
  return compileTypeScript(source, "notification-i18n.ts", (id) => {
    if (id === "@/i18n/config") {
      return config
    }
    if (id === "@/i18n/messages") {
      return messages
    }
    throw new Error(`unexpected notification dependency ${id}`)
  })
}

test("localizes realtime ticket assignment notification to English", async () => {
  const { localizeNotificationItem } = await loadModule()

  const result = localizeNotificationItem(
    {
      id: 1,
      recipientUserId: 2,
      title: "\u5de5\u5355\u6307\u6d3e\u63d0\u9192",
      content: "\u5de5\u5355 TK-100 \u5df2\u6307\u6d3e\u7ed9\u4f60\nCannot sign in\n\u6307\u6d3e\u539f\u56e0: urgent",
      notificationType: "ticket_assigned",
      bizType: "ticket",
      bizId: 100,
      actionUrl: "/dashboard/tickets?ticketId=100",
    },
    "en-US"
  )

  assert.equal(result.title, "Ticket assigned")
  assert.equal(
    result.content,
    "Ticket TK-100 has been assigned to you.\nCannot sign in\nAssignment reason: urgent"
  )
})
