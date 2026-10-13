// This file has been modified for the KestrelDesk project.

import assert from "node:assert/strict"
import test from "node:test"

import {
  handleAgentMessageCreated,
  isFullAgentMessageRecalledPayload,
  shouldReloadConversationListForRealtimePatch,
} from "./agent-conversation-realtime.ts"

test("reloads conversation list when realtime patch changes list membership fields", () => {
  assert.equal(
    shouldReloadConversationListForRealtimePatch({
      conversationId: 1,
      status: 3,
      currentAssigneeId: 101,
    }),
    true
  )
})

test("keeps local patching for message summary and unread-only changes", () => {
  assert.equal(
    shouldReloadConversationListForRealtimePatch({
      conversationId: 1,
      lastMessageId: 9,
      lastMessageSummary: "hello",
      agentUnreadCount: 1,
    }),
    false
  )
})

const context = {
  currentUserId: 101,
  hidden: true,
  activeConversationStatus: 3,
}
const metadata = {
  conversationId: 42,
  messageId: 9,
  senderType: "customer",
  status: 3,
  currentAssigneeId: 101,
}
const fullMessage = {
  id: 9,
  conversationId: 42,
  senderType: "customer",
  senderId: 501,
  senderName: "Synthetic customer",
  messageType: "text",
  content: "Synthetic private message",
  payload: '{"synthetic":"private attachment"}',
  sendStatus: 3,
  sentAt: "2026-10-04 12:00:00",
  customerRead: false,
  agentRead: false,
}

function captureEffects(normalize = () => {
  throw new Error("metadata must never be normalized")
}) {
  const calls = { normalized: [], applied: [], refreshed: [], notified: [] }
  const effects = {
    normalize: (payload) => {
      calls.normalized.push(payload)
      return normalize(payload)
    },
    applyMessage: (message) => { calls.applied.push(message) },
    refresh: async (conversationId) => { calls.refreshed.push(conversationId) },
    notify: (...args) => { calls.notified.push(args) },
  }
  return { calls, effects }
}

test("metadata message refreshes without normalization", async () => {
  const { calls, effects } = captureEffects()
  effects.applyMessage = () => { throw new Error("metadata must never be applied") }
  await handleAgentMessageCreated(metadata, context, effects)
  assert.deepEqual(calls, {
    normalized: [], applied: [], refreshed: [42], notified: [[42]],
  })
})

test("empty nested message refreshes without normalization", async () => {
  for (const message of [{}, { id: 0 }, { id: -1 }, { id: "9" }]) {
    const { calls, effects } = captureEffects()
    await handleAgentMessageCreated({ ...metadata, message }, context, effects)
    assert.deepEqual(calls, {
      normalized: [], applied: [], refreshed: [42], notified: [[42]],
    })
  }
  // Type alone is insufficient evidence of a legacy message body.
  const { calls, effects } = captureEffects()
  await handleAgentMessageCreated({ ...metadata, messageType: "text" }, context, effects)
  assert.deepEqual(calls.normalized, [])
  assert.deepEqual(calls.refreshed, [42])
})

test("full message applies without metadata refresh", async () => {
  const legacy = {
    ...metadata,
    senderId: fullMessage.senderId,
    senderName: fullMessage.senderName,
    messageType: fullMessage.messageType,
    content: fullMessage.content,
    payload: fullMessage.payload,
    sendStatus: fullMessage.sendStatus,
    sentAt: fullMessage.sentAt,
  }
  for (const payload of [{ ...metadata, message: fullMessage }, legacy]) {
    const { calls, effects } = captureEffects(() => fullMessage)
    await handleAgentMessageCreated(payload, context, effects)
    assert.deepEqual(calls.normalized, [payload])
    assert.deepEqual(calls.applied, [fullMessage])
    assert.equal(calls.applied[0], fullMessage)
    assert.deepEqual(calls.refreshed, [])
    assert.deepEqual(calls.notified, [[42]])
    assert.equal(calls.applied[0].content, "Synthetic private message")
    assert.equal(calls.applied[0].payload, '{"synthetic":"private attachment"}')
  }
  // Empty text and payload-only legacy messages remain valid full shapes.
  for (const body of [{ content: "" }, { payload: fullMessage.payload }]) {
    const { calls, effects } = captureEffects(() => fullMessage)
    await handleAgentMessageCreated({ ...metadata, messageType: "text", ...body }, context, effects)
    assert.deepEqual(calls.applied, [fullMessage])
    assert.deepEqual(calls.refreshed, [])
  }
})

test("notifications expose only conversation IDs", async () => {
  const { calls, effects } = captureEffects()
  await handleAgentMessageCreated(metadata, context, effects)
  assert.deepEqual(calls.notified, [[42]])
  const cases = [
    [metadata, { ...context, hidden: false }],
    [{ ...metadata, currentAssigneeId: 0 }, context],
    [{ ...metadata, currentAssigneeId: 102 }, context],
    [{ ...metadata, status: 2 }, context],
    [{ ...metadata, senderType: "agent" }, context],
    [{ ...metadata, senderType: "ai" }, context],
    [{ ...metadata, conversationId: 0 }, context],
    [{ ...metadata, conversationId: -42 }, context],
  ]
  for (const [payload, notificationContext] of cases) {
    const capture = captureEffects()
    await handleAgentMessageCreated(payload, notificationContext, capture.effects)
    assert.deepEqual(capture.calls.notified, [], JSON.stringify(payload))
  }
  for (const senderType of ["agent", "ai"]) {
    const capture = captureEffects(() => ({ ...fullMessage, senderType }))
    await handleAgentMessageCreated({ ...metadata, message: { ...fullMessage, senderType } }, context, capture.effects)
    assert.deepEqual(capture.calls.notified, [])
  }
})

test("invalid payload has no message effects", async () => {
  for (const payload of [null, undefined, {}, { ...metadata, conversationId: 0 }, { ...metadata, conversationId: -1 }, { ...metadata, conversationId: "42" }, { ...metadata, conversationId: NaN }]) {
    const { calls, effects } = captureEffects()
    await handleAgentMessageCreated(payload, context, effects)
    assert.deepEqual(calls, { normalized: [], applied: [], refreshed: [], notified: [] })
  }
})

test("failed full normalization refreshes without applying a message", async () => {
  const { calls, effects } = captureEffects(() => null)
  const payload = { ...metadata, message: fullMessage }
  await handleAgentMessageCreated(payload, context, effects)
  assert.deepEqual(calls.normalized, [payload])
  assert.deepEqual(calls.applied, [])
  assert.deepEqual(calls.refreshed, [42])
})

test("metadata refresh failures propagate to the hook error handler", async () => {
  const { effects } = captureEffects()
  const error = new Error("Synthetic refresh failure")
  effects.refresh = async () => { throw error }
  await assert.rejects(handleAgentMessageCreated(metadata, context, effects), error)
})

test("metadata recall is invalidation while full recall is a patch", () => {
  for (const payload of [null, undefined, { conversationId: 42, messageId: 9 }, { messageId: 9, sendStatus: 6 }, { messageId: 9, recalledAt: "2026-10-04 12:00:00" }, { messageId: 9, sendStatus: "6", recalledAt: "2026-10-04 12:00:00" }, { messageId: 9, sendStatus: 6, recalledAt: " " }]) {
    assert.equal(isFullAgentMessageRecalledPayload(payload), false)
  }
  assert.equal(isFullAgentMessageRecalledPayload({ messageId: 9, sendStatus: 6, recalledAt: "2026-10-04 12:00:00" }), true)
})
