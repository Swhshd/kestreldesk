import assert from "node:assert/strict"
import test from "node:test"
import { readFile } from "node:fs/promises"

const read = (path) => readFile(new URL(path, import.meta.url), "utf8")

const expectedLabels = {
  "zh-CN": {
    title: "支持配置",
    description: "配置支持中心的默认在线咨询渠道和导航菜单。",
    refresh: "刷新",
    save: "保存",
    navigationTitle: "导航菜单",
    navigationDescription: "配置支持中心显示的导航项。",
    addNavigation: "添加导航",
    sort: "排序",
    menuTitle: "菜单标题",
    menuURL: "菜单地址",
    target: "打开方式",
  },
  "en-US": {
    title: "Support Settings",
    description: "Configure the default live support channel and the Support Center navigation menu.",
    refresh: "Refresh",
    save: "Save",
    navigationTitle: "Navigation menu",
    navigationDescription: "Configure the items displayed in the Support Center.",
    addNavigation: "Add navigation",
    sort: "Order",
    menuTitle: "Menu title",
    menuURL: "Menu URL",
    target: "Open in",
  },
}

test("support configuration labels used by the page are translated in both locales", async () => {
  const [panel, zh, en] = await Promise.all([
    read("../app/(dashboard)/dashboard/support/_components/support-config-panel.tsx"),
    read("../messages/zh-CN.json").then(JSON.parse),
    read("../messages/en-US.json").then(JSON.parse),
  ])
  const usedKeys = [...panel.matchAll(/t\("supportConfig\.([\w]+)"/g)].map((match) => match[1])

  assert.ok(usedKeys.length > 0, "the support configuration page should use localized copy")
  for (const [locale, messages] of [["zh-CN", zh], ["en-US", en]]) {
    for (const key of usedKeys) {
      const value = messages.supportConfig?.[key]
      assert.equal(typeof value, "string", `${locale} is missing supportConfig.${key}`)
      assert.notEqual(value, `supportConfig.${key}`, `${locale} exposes supportConfig.${key}`)
      assert.doesNotMatch(value, /supportConfig\./, `${locale} exposes a raw supportConfig key`)
    }
  }

  for (const [key, value] of Object.entries(expectedLabels["zh-CN"])) {
    assert.equal(zh.supportConfig[key], value, `zh-CN supportConfig.${key}`)
  }
  for (const [key, value] of Object.entries(expectedLabels["en-US"])) {
    assert.equal(en.supportConfig[key], value, `en-US supportConfig.${key}`)
  }
})

test("the no-default sentinel is presented through localized text, never as _none", async () => {
  const [panel, zh, en] = await Promise.all([
    read("../app/(dashboard)/dashboard/support/_components/support-config-panel.tsx"),
    read("../messages/zh-CN.json").then(JSON.parse),
    read("../messages/en-US.json").then(JSON.parse),
  ])

  assert.match(panel, /<SelectItem value="_none">\s*\{t\("supportConfig\.noDefaultWebChannel"\)\}\s*<\/SelectItem>/)
  assert.match(panel, /<SelectValue>\s*\{defaultWebChannelId[\s\S]*?t\("supportConfig\.noDefaultWebChannel"\)[\s\S]*?<\/SelectValue>/)
  assert.ok(zh.supportConfig.noDefaultWebChannel)
  assert.ok(en.supportConfig.noDefaultWebChannel)
  assert.notEqual(zh.supportConfig.noDefaultWebChannel, "_none")
  assert.notEqual(en.supportConfig.noDefaultWebChannel, "_none")
})
