import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import test from "node:test"

const policy = await import("./ticket-assignee-policy.ts").catch(() => null)
const detailSource = await readFile(
  new URL("../app/(dashboard)/dashboard/tickets/_components/ticket-detail-dialog.tsx", import.meta.url),
  "utf8",
)
const assignSource = await readFile(
  new URL("../app/(dashboard)/dashboard/tickets/_components/ticket-assign-dialog.tsx", import.meta.url),
  "utf8",
)
const ticketListSource = await readFile(
  new URL("../app/(dashboard)/dashboard/tickets/page.tsx", import.meta.url),
  "utf8",
)

test("ordinary agents do not get a ticket assignment action", () => {
  assert.ok(policy, "ticket assignment policy is missing")
  assert.equal(policy.getTicketAssignmentMode(["cs_user"]), "none")
  assert.match(detailSource, /assignmentMode !== "none"/)
  assert.match(detailSource, /t\("ticket\.assign"\)/)
  assert.match(ticketListSource, /assigneeMode === "team"[\s\S]*?assigneeMode === "global"[\s\S]*?Promise\.resolve\(\[\]\)/)
})

test("team leaders get assignment with server-scoped team candidates", () => {
  assert.ok(policy, "ticket assignment policy is missing")
  assert.equal(policy.getTicketAssignmentMode(["cs_team_leader"]), "team")
  assert.match(assignSource, /mode === "team"\s*\?\s*fetchTicketCreateAssigneeOptions\(\)/)
  assert.match(ticketListSource, /assigneeMode === "team"\s*\?\s*fetchTicketCreateAssigneeOptions\(\)/)
  assert.match(detailSource, /mode=\{assignmentMode\}/)
})

test("administrators retain the global assignment selector", () => {
  assert.ok(policy, "ticket assignment policy is missing")
  assert.equal(policy.getTicketAssignmentMode(["admin"]), "global")
  assert.equal(policy.getTicketAssignmentMode(["super_admin"]), "global")
  assert.match(assignSource, /mode === "team"\s*\?\s*fetchTicketCreateAssigneeOptions\(\)\s*:\s*fetchAgentProfilesAll\(\)/)
})
