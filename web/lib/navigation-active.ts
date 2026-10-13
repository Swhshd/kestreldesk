export type DashboardNavActiveItem = {
  url: string
}

const DASHBOARD_NAV_SECTION_STORAGE_PREFIX = "dashboard.sidebar.navSection"

function normalizePath(path: string) {
  return path !== "/" ? path.replace(/\/+$/, "") : path
}

export function isDashboardNavItemActive(pathname: string, itemUrl: string) {
  const currentPath = normalizePath(pathname)
  const targetPath = normalizePath(itemUrl)

  if (targetPath === "/" || targetPath === "/dashboard") {
    return currentPath === targetPath
  }
  return currentPath === targetPath || currentPath.startsWith(targetPath + "/")
}

export function dashboardNavSectionHasActiveItem(
  items: ReadonlyArray<DashboardNavActiveItem>,
  pathname: string
) {
  return items.some((item) => isDashboardNavItemActive(pathname, item.url))
}

export function getDashboardNavSectionStorageKey(sectionKey: string) {
  return `${DASHBOARD_NAV_SECTION_STORAGE_PREFIX}.${sectionKey}.open`
}

export function parseDashboardNavSectionOpenState(value: string | null) {
  if (value === "true") {
    return true
  }
  if (value === "false") {
    return false
  }
  return undefined
}
