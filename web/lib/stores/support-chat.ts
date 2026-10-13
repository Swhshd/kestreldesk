"use client"

// This file has been modified for the KestrelDesk project.

import { create } from "zustand"

import {
  closeImConversation,
  createOrMatchImConversation,
  beginCustomerSessionBootstrap,
  departCustomerSessionIdentity,
  ensureCustomerSession,
  isCustomerSessionContinuityLost,
  fetchImMessages,
  fetchImWidgetConfig,
  markImMessageRead,
  sendImMessage,
  uploadImAttachment,
  uploadImImage,
  applyCustomerSessionRefresh,
  type ImAsset,
  type ImConversation,
  type ImCustomerSession,
  type ImCustomerSessionCustomer,
  type ImMessage,
  type ImWidgetConfig,
} from "@/lib/api/im"
import {
  captureCustomerSessionScope,
  isCustomerSessionAttemptCurrent,
  isCustomerSessionScopeCurrent,
  type CustomerSessionScope,
} from "@/lib/api/im-session-scope"
import {
  createImRealtimeConnection,
  type ImRealtimeEnvelope,
} from "@/lib/im-realtime"
import {
  cursorFromLoadedImMessages,
  hasMoreAfterLatestImMessageMerge,
  mergeImMessagesByIdAsc,
  parseImMessageCursorId,
} from "@/lib/im-message-merge"
import {
  markMessagesReadToMessageId,
  normalizeRealtimeMessage,
  patchConversation,
  patchConversationWithMessage,
} from "@/lib/im-realtime-state"
import { summarizeIMMessage } from "@/lib/im-message"
import { createRealtimeConnectionManager } from "@/lib/realtime-connection"
import { generateUUID } from "@/lib/utils"
import {
  readSupportChatRuntimeConfig,
  setSupportChatRuntimeConfig,
} from "@/lib/sdk/runtime-config"
import { translateCurrentMessage } from "@/i18n/messages"

type ChatStatus = "connecting" | "connected" | "disconnected"

const DEFAULT_PAGE_LIMIT = 50

function getNotificationBody(message: ImMessage): string {
  return summarizeIMMessage(message)
}

function showNotification(title: string, body: string, isCurrent: () => boolean, onClick?: () => void) {
  if (typeof window === "undefined" || !("Notification" in window)) {
    return
  }

  const create = () => {
    if (!isCurrent()) {
      return
    }
    const notification = new Notification(title, { body })
    notification.onclick = () => {
      if (!isCurrent()) {
        notification.close()
        return
      }
      window.focus()
      onClick?.()
      notification.close()
    }
  }

  if (Notification.permission === "granted") {
    create()
    return
  }

  if (Notification.permission === "default") {
    void Notification.requestPermission().then((permission) => {
      if (permission === "granted") {
        create()
      }
    })
  }
}

function ensureMessageList(value: ImMessage[] | null | undefined): ImMessage[] {
  return Array.isArray(value) ? value : []
}

function markConversationReadMessages(
  messages: ImMessage[],
  payload: ImRealtimeEnvelope["data"] | ImRealtimeEnvelope["payload"]
) {
  let next = messages
  if ((payload?.agentLastReadMessageId ?? 0) > 0) {
    next = markMessagesReadToMessageId(
      next,
      payload?.agentLastReadMessageId ?? 0,
      "agent",
      payload?.agentLastReadAt
    )
  }
  if ((payload?.customerLastReadMessageId ?? 0) > 0) {
    next = markMessagesReadToMessageId(
      next,
      payload?.customerLastReadMessageId ?? 0,
      "customer",
      payload?.customerLastReadAt
    )
  }
  return next
}

export type SupportChatStore = {
  title: string
  subtitle: string
  themeColor: string
  customer: ImCustomerSessionCustomer | null
  customerChannelId: string
  conversation: ImConversation | null
  messages: ImMessage[]
  messagesCursor: string
  messagesHasMore: boolean
  messagesLoadingMore: boolean
  initialized: boolean
  bootstrapUnavailable: boolean
  status: ChatStatus
  error: string
  sending: boolean
  uploadingAsset: boolean
  closingConversation: boolean
  isOpen: boolean
  isVisible: boolean
  socket: WebSocket | null
  readingMessageId: number

  setIsOpen: (isOpen: boolean) => void
  setIsVisible: (isVisible: boolean) => void
  bootstrap: () => void
  disconnectSocket: () => void
  refreshMessages: () => Promise<void>
  syncLatestMessages: () => Promise<void>
  loadOlderMessages: () => Promise<void>
  markConversationRead: () => Promise<void>
  handleSendMessage: (content: string) => Promise<void>
  sendMessage: (content: string) => Promise<void>
  uploadMessageImage: (file: File) => Promise<ImAsset | null>
  sendAttachment: (file: File) => Promise<void>
  closeConversation: () => Promise<void>
  retry: () => Promise<void>
}

