import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import test from "node:test"

const ux = await import("./agent-conversation-ux.ts").catch(() => null)
const agentApiSource = await readFile(new URL("./api/agent.ts", import.meta.url), "utf8")
const chatPanelSource = await readFile(
  new URL("../app/(dashboard)/dashboard/conversations/_components/chat-panel.tsx", import.meta.url),
  "utf8"
)
const messages = Object.fromEntries(
  await Promise.all(
    ["zh-CN", "en-US"].map(async (locale) => [
      locale,
      JSON.parse(await readFile(new URL(`../messages/${locale}.json`, import.meta.url), "utf8")),
    ])
  )
)

test("only permitted cs_user can self-claim unassigned human-waiting conversations", () => {
  assert.ok(ux, "conversation UX policy is missing")
  assert.equal(
    ux.canSelfClaimAgentConversation(["cs_user"], ["conversation.claim"], true, 0, false),
    true
  )
  assert.equal(
    ux.canSelfClaimAgentConversation(["cs_user"], ["conversation.claim"], true, 12, false),
    false
  )
  assert.equal(
    ux.canSelfClaimAgentConversation(["cs_user"], ["conversation.claim"], false, 0, false),
    false
  )
  assert.equal(
    ux.canSelfClaimAgentConversation(["cs_user"], ["conversation.claim"], true, 0, true),
    false
  )
  assert.equal(
    ux.canSelfClaimAgentConversation(["cs_user"], [], true, 0, false),
    false
  )
  assert.equal(
    ux.canSelfClaimAgentConversation(["cs_team_leader"], ["conversation.claim"], true, 0, false),
    false
  )
})

test("recall actions match effective recall permission and own-message rules", () => {
  assert.ok(ux, "conversation UX policy is missing")
  const ownAgentMessage = { senderType: "agent", senderId: 12 }
  assert.equal(ux.canRecallAgentMessage([], ownAgentMessage, 12), false)
  assert.equal(ux.canRecallAgentMessage(["conversation.recall"], ownAgentMessage, 12), true)
  assert.equal(ux.canRecallAgentMessage(["conversation.recall"], ownAgentMessage, 8), false)
  assert.equal(ux.canRecallAgentMessage(["conversation.recall"], { senderType: "customer", senderId: 12 }, 12), false)
})

test("self-claim API sends only the conversation ID to the dedicated endpoint", () => {
  const claimFunction = agentApiSource.match(
    /export function claimAgentConversation\([\s\S]*?\n}/
  )?.[0]
  assert.ok(claimFunction, "dedicated self-claim API helper is missing")
  assert.match(claimFunction, /\/api\/dashboard\/conversation\/claim/)
  assert.match(claimFunction, /JSON\.stringify\(\{ conversationId \}\)/)
  assert.doesNotMatch(claimFunction, /assigneeId|teamId|userId/)
})

test("chat panel wires the self-claim and recall presentation rules", () => {
  assert.match(chatPanelSource, /canSelfClaimAgentConversation/)
  const claimHandler = chatPanelSource.match(
    /const handleClaim = async \(\) => \{([\s\S]*?)\n  \};/
  )?.[0]
  assert.ok(claimHandler, "claim handler is missing")
  assert.match(claimHandler, /if \(isCsUser\) \{[\s\S]*claimAgentConversation\(conversation\.id\)/)
  assert.match(claimHandler, /else \{[\s\S]*assignAgentConversation\(/)
  assert.match(chatPanelSource, /action=\{!isCsUser \|\| canSelfClaim \? \(/)
  assert.match(chatPanelSource, /canRecall=\{canRecallAgentMessage\(userPermissions, message, currentUserId\)\}/)
  assert.match(chatPanelSource, /claimUnavailable/)
})

test("claim-unavailable feedback is localized in zh-CN and en-US", () => {
  for (const locale of ["zh-CN", "en-US"]) {
    assert.ok(messages[locale].conversation.claimUnavailable)
  }
})
