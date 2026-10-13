import type { TagTree } from "@/lib/api/admin"

export type FlatTagNode = TagTree & {
  depth: number
  path: string
  searchableText: string
}

type FlattenTagTreeOptions = {
  excludeIds?: number[]
}

type FlattenVisibleTagTreeOptions = FlattenTagTreeOptions & {
  collapsedIds?: number[]
}

export function flattenTagTree(
  nodes: TagTree[] | null | undefined,
  options: FlattenTagTreeOptions = {}
): FlatTagNode[] {
  const excluded = new Set(options.excludeIds ?? [])
  const result: FlatTagNode[] = []

  function walk(items: TagTree[] | null | undefined, depth: number, parentPath: string) {
    const safeItems = Array.isArray(items) ? items : []

    safeItems.forEach((item) => {
      if (excluded.has(item.id)) {
        return
      }

      const path = parentPath ? `${parentPath} / ${item.name}` : item.name
      result.push({
        ...item,
        depth,
        path,
        searchableText: `${path} ${item.id} ${item.remark ?? ""}`,
      })
      walk(item.children, depth + 1, path)
    })
  }

  walk(nodes, 0, "")
  return result
}

export function buildTagPathMap(
  nodes: TagTree[] | null | undefined
): Map<number, string> {
  const result = new Map<number, string>()
  flattenTagTree(nodes).forEach((item) => {
    result.set(item.id, item.path)
  })
  return result
}

export function flattenVisibleTagTree(
  nodes: TagTree[] | null | undefined,
  options: FlattenVisibleTagTreeOptions = {}
): FlatTagNode[] {
  const excluded = new Set(options.excludeIds ?? [])
  const collapsed = new Set(options.collapsedIds ?? [])
  const result: FlatTagNode[] = []

  function walk(items: TagTree[] | null | undefined, depth: number, parentPath: string) {
    const safeItems = Array.isArray(items) ? items : []

    safeItems.forEach((item) => {
      if (excluded.has(item.id)) {
        return
      }

      const path = parentPath ? `${parentPath} / ${item.name}` : item.name
      result.push({
        ...item,
        depth,
        path,
        searchableText: `${path} ${item.id} ${item.remark ?? ""}`,
      })

      if (!collapsed.has(item.id)) {
        walk(item.children, depth + 1, path)
      }
    })
  }

  walk(nodes, 0, "")
  return result
}

type MutableTagTreeLike<T> = {
  id: number
  status: number
  children: T[]
}

export function updateTagTreeStatus<T extends MutableTagTreeLike<T>>(
  nodes: T[] | null | undefined,
  id: number,
  status: number
): T[] {
  const safeNodes = Array.isArray(nodes) ? nodes : []

  function walk(items: T[]): { nodes: T[]; changed: boolean } {
    let changed = false
    const nextNodes = items.map((item) => {
      const nextChildren = walk(item.children)
      const statusChanged = item.id === id && item.status !== status

      if (!statusChanged && !nextChildren.changed) {
        return item
      }

      changed = true
      return {
        ...item,
        status: statusChanged ? status : item.status,
        children: nextChildren.nodes,
      }
    })

    return { nodes: changed ? nextNodes : items, changed }
  }

  return walk(safeNodes).nodes
}
