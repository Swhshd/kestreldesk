import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import test from "node:test"

const source = await readFile(new URL("./nav-main.tsx", import.meta.url), "utf8")

test("dashboard nav items expose full labels when visual text is truncated", () => {
  assert.match(source, /<span\s+title=\{title\}>\{title\}<\/span>/)
  assert.match(source, /tooltip=\{item\.title\}/)
  assert.match(source, /<span\s+title=\{item\.title\}>\{item\.title\}<\/span>/)
})

test("collapsed dashboard nav sections expose submenu flyouts on hover", () => {
  assert.match(source, /useSidebar/)
  assert.match(source, /state === "collapsed" && !isMobile/)
  assert.match(source, /<DropdownMenuTrigger\s+openOnHover/)
  assert.match(source, /render=\{<SidebarMenuButton \/>\}/)
  assert.match(source, /<DropdownMenuContent[\s\S]*side="right"/)
  assert.match(source, /<DropdownMenuGroup>[\s\S]*<DropdownMenuLabel/)
})
