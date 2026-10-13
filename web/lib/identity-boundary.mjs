/** @param {string | undefined} userType */
export function isSupportCustomer(userType) {
  return userType === "user"
}

/** @param {string | undefined} userType */
export function isDashboardEmployee(userType) {
  return userType === "employee"
}

/** @param {string | undefined} userType */
export function getSupportAccountIdentity(userType) {
  const isCustomer = isSupportCustomer(userType)
  const isEmployee = isDashboardEmployee(userType)
  return {
    isCustomer,
    isEmployee,
    showProfileLinks: isCustomer,
    showDashboardLink: isEmployee,
    kind: isCustomer ? "customer" : isEmployee ? "staff" : "unknown",
  }
}

/** @param {string | undefined} userType @param {string} nextDestination */
export function getSupportAccountLoginDestination(userType, nextDestination) {
  if (isSupportCustomer(userType)) return nextDestination
  if (isDashboardEmployee(userType)) return "/dashboard"
  return "/support"
}

/** @param {string | undefined} userType @param {string} requestedPath */
export function getDashboardLoginDestination(userType, requestedPath) {
  return isDashboardEmployee(userType) ? requestedPath : "/support"
}

/** @param {string | undefined} userType */
export function canRenderDashboardShell(userType) {
  return isDashboardEmployee(userType)
}

/** @param {string | undefined} userType */
export function getNonEmployeeWorkspaceRedirect(userType) {
  return canRenderDashboardShell(userType) ? null : "/support"
}
