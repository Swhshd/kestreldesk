/** @param {string | null | undefined} pathname */
export function isDashboardPublicAuthPath(pathname) {
  return pathname === "/dashboard/login" ||
    pathname?.startsWith("/dashboard/login/") === true ||
    pathname === "/dashboard/forgot-password" ||
    pathname === "/dashboard/reset-password"
}
