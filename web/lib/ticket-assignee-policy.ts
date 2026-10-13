export type TicketAssigneeMode = "self" | "team" | "global"
export type TicketAssignmentMode = "none" | "team" | "global"

export type TicketAssigneeCandidate = {
  userId: number
  displayName?: string
  nickname?: string
  username?: string
}

type TicketAssigneeFieldInput = {
  mode: TicketAssigneeMode
  currentUserId: number
  currentUserName: string
  agents: readonly TicketAssigneeCandidate[]
  noAssigneeLabel?: string
  agentFallbackLabel?: (userId: number) => string
}

export type TicketAssigneeField =
  | { kind: "fixed"; userId: number; label: string }
  | { kind: "select"; options: { value: string; label: string }[] }

export function getTicketAssigneeMode(roles: readonly string[]): TicketAssigneeMode {
  if (roles.includes("super_admin") || roles.includes("admin")) {
    return "global"
  }
  if (roles.includes("cs_team_leader")) {
    return "team"
  }
  return "self"
}

export function getTicketAssignmentMode(roles: readonly string[]): TicketAssignmentMode {
  const mode = getTicketAssigneeMode(roles)
  return mode === "self" ? "none" : mode
}

export function getTicketAssigneeField({
  mode,
  currentUserId,
  currentUserName,
  agents,
  noAssigneeLabel = "No assignee",
  agentFallbackLabel = (userId) => `Agent ${userId}`,
}: TicketAssigneeFieldInput): TicketAssigneeField {
  if (mode === "self") {
    return { kind: "fixed", userId: currentUserId, label: currentUserName }
  }

  const options = agents.map((agent) => ({
    value: String(agent.userId),
    label:
      agent.displayName ||
      agent.nickname ||
      agent.username ||
      agentFallbackLabel(agent.userId),
  }))

  if (mode === "global") {
    options.unshift({ value: "0", label: noAssigneeLabel })
  }

  return { kind: "select", options }
}