let bootstrapToken = 0

function t(key: string) {
  return translateCurrentMessage(key)
}

export const useSupportChatStore = create<SupportChatStore>((set, get) => {
  let resettingCustomerState = false
  const socketScopes = new WeakMap<WebSocket, CustomerSessionScope>()
  const isCurrentConversation = (scope: CustomerSessionScope | null, conversationId: number) =>
    isCustomerSessionScopeCurrent(scope) && get().conversation?.id === conversationId
  const realtime = createRealtimeConnectionManager({
    createSocket: () => {
      const scope = captureCustomerSessionScope()
      const socket = createImRealtimeConnection()
      if (scope) {
        socketScopes.set(socket, scope)
      }
      return socket
    },
    canReconnect: () => Boolean(get().isOpen && get().conversation?.id),
    onStatusChange: (status) => {
      if (!resettingCustomerState && (get().isOpen || status === "disconnected")) {
        set({ status })
      }
    },
    onSocketChange: (socket) => {
      if (!resettingCustomerState) {
        set({ socket })
      }
    },
    onMessage: (messageEvent, socket) => {
      const scope = socketScopes.get(socket) ?? null
      if (!isCustomerSessionScopeCurrent(scope)) {
        return
      }
      let event: ImRealtimeEnvelope
      try {
        event = JSON.parse(messageEvent.data) as ImRealtimeEnvelope
      } catch {
        return
      }

      const payload = event.data ?? event.payload
      if (event.type === "customer_session.refresh") {
        applyCustomerSessionRefresh(payload, scope)
        return
      }

      const conversationId = get().conversation?.id
      if (!conversationId) {
        return
      }

      if (event.type === "resyncRequired") {
        void get().refreshMessages()
        return
      }
      if (payload?.conversationId !== conversationId) {
        return
      }

      if (event.type === "message.created") {
        const message = normalizeRealtimeMessage<ImMessage>(payload)
        if (!message) {
          void get().syncLatestMessages()
          return
        }
        set((state) => ({
          messages: mergeImMessagesByIdAsc(state.messages, [message]),
          conversation: patchConversationWithMessage(state.conversation, message),
        }))
        if (
          message.senderType !== "customer" &&
          typeof document !== "undefined" &&
          document.visibilityState !== "visible"
        ) {
          const state = get()
          showNotification(
            t("supportChat.newMessage"),
            getNotificationBody(message),
            () => isCurrentConversation(scope, conversationId),
            () => {
              state.setIsOpen(true)
              state.setIsVisible(true)
            }
          )
        }
        return
      }

      if (event.type?.startsWith("conversation.")) {
        set((state) => ({
          conversation: patchConversation(state.conversation, payload),
          messages: markConversationReadMessages(state.messages, payload),
        }))
      }
    },
  })

  const closeSocket = (options?: { reconnect?: boolean }) => {
    realtime.disconnect({
      reconnect: options?.reconnect ?? false,
      updateStatus: true,
    })
  }

  const connectSocket = () => {
    if (!get().conversation?.id) {
      return
    }
    const socket = get().socket
    if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) {
      return
    }
    realtime.connect()
  }

  const resetCustomerState = (customer: ImCustomerSessionCustomer | null, channelId: string): void => {
    resettingCustomerState = true
    try {
      realtime.disconnect({ reconnect: false, updateStatus: false })
    } finally {
      resettingCustomerState = false
    }
    set({
      customer,
      customerChannelId: channelId,
      conversation: null,
      messages: [],
      messagesCursor: "",
      messagesHasMore: false,
      messagesLoadingMore: false,
      initialized: false,
      bootstrapUnavailable: false,
      error: "",
      sending: false,
      uploadingAsset: false,
      closingConversation: false,
      readingMessageId: 0,
      socket: null,
      status: "connecting",
    })
  }

  const acceptCustomerSession = (session: ImCustomerSession): void => {
    const state = get()
    if (state.customer?.id !== session.customer.id || state.customerChannelId !== session.channelId) {
      // The API has already activated the accepted identity scope.
      resetCustomerState(session.customer, session.channelId)
      return
    }
    set({ customer: session.customer, customerChannelId: session.channelId })
  }

  return {
    title: t("supportChat.title"),
    subtitle: "",
    themeColor: "#2563eb",
    customer: null,
    customerChannelId: "",
    conversation: null,
    messages: [],
    messagesCursor: "",
    messagesHasMore: false,
    messagesLoadingMore: false,
    initialized: false,
    bootstrapUnavailable: false,
    status: "connecting",
    error: "",
    sending: false,
    uploadingAsset: false,
    closingConversation: false,
    isOpen: typeof window !== "undefined" ? window.self === window.top : false,
    isVisible:
      typeof window !== "undefined" ? window.self === window.top : false,
    socket: null,
    readingMessageId: 0,

    setIsOpen: (isOpen: boolean) => {
      set({ isOpen })
    },

    setIsVisible: (isVisible: boolean) => {
      set({ isVisible })
    },

    bootstrap: () => {
      const token = ++bootstrapToken
      let attempt = beginCustomerSessionBootstrap()
      // A valid proof inside the cache margin renews without departing identity.
      if (isCustomerSessionContinuityLost()) {
        departCustomerSessionIdentity()
        resetCustomerState(null, readSupportChatRuntimeConfig().channelId || "")
      }
      const isCurrentBootstrap = () => bootstrapToken === token &&
        isCustomerSessionAttemptCurrent(attempt) && get().isOpen

      if (!get().isOpen) {
        closeSocket({ reconnect: false })
        set({ status: "disconnected" })
        return
      }

      const activateChat = async () => {
        try {
          set({
            error: "",
            bootstrapUnavailable: false,
            status: get().socket?.readyState === WebSocket.OPEN ? "connected" : "connecting",
          })

          let widgetConfig: ImWidgetConfig = {}
          try {
            widgetConfig = await fetchImWidgetConfig()
          } catch (error) {
            if (!isCurrentBootstrap()) {
              return
            }
            if (!get().initialized) {
              set({
                bootstrapUnavailable: true,
                status: "disconnected",
                error: error instanceof Error ? error.message : t("supportChat.initFailed"),
              })
              return
            }
          }
          if (!isCurrentBootstrap()) {
            return
          }

          if (widgetConfig.channelId && widgetConfig.channelId !== readSupportChatRuntimeConfig().channelId) {
            setSupportChatRuntimeConfig({
              ...readSupportChatRuntimeConfig(),
              channelId:
                widgetConfig.channelId || readSupportChatRuntimeConfig().channelId,
            })
            departCustomerSessionIdentity()
            resetCustomerState(null, widgetConfig.channelId)
            attempt = beginCustomerSessionBootstrap()
          } else if (isCustomerSessionContinuityLost()) {
            // Proof can disappear or expire while widget configuration is pending.
            departCustomerSessionIdentity()
            resetCustomerState(null, readSupportChatRuntimeConfig().channelId || "")
          }

          set({
            title: widgetConfig.title || get().title,
            subtitle: widgetConfig.subtitle ?? get().subtitle,
            themeColor: widgetConfig.themeColor || get().themeColor,
          })

          const session = await ensureCustomerSession(attempt)
          if (!isCurrentBootstrap()) {
            return
          }
          acceptCustomerSession(session)

          let currentConversation = get().conversation
          if (!get().initialized || !currentConversation) {
            currentConversation = await createOrMatchImConversation()
            if (!isCurrentBootstrap()) {
              return
            }
            set({ initialized: true, conversation: currentConversation })
          }

          await get().refreshMessages()
          if (!isCurrentBootstrap()) {
            return
          }

          connectSocket()
        } catch (error) {
          if (!isCurrentBootstrap()) {
            return
          }
          set({
            status: "disconnected",
            error: error instanceof Error ? error.message : t("supportChat.initFailed"),
          })
        }
      }

      void activateChat()
    },

    disconnectSocket: () => {
      closeSocket({ reconnect: false })
    },

    refreshMessages: async () => {
      const scope = captureCustomerSessionScope()
      const conversationId = get().conversation?.id
      if (!conversationId) {
        return
      }

      try {
        const page = await fetchImMessages({
          conversationId,
          limit: DEFAULT_PAGE_LIMIT,
        })
        if (!isCurrentConversation(scope, conversationId)) {
          return
        }
        const results = ensureMessageList(page.results)
        set({
          messages: results,
          messagesCursor: cursorFromLoadedImMessages(results) || page.cursor || "",
          messagesHasMore: Boolean(page.hasMore) || results.length >= DEFAULT_PAGE_LIMIT,
        })
      } catch (error) {
        if (!isCurrentConversation(scope, conversationId)) {
          return
        }
        set({
          error: error instanceof Error ? error.message : t("supportChat.loadMessagesFailed"),
        })
        throw error
      }
    },

    syncLatestMessages: async () => {
      const scope = captureCustomerSessionScope()
      const conversationId = get().conversation?.id
      if (!conversationId) {
        return
      }

      try {
        const page = await fetchImMessages({
          conversationId,
          limit: DEFAULT_PAGE_LIMIT,
        })
        if (!isCurrentConversation(scope, conversationId)) {
          return
        }
        const batch = ensureMessageList(page.results)
        if (batch.length === 0) {
          return
        }
        set((state) => {
          const merged = mergeImMessagesByIdAsc(state.messages, batch)
          return {
            messages: merged,
            messagesCursor: cursorFromLoadedImMessages(merged) || page.cursor || "",
            messagesHasMore: hasMoreAfterLatestImMessageMerge({
              previousMessages: state.messages,
              previousHasMore: state.messagesHasMore,
              merged,
              apiHasMore: Boolean(page.hasMore) || batch.length >= DEFAULT_PAGE_LIMIT,
            }),
          }
        })
      } catch (error) {
        if (!isCurrentConversation(scope, conversationId)) {
          return
        }
        set({
          error: error instanceof Error ? error.message : t("supportChat.syncMessagesFailed"),
        })
      }
    },

    loadOlderMessages: async () => {
      const scope = captureCustomerSessionScope()
      const conversationId = get().conversation?.id
      if (
        !conversationId ||
        get().messagesLoadingMore ||
        !get().messagesHasMore
      ) {
        return
      }

      const cursorId = parseImMessageCursorId(get().messagesCursor)
      if (cursorId <= 0) {
        return
      }

      set({ messagesLoadingMore: true })
      try {
        const page = await fetchImMessages({
          conversationId,
          cursor: cursorId,
          limit: DEFAULT_PAGE_LIMIT,
        })
        if (!isCurrentConversation(scope, conversationId)) {
          return
        }
        const results = ensureMessageList(page.results)
        set((state) => {
          const merged = mergeImMessagesByIdAsc(
            ensureMessageList(state.messages),
            results
          )
          return {
            messages: merged,
            messagesCursor: cursorFromLoadedImMessages(merged) || page.cursor || "",
            messagesHasMore: Boolean(page.hasMore) || results.length >= DEFAULT_PAGE_LIMIT,
            messagesLoadingMore: false,
          }
        })
      } catch (error) {
        if (!isCurrentConversation(scope, conversationId)) {
          return
        }
        set({
          messagesLoadingMore: false,
          error: error instanceof Error ? error.message : t("supportChat.loadHistoryFailed"),
        })
        throw error
      }
    },

    markConversationRead: async () => {
      const scope = captureCustomerSessionScope()
      const state = get()
      const conversation = state.conversation
      const lastMessage = state.messages.at(-1)
      if (!conversation?.id || !lastMessage) {
        return
      }

      if (
        conversation.customerUnreadCount <= 0 &&
        conversation.customerLastReadMessageId >= lastMessage.id
      ) {
        return
      }
      if (state.readingMessageId === lastMessage.id) {
        return
      }

      set({ readingMessageId: lastMessage.id })
      try {
        await markImMessageRead(conversation.id, lastMessage.id)
        if (!isCurrentConversation(scope, conversation.id)) {
          return
        }
        set((current) => ({
          readingMessageId: 0,
          messages: current.messages.map((item) => {
            if (item.id > lastMessage.id) {
              return item
            }
            return item.customerRead ? item : { ...item, customerRead: true }
          }),
          conversation: current.conversation
            ? {
                ...current.conversation,
                customerUnreadCount: 0,
                customerLastReadMessageId: lastMessage.id,
              }
            : null,
        }))
      } catch (error) {
        if (!isCurrentConversation(scope, conversation.id)) {
          return
        }
        set({ readingMessageId: 0 })
        throw error
      }
    },

    handleSendMessage: async (content: string) => {
      const scope = captureCustomerSessionScope()
      const conversationId = get().conversation?.id
      if (!conversationId) {
        return
      }

      set({ error: "", sending: true })
      try {
        const nextMessage = await sendImMessage({
          conversationId,
          messageType: "html",
          content,
          clientMsgId: `support_chat_html_${generateUUID()}`,
        })
        if (!isCurrentConversation(scope, conversationId)) {
          return
        }
        set((state) => ({
          sending: false,
          messages: state.messages.some((message) => message.id === nextMessage.id)
            ? state.messages.map((message) =>
                message.id === nextMessage.id ? nextMessage : message
              )
            : [...state.messages, nextMessage],
          conversation: state.conversation
            ? {
                ...state.conversation,
                customerLastReadMessageId: nextMessage.id,
                customerUnreadCount: 0,
                lastMessageAt: nextMessage.sentAt,
                lastMessageSummary: summarizeIMMessage(nextMessage),
              }
            : null,
        }))
      } catch (error) {
        if (!isCurrentConversation(scope, conversationId)) {
          return
        }
        set({
          sending: false,
          error: error instanceof Error ? error.message : t("supportChat.sendMessageFailed"),
        })
        throw error
      }
    },

    sendMessage: async (content: string) => {
      return get().handleSendMessage(content)
    },

    uploadMessageImage: async (file: File) => {
      const scope = captureCustomerSessionScope()
      const conversationId = get().conversation?.id
      if (!conversationId) {
        return null
      }

      set({ error: "", uploadingAsset: true })
      try {
        const asset = await uploadImImage(conversationId, file)
        return isCurrentConversation(scope, conversationId) ? asset : null
      } catch (error) {
        if (!isCurrentConversation(scope, conversationId)) {
          return null
        }
        set({
          error: error instanceof Error ? error.message : t("supportChat.uploadImageFailed"),
        })
        return null
      } finally {
        if (isCurrentConversation(scope, conversationId)) {
          set({ uploadingAsset: false })
        }
      }
    },

    sendAttachment: async (file: File) => {
      const scope = captureCustomerSessionScope()
      const conversationId = get().conversation?.id
      if (!conversationId) {
        return
      }

      set({ error: "", uploadingAsset: true })
      try {
        const asset = await uploadImAttachment(conversationId, file)
        if (!isCurrentConversation(scope, conversationId)) {
          return
        }
        const nextMessage = await sendImMessage({
          conversationId,
          messageType: "attachment",
          content: asset.filename,
          payload: JSON.stringify({ assetId: asset.assetId }),
          clientMsgId: `support_chat_attachment_${generateUUID()}`,
        })
        if (!isCurrentConversation(scope, conversationId)) {
          return
        }
        set((state) => ({
          uploadingAsset: false,
          messages: state.messages.some((message) => message.id === nextMessage.id)
            ? state.messages.map((message) =>
                message.id === nextMessage.id ? nextMessage : message
              )
            : [...state.messages, nextMessage],
          conversation: state.conversation
            ? {
                ...state.conversation,
                customerLastReadMessageId: nextMessage.id,
                customerUnreadCount: 0,
                lastMessageAt: nextMessage.sentAt,
                lastMessageSummary: summarizeIMMessage(nextMessage),
              }
            : null,
        }))
      } catch (error) {
        if (!isCurrentConversation(scope, conversationId)) {
          return
        }
        set({
          uploadingAsset: false,
          error: error instanceof Error ? error.message : t("supportChat.sendAttachmentFailed"),
        })
        throw error
      }
    },

    closeConversation: async () => {
      const scope = captureCustomerSessionScope()
      const conversationId = get().conversation?.id
      if (!conversationId) {
        return
      }

      set({ error: "", closingConversation: true })
      try {
        await closeImConversation(conversationId)
        if (!isCurrentConversation(scope, conversationId)) {
          return
        }
        closeSocket({ reconnect: false })
        set((state) => ({
          closingConversation: false,
          status: "disconnected",
          conversation: state.conversation
            ? {
                ...state.conversation,
                status: 2,
              }
            : null,
        }))
      } catch (error) {
        if (!isCurrentConversation(scope, conversationId)) {
          return
        }
        set({
          closingConversation: false,
          error: error instanceof Error ? error.message : t("supportChat.closeConversationFailed"),
        })
        throw error
      }
    },

    retry: async () => {
      const scope = captureCustomerSessionScope()
      const conversationId = get().conversation?.id
      if (!conversationId) {
        return
      }

      set({ error: "", status: "connecting" })
      try {
        await get().refreshMessages()
        if (!isCurrentConversation(scope, conversationId)) {
          return
        }
        if (get().isOpen) {
          connectSocket()
        }
      } catch (error) {
        if (!isCurrentConversation(scope, conversationId)) {
          return
        }
        set({
          status: "disconnected",
          error: error instanceof Error ? error.message : t("supportChat.refreshFailed"),
        })
      }
    },
  }
})
