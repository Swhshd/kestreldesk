// This file has been modified for the KestrelDesk project.

import type { AgentMessage } from "@/lib/api/agent"
import type {
  RealtimeConversationPatch,
  RealtimeMessageCreatedPayload,
} from "@/lib/im-realtime-state"

export type AgentMessageCreatedData = RealtimeMessageCreatedPayload<AgentMessage> & {
  status?: number
  currentAssigneeId?: number
}

export type AgentMessageCreatedContext = {
  currentUserId: number
  hidden: boolean
  activeConversationStatus: number
}

export type AgentMessageCreatedEffects = {
  normalize: (payload: AgentMessageCreatedData) => AgentMessage | null
  applyMessage: (message: AgentMessage) => void
  refresh: (conversationId?: number) => Promise<void>
  notify: (conversationId: number) => void
}

export async function handleAgentMessageCreated(
  payload: AgentMessageCreatedData | null | undefined,
  context: AgentMessageCreatedContext,
  effects: AgentMessageCreatedEffects
): Promise<void> {
  const conversationId = payload?.conversationId
  if (!payload || typeof conversationId !== "number" || !Number.isFinite(conversationId) || conversationId <= 0) {
    return
  }

  const hasNestedMessage =
    typeof payload.message?.id === "number" &&
    Number.isFinite(payload.message.id) &&
    payload.message.id > 0
  const hasLegacyMessage =
    typeof payload.messageType === "string" &&
    payload.messageType.length > 0 &&
    (Object.hasOwn(payload, "content") || Object.hasOwn(payload, "payload"))

  let senderType = payload.senderType
  if (hasNestedMessage || hasLegacyMessage) {
    const message = effects.normalize(payload)
    if (!message) {
      await effects.refresh(conversationId)
      return
    }
    effects.applyMessage(message)
    senderType = message.senderType
  } else {
    await effects.refresh(conversationId)
  }

  if (
    senderType === "customer" &&
    payload.status === context.activeConversationStatus &&
    (payload.currentAssigneeId ?? 0) > 0 &&
    payload.currentAssigneeId === context.currentUserId &&
    context.hidden
  ) {
    effects.notify(conversationId)
  }
}

export type AgentMessageRecalledData = {
  conversationId?: number
  messageId?: number
  sendStatus?: number
  recalledAt?: string
}

export function isFullAgentMessageRecalledPayload(
  payload: AgentMessageRecalledData | null | undefined
): boolean {
  return (
    typeof payload?.sendStatus === "number" &&
    typeof payload.recalledAt === "string" &&
    payload.recalledAt.trim().length > 0
  )
}

const listMembershipFields = new Set<keyof RealtimeConversationPatch>([
  "status",
  "currentAssigneeId",
  "currentTeamId",
])

export function shouldReloadConversationListForRealtimePatch(
  patch: RealtimeConversationPatch | null | undefined
) {
  if (!patch) {
    return false
  }
  return Object.keys(patch).some((key) =>
    listMembershipFields.has(key as keyof RealtimeConversationPatch)
  )
}
