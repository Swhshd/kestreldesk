import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import test from "node:test"

const policy = await import("./ticket-assignee-policy.ts").catch(() => null)
const editSource = await readFile(
  new URL("../app/(dashboard)/dashboard/tickets/_components/edit.tsx", import.meta.url),
  "utf8"
)
const conversationCreateSource = await readFile(
  new URL(
    "../app/(dashboard)/dashboard/tickets/_components/create-ticket-from-conversation-dialog.tsx",
    import.meta.url,
  ),
  "utf8",
)

test("ordinary agents get a fixed assignee and never load assignee choices", () => {
  assert.ok(policy, "ticket assignee policy is missing")
  assert.equal(policy.getTicketAssigneeMode(["cs_user"]), "self")
  assert.deepEqual(
    policy.getTicketAssigneeField({
      mode: "self",
      currentUserId: 17,
      currentUserName: "Agent One",
      agents: [{ userId: 18, displayName: "Agent Two" }],
    }),
    { kind: "fixed", userId: 17, label: "Agent One" }
  )
  assert.match(editSource, /assigneeMode === "self"/)
  assert.match(editSource, /if \(assigneeMode === "self"\) \{\s*delete payload\.currentAssigneeId/)
  const fixedAssigneeControl = editSource.match(
    /\{assigneeField\.kind === "fixed" \? \(\s*fixedConversationId \? null : \(([\s\S]*?)\)\s*\)\s*:/,
  )?.[1]
  assert.ok(fixedAssigneeControl, "ordinary agent fixed-assignee control was not found")
  assert.match(fixedAssigneeControl, /<output[\s\S]*?assigneeField\.label/)
  assert.doesNotMatch(fixedAssigneeControl, /<Input|<OptionCombobox|<Controller/)
  assert.match(editSource, /fetchTicketCreateAssigneeOptions/)
  assert.match(
    editSource,
    /const assigneeRequest =\s*assigneeMode === "team"\s*\? fetchTicketCreateAssigneeOptions\(\)\s*:\s*assigneeMode === "global"\s*\? fetchAgentProfilesAll\(\)\s*:\s*Promise\.resolve\(\[\]\)/
  )
})

test("conversation Ticket creation shows ownership context as read-only", () => {
  assert.match(conversationCreateSource, /fixedCustomerLabel=\{conversation\?\.customerName\}/)
  const requestFields = conversationCreateSource.match(
    /await createTicketFromConversation\(\{([\s\S]*?)\}\)/,
  )?.[1]
  assert.ok(requestFields, "conversation creation request was not found")
  assert.match(requestFields, /conversationId: conversation\.id/)
  assert.doesNotMatch(requestFields, /customerId/)
  assert.match(editSource, /t\("ticket\.linkedCustomer"\)/)
  assert.match(editSource, /t\("ticket\.sourceConversation"\)/)
  assert.match(editSource, /<dd[^>]*>\s*#\{fixedConversationId\}/)
  assert.match(editSource, /!itemId && fixedConversationId/)
  assert.match(editSource, /assigneeField\.kind === "fixed"[\s\S]*?t\("ticket\.assignee"\)[\s\S]*?assigneeField\.label/)
})

test("team leaders receive only server-provided team assignee options", () => {
  assert.ok(policy, "ticket assignee policy is missing")
  assert.equal(policy.getTicketAssigneeMode(["cs_team_leader"]), "team")
  assert.deepEqual(
    policy.getTicketAssigneeField({
      mode: "team",
      currentUserId: 17,
      currentUserName: "Leader",
      agents: [
        { userId: 17, displayName: "Leader" },
        { userId: 18, displayName: "Same team" },
      ],
    }),
    {
      kind: "select",
      options: [
        { value: "17", label: "Leader" },
        { value: "18", label: "Same team" },
      ],
    }
  )
  assert.match(editSource, /assigneeMode === "team"/)
  assert.match(editSource, /assigneeMode === "team"\s*\? fetchTicketCreateAssigneeOptions\(\)/)
})

test("administrators retain the global assignee selector", () => {
  assert.ok(policy, "ticket assignee policy is missing")
  assert.equal(policy.getTicketAssigneeMode(["admin"]), "global")
  assert.equal(policy.getTicketAssigneeMode(["super_admin"]), "global")
  assert.deepEqual(
    policy.getTicketAssigneeField({
      mode: "global",
      currentUserId: 17,
      currentUserName: "Admin",
      agents: [{ userId: 18, displayName: "Any agent" }],
    }),
    {
      kind: "select",
      options: [
        { value: "0", label: "No assignee" },
        { value: "18", label: "Any agent" },
      ],
    }
  )
  assert.match(editSource, /assigneeMode === "global"/)
  assert.match(editSource, /fetchAgentProfilesAll/)
})
