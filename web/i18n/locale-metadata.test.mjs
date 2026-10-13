import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import test from "node:test"
import ts from "typescript"
import vm from "node:vm"

async function loadLocaleMessages() {
  return {
    "en-US": JSON.parse(
      await readFile(new URL("../messages/en-US.json", import.meta.url), "utf8")
    ),
    "zh-CN": JSON.parse(
      await readFile(new URL("../messages/zh-CN.json", import.meta.url), "utf8")
    ),
  }
}

async function loadTranslationRuntime(messages) {
  const source = await readFile(new URL("./messages.ts", import.meta.url), "utf8")
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      target: ts.ScriptTarget.ES2020,
      module: ts.ModuleKind.CommonJS,
    },
    fileName: "messages.ts",
  })
  const exports = {}
  const sandbox = {
    exports,
    require(moduleName) {
      if (moduleName === "@/i18n/config") {
        return { DEFAULT_LOCALE: "en-US", readStoredLocale: () => "en-US" }
      }
      if (moduleName === "@/messages/en-US.json") return messages["en-US"]
      if (moduleName === "@/messages/zh-CN.json") return messages["zh-CN"]
      throw new Error(`Unexpected module: ${moduleName}`)
    },
  }
  vm.runInNewContext(compiled.outputText, sandbox)
  return exports
}

test("locale modification metadata parses but stays outside the translation catalog", async () => {
  const messages = await loadLocaleMessages()
  const expectedNotice = "This file has been modified for the KestrelDesk project."

  for (const locale of Object.values(messages)) {
    assert.equal(locale.__kestreldesk?.modifiedNotice, expectedNotice)
  }

  const runtime = await loadTranslationRuntime(messages)
  for (const locale of ["en-US", "zh-CN"]) {
    assert.equal(
      runtime.translateMessage(locale, "__kestreldesk.modifiedNotice"),
      "__kestreldesk.modifiedNotice"
    )
    assert.equal(runtime.translateMessage(locale, "app.brand"), "KestrelDesk")
  }
})
