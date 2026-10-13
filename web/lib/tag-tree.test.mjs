import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import test from "node:test"
import ts from "typescript"
import vm from "node:vm"

async function loadModule() {
  const source = await readFile(new URL("./tag-tree.ts", import.meta.url), "utf8")
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      target: ts.ScriptTarget.ES2017,
      module: ts.ModuleKind.CommonJS,
    },
    fileName: "tag-tree.ts",
  })
  const sandbox = {
    exports: {},
    module: { exports: {} },
  }
  sandbox.exports = sandbox.module.exports
  vm.runInNewContext(compiled.outputText, sandbox)
  return sandbox.module.exports
}

const tags = [
  {
    id: 1,
    parentId: 0,
    name: "产品",
    remark: "",
    sortNo: 1,
    status: 0,
    createdAt: "",
    updatedAt: "",
    children: [
      {
        id: 2,
        parentId: 1,
        name: "退款",
        remark: "售后",
        sortNo: 1,
        status: 0,
        createdAt: "",
        updatedAt: "",
        children: [],
      },
    ],
  },
  {
    id: 3,
    parentId: 0,
    name: "技术",
    remark: "",
    sortNo: 2,
    status: 0,
    createdAt: "",
    updatedAt: "",
    children: [],
  },
]

test("flattens tag tree with depth and full path", async () => {
  const { flattenTagTree } = await loadModule()

  assert.equal(
    JSON.stringify(flattenTagTree(tags).map((item) => ({
      id: item.id,
      depth: item.depth,
      path: item.path,
      searchableText: item.searchableText,
    }))),
    JSON.stringify([
      { id: 1, depth: 0, path: "产品", searchableText: "产品 1 " },
      { id: 2, depth: 1, path: "产品 / 退款", searchableText: "产品 / 退款 2 售后" },
      { id: 3, depth: 0, path: "技术", searchableText: "技术 3 " },
    ])
  )
})

test("excludes a tag and its descendants for parent selection", async () => {
  const { flattenTagTree } = await loadModule()

  assert.equal(
    JSON.stringify(flattenTagTree(tags, { excludeIds: [1] }).map((item) => item.id)),
    JSON.stringify([3])
  )
})

test("builds full-path map for selected tag badges", async () => {
  const { buildTagPathMap } = await loadModule()

  assert.equal(buildTagPathMap(tags).get(2), "产品 / 退款")
})

test("flattens only visible branches when a parent is collapsed", async () => {
  const { flattenVisibleTagTree } = await loadModule()

  assert.equal(
    JSON.stringify(
      flattenVisibleTagTree(tags, { collapsedIds: [1] }).map((item) => item.id)
    ),
    JSON.stringify([1, 3])
  )
})

test("updates a tag status in tree data without mutating the original tree", async () => {
  const { updateTagTreeStatus } = await loadModule()

  const nextTags = updateTagTreeStatus(tags, 2, 1)

  assert.equal(tags[0].children[0].status, 0)
  assert.equal(nextTags[0].children[0].status, 1)
  assert.equal(nextTags[0].status, 0)
  assert.equal(nextTags[1], tags[1])
})
