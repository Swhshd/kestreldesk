import type { AgentConversation, AgentMessage } from "@/lib/api/agent"
import type { ImConversation, ImMessage } from "@/lib/api/im"
import { mergeImMessagesByIdAsc } from "@/lib/im-message-merge"
import { summarizeIMMessage } from "@/lib/im-message"

export type RealtimeMessage = AgentMessage | ImMessage
export type RealtimeConversation = AgentConversation | ImConversation

export type RealtimeMessageCreatedPayload<TMessage extends RealtimeMessage> = {
  conversationId?: number
  messageId?: number
  message?: TMessage
  senderType?: string
  senderId?: number
  senderName?: string
  senderAvatar?: string
  messageType?: string
  content?: string
  payload?: string
  sendStatus?: number
  sentAt?: string
}

export type RealtimeConversationPatch = Partial<RealtimeConversation> & {
  conversationId?: number
}

export function normalizeRealtimeMessage<TMessage extends RealtimeMessage>(
  payload: RealtimeMessageCreatedPayload<TMessage> | null | undefined
): TMessage | null {
  if (!payload) {
    return null
  }
  if (payload.message?.id) {
    return payload.message
  }
  const id = payload.messageId ?? 0
  const conversationId = payload.conversationId ?? 0
  if (id <= 0 || conversationId <= 0) {
    return null
  }
  return {
    id,
    conversationId,
    senderType: payload.senderType ?? "",
    senderId: payload.senderId ?? 0,
    senderName: payload.senderName,
    senderAvatar: payload.senderAvatar,
    messageType: payload.messageType ?? "",
    content: payload.content ?? "",
    payload: payload.payload,
    sendStatus: payload.sendStatus ?? 0,
    sentAt: payload.sentAt,
    customerRead: false,
    agentRead: false,
  } as TMessage
}

export function mergeRealtimeMessage<TMessage extends RealtimeMessage>(
  messages: TMessage[],
  message: TMessage | null | undefined
): TMessage[] {
  if (!message) {
    return messages
  }
  return mergeImMessagesByIdAsc(messages, [message])
}

export function patchConversation<TConversation extends RealtimeConversation>(
  conversation: TConversation | null,
  patch: RealtimeConversationPatch | null | undefined
): TConversation | null {
  if (!conversation || !patch) {
    return conversation
  }
  const id = patch.id ?? patch.conversationId
  if (!id || conversation.id !== id) {
    return conversation
  }
  const fields = { ...patch }
  delete fields.conversationId
  return {
    ...conversation,
    ...fields,
  } as TConversation
}

export function patchConversationList<TConversation extends RealtimeConversation>(
  conversations: TConversation[],
  patch: RealtimeConversationPatch | null | undefined
): TConversation[] {
  if (!patch) {
    return conversations
  }
  const id = patch.id ?? patch.conversationId
  if (!id) {
    return conversations
  }
  let changed = false
  const next = conversations.map((item) => {
    const patched = patchConversation(item, patch)
    if (patched !== item) {
      changed = true
    }
    return patched ?? item
  })
  return changed ? next : conversations
}

export function patchConversationWithMessage<
  TConversation extends RealtimeConversation,
  TMessage extends RealtimeMessage,
>(conversation: TConversation | null, message: TMessage | null | undefined) {
  if (!conversation || !message || conversation.id !== message.conversationId) {
    return conversation
  }
  return {
    ...conversation,
    lastMessageId: message.id,
    lastMessageAt: message.sentAt ?? conversation.lastMessageAt,
    lastActiveAt: message.sentAt ?? conversation.lastActiveAt,
    lastMessageSummary: summarizeIMMessage(message),
  } as TConversation
}

export function patchConversationListWithMessage<
  TConversation extends RealtimeConversation,
  TMessage extends RealtimeMessage,
>(conversations: TConversation[], message: TMessage | null | undefined) {
  if (!message) {
    return conversations
  }
  let changed = false
  const next = conversations.map((item) => {
    const patched = patchConversationWithMessage(item, message)
    if (patched !== item) {
      changed = true
    }
    return patched ?? item
  })
  return changed ? next : conversations
}

export function markMessagesReadToMessageId<TMessage extends RealtimeMessage>(
  messages: TMessage[],
  messageId: number,
  reader: "agent" | "customer",
  readAt?: string
): TMessage[] {
  if (messageId <= 0) {
    return messages
  }
  let changed = false
  const next = messages.map((message) => {
    if (message.id > messageId) {
      return message
    }
    if (reader === "agent") {
      if (message.agentRead && message.agentReadAt === readAt) {
        return message
      }
      changed = true
      return { ...message, agentRead: true, agentReadAt: readAt } as TMessage
    }
    if (message.customerRead && message.customerReadAt === readAt) {
      return message
    }
    changed = true
    return { ...message, customerRead: true, customerReadAt: readAt } as TMessage
  })
  return changed ? next : messages
}
