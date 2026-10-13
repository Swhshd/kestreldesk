import assert from "node:assert/strict"
import { readFile } from "node:fs/promises"
import test from "node:test"

const realtimeSource = await readFile(
  new URL("../im-realtime.ts", import.meta.url),
  "utf8"
)
const supportChatSource = await readFile(
  new URL("./support-chat.ts", import.meta.url),
  "utf8"
)

test("customer WebSocket keeps the existing endpoint and does not build a topic", () => {
  assert.match(
    realtimeSource,
    /new WebSocket\([\s\S]*?`\$\{baseUrl\}\/api\/ws\/open\?channelId=\$\{channelId\}&customerSessionToken=\$\{encodeURIComponent\(customerSessionToken\)\}`\s*\)/
  )
  assert.match(realtimeSource, /const channelId = encodeURIComponent\(config\.channelId \|\| ""\)/)
  assert.match(realtimeSource, /const customerSessionToken = getCustomerSessionToken\(\)/)
  assert.doesNotMatch(realtimeSource, /customer:\$\{|guest:\$\{/)
  assert.doesNotMatch(supportChatSource, /customer:\$\{|guest:\$\{/)
})

test("connected topic values are ignored and customer events use payload conversationId", () => {
  const handlerStart = supportChatSource.indexOf(
    "    onMessage: (messageEvent, socket) => {"
  )
  const handlerEnd = supportChatSource.indexOf("\n  })", handlerStart)
  assert.ok(handlerStart >= 0, "support-chat realtime message handler exists")
  assert.ok(handlerEnd > handlerStart, "support-chat realtime message handler ends")

  const handler = supportChatSource.slice(handlerStart, handlerEnd)
  assert.match(handler, /const payload = event\.data \?\? event\.payload/)
  assert.doesNotMatch(
    handler,
    /event\.topic|event\.data\.topics|event\.payload\.topics/
  )
  assert.doesNotMatch(handler, /\btopics\b/)

  const conversationGuard = handler.indexOf(
    "if (payload?.conversationId !== conversationId)"
  )
  const messageMutation = handler.indexOf(
    "messages: mergeImMessagesByIdAsc(state.messages, [message])"
  )
  const conversationMutation = handler.indexOf(
    "conversation: patchConversation(state.conversation, payload)"
  )
  assert.ok(
    conversationGuard >= 0,
    "protected customer updates use payload conversationId"
  )
  assert.ok(
    messageMutation > conversationGuard,
    "conversation guard precedes message updates"
  )
  assert.ok(
    conversationMutation > conversationGuard,
    "conversation guard precedes conversation updates"
  )
  assert.doesNotMatch(supportChatSource, /customer:\$\{|guest:\$\{/)
})
