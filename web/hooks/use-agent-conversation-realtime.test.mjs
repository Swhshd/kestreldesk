import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import test from "node:test"

const hookSource = await readFile(
  new URL("./use-agent-conversation-realtime.ts", import.meta.url),
  "utf8"
)
const monitorSource = await readFile(
  new URL("../app/(dashboard)/dashboard/conversation-monitor/page.tsx", import.meta.url),
  "utf8"
)

test("message events use the metadata-aware helper and generated active status", () => {
  assert.match(hookSource, /import\s*\{[^}]*handleAgentMessageCreated[^}]*\}\s*from\s*"@\/lib\/agent-conversation-realtime"/s)
  assert.match(hookSource, /handleAgentMessageCreated\(\s*payload,/)
  assert.match(hookSource, /activeConversationStatus:\s*IMConversationStatus\.Active/)
  assert.match(hookSource, /normalize:\s*normalizeRealtimeMessage/)
  assert.match(hookSource, /applyMessage:\s*store\.applyRealtimeMessageCreated/)
  assert.match(hookSource, /refresh:\s*store\.resyncRealtimeData/)
  assert.match(hookSource, /\.catch\([\s\S]*?t\("conversation\.syncMessagesFailed"\)/)
})

test("notifications use generic text and only the conversation ID", () => {
  assert.doesNotMatch(hookSource, /getNotificationBody/)
  assert.match(hookSource, /notify:\s*\(conversationId\)\s*=>/)
  assert.match(hookSource, /showNotification\(\s*t\("conversation\.newMessage"\),\s*t\("conversation\.newMessage"\),/)
  assert.match(hookSource, /store\.selectConversation\(conversationId\)/)
})

test("metadata recall refreshes while complete recall fields patch the transcript", () => {
  assert.match(hookSource, /import\s*\{[^}]*isFullAgentMessageRecalledPayload[^}]*\}\s*from\s*"@\/lib\/agent-conversation-realtime"/s)
  assert.match(hookSource, /if\s*\(isFullAgentMessageRecalledPayload\(payload\)\)\s*\{[\s\S]*?store\.applyRealtimeMessageRecalled\(payload\.messageId,/)
  assert.match(hookSource, /eventType === "message\.recalled"[\s\S]*?else\s*\{\s*void store\.resyncRealtimeData\(conversationId\)/)
})

test("agent realtime uses the shared profile validator without socket recreation dependency", () => {
  assert.match(hookSource, /import\s*\{\s*useAuth\s*\}\s*from\s*"@\/components\/auth-provider"/)
  assert.match(hookSource, /beforeReconnect:\s*\(\)\s*=>\s*validateRealtimeProfileRef\.current\(\)/)
  assert.match(hookSource, /validateRealtimeProfileRef\.current\s*=\s*validateRealtimeProfile/)
  const connectionEffect = hookSource.match(/useEffect\(\(\)\s*=>\s*\{[\s\S]*?realtime\.connect\(\)[\s\S]*?\},\s*\[([^\]]*)\]\)/)
  assert.ok(connectionEffect, "manager effect remains explicit")
  assert.doesNotMatch(connectionEffect[1], /validateRealtimeProfile/)
})

test("Monitor reloads the queue and matching detail without reading message bodies", () => {
  const onMessage = monitorSource.match(/socket\.onmessage = \(event\) => \{([\s\S]*?)\n\s*socket\.onclose/)
  assert.ok(onMessage, "Monitor onmessage handler exists at the current route")
  assert.match(onMessage[1], /void loadConversations\(\)/)
  assert.match(onMessage[1], /if \(conversationId > 0 && currentDetail\?\.id === conversationId\)\s*\{\s*void loadDetail\(currentDetail\)/)
  assert.doesNotMatch(onMessage[1], /\.(?:message|content|payload)\b/)
})
