import assert from "node:assert/strict"
import { describe, it } from "node:test"
import ts from "typescript"
import { readFile } from "node:fs/promises"
import vm from "node:vm"

function plain(value) {
  return JSON.parse(JSON.stringify(value))
}

async function loadModule() {
  const source = await readFile(
    new URL("./dashboard-crud-utils.ts", import.meta.url),
    "utf8"
  )
  const compiled = ts.transpileModule(source, {
    compilerOptions: {
      target: ts.ScriptTarget.ES2017,
      module: ts.ModuleKind.CommonJS,
    },
    fileName: "dashboard-crud-utils.ts",
  })
  const sandbox = {
    exports: {},
    module: { exports: {} },
  }
  sandbox.exports = sandbox.module.exports
  vm.runInNewContext(compiled.outputText, sandbox)
  return sandbox.module.exports
}

describe("buildDashboardCrudQuery", () => {
  it("trims text filters and omits empty values", async () => {
    const { buildDashboardCrudQuery } = await loadModule()
    const query = buildDashboardCrudQuery({
      values: {
        title: "  hello  ",
        groupName: "   ",
      },
      filters: [
        { name: "title", trim: true },
        { name: "groupName", trim: true },
      ],
      page: 2,
      limit: 50,
    })

    assert.deepEqual(plain(query), {
      title: "hello",
      page: 2,
      limit: 50,
    })
  })

  it("omits configured all values and parses numbers", async () => {
    const { buildDashboardCrudQuery } = await loadModule()
    const query = buildDashboardCrudQuery({
      values: {
        status: "all",
        companyId: "42",
      },
      filters: [
        { name: "status", allValue: "all" },
        { name: "companyId", allValue: "0", valueType: "number" },
      ],
      page: 1,
      limit: 20,
    })

    assert.deepEqual(plain(query), {
      companyId: 42,
      page: 1,
      limit: 20,
    })
  })
})

describe("buildDashboardCrudInitialFilters", () => {
  it("creates stable filter state from defaults", async () => {
    const { buildDashboardCrudInitialFilters } = await loadModule()

    assert.deepEqual(
      plain(
        buildDashboardCrudInitialFilters([
          { name: "keyword", defaultValue: "" },
          { name: "status", defaultValue: "all" },
          { name: "companyId", defaultValue: 0 },
        ])
      ),
      {
        keyword: "",
        status: "all",
        companyId: 0,
      }
    )
  })
})

describe("normalizeDashboardCrudPageResult", () => {
  it("returns a stable empty page when the API result is missing", async () => {
    const { normalizeDashboardCrudPageResult } = await loadModule()
    assert.deepEqual(plain(normalizeDashboardCrudPageResult(null, 3, 10)), {
      results: [],
      page: {
        page: 3,
        limit: 10,
        total: 0,
      },
    })
  })
})

describe("buildDashboardCrudFormValues", () => {
  it("uses defaults for create forms and item values for edit forms", async () => {
    const { buildDashboardCrudFormValues } = await loadModule()
    const fields = [
      { name: "title", defaultValue: "Untitled" },
      { name: "sortNo", type: "number", defaultValue: "0" },
      {
        name: "status",
        defaultValue: "0",
        valueFromItem: (item) => String(item.status),
      },
    ]

    assert.deepEqual(plain(buildDashboardCrudFormValues(fields)), {
      title: "Untitled",
      sortNo: "0",
      status: "0",
    })
    assert.deepEqual(
      plain(buildDashboardCrudFormValues(fields, { title: "Hello", sortNo: 7, status: 1 })),
      {
        title: "Hello",
        sortNo: "7",
        status: "1",
      }
    )
  })

  it("normalizes boolean and multi-select defaults", async () => {
    const { buildDashboardCrudFormValues } = await loadModule()
    const fields = [
      { name: "enabled", type: "switch", defaultValue: true },
      { name: "required", type: "checkbox" },
      { name: "toolIds", type: "multiSelect", defaultValue: [1, "2"] },
    ]

    assert.deepEqual(plain(buildDashboardCrudFormValues(fields)), {
      enabled: true,
      required: false,
      toolIds: ["1", "2"],
    })
    assert.deepEqual(
      plain(
        buildDashboardCrudFormValues(fields, {
          enabled: 0,
          required: "1",
          toolIds: [3, "4"],
        })
      ),
      {
        enabled: false,
        required: true,
        toolIds: ["3", "4"],
      }
    )
  })
})

describe("normalizeDashboardCrudSubmitValues", () => {
  it("trims strings and converts number fields", async () => {
    const { normalizeDashboardCrudSubmitValues } = await loadModule()
    const fields = [
      { name: "title", trim: true },
      { name: "sortNo", type: "number" },
      { name: "status", type: "select", valueType: "number" },
    ]

    assert.deepEqual(
      plain(
        normalizeDashboardCrudSubmitValues(fields, {
          title: "  Hello  ",
          sortNo: "12",
          status: "1",
        })
      ),
      {
        title: "Hello",
        sortNo: 12,
        status: 1,
      }
    )
  })

  it("converts boolean and multi-select fields", async () => {
    const { normalizeDashboardCrudSubmitValues } = await loadModule()
    const fields = [
      { name: "enabled", type: "switch" },
      { name: "toolCodes", type: "multiSelect" },
      { name: "roleIds", type: "multiSelect", valueType: "number" },
      { name: "section", type: "section" },
    ]

    assert.deepEqual(
      plain(
        normalizeDashboardCrudSubmitValues(fields, {
          enabled: true,
          toolCodes: ["search", "faq"],
          roleIds: ["1", "2", "bad"],
          section: "",
        })
      ),
      {
        enabled: true,
        toolCodes: ["search", "faq"],
        roleIds: [1, 2],
      }
    )
  })
})

describe("dashboard CRUD action rules", () => {
  it("defaults actions to visible and enabled", async () => {
    const {
      isDashboardCrudActionVisible,
      isDashboardCrudActionDisabled,
    } = await loadModule()
    const action = {}

    assert.equal(isDashboardCrudActionVisible(action, { status: 0 }), true)
    assert.equal(isDashboardCrudActionDisabled(action, { status: 0 }), false)
  })

  it("honors visible and disabled predicates", async () => {
    const {
      isDashboardCrudActionVisible,
      isDashboardCrudActionDisabled,
    } = await loadModule()
    const action = {
      visible: (item) => item.status !== 2,
      disabled: (item) => item.status === 1,
    }

    assert.equal(isDashboardCrudActionVisible(action, { status: 2 }), false)
    assert.equal(isDashboardCrudActionVisible(action, { status: 0 }), true)
    assert.equal(isDashboardCrudActionDisabled(action, { status: 1 }), true)
    assert.equal(isDashboardCrudActionDisabled(action, { status: 0 }), false)
  })
})
