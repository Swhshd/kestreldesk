"use client"

// This file has been modified for the KestrelDesk project.

import { useEffect, useRef } from "react"
import { toast } from "sonner"
import { useAuth } from "@/components/auth-provider"

import { createAdminWebSocketUrl } from "@/lib/api/admin"
import {
  handleAgentMessageCreated,
  isFullAgentMessageRecalledPayload,
  shouldReloadConversationListForRealtimePatch,
  type AgentMessageCreatedData,
  type AgentMessageRecalledData,
} from "@/lib/agent-conversation-realtime"
import { readSession } from "@/lib/auth"
import { IMConversationStatus } from "@/lib/generated/enums"
import {
  normalizeRealtimeMessage,
  type RealtimeConversationPatch,
} from "@/lib/im-realtime-state"
import { createRealtimeConnectionManager } from "@/lib/realtime-connection"
import { showNotification } from "@/lib/services/notification"
import { useAgentConversationsStore } from "@/lib/stores/agent-conversations"
import { useI18n } from "@/i18n/provider"

type AgentRealtimeConnection = ReturnType<typeof createRealtimeConnectionManager>
type AgentRealtimeEnvelope = {
  eventId?: string
  type?: string
  data?: AgentMessageCreatedData & RealtimeConversationPatch & AgentMessageRecalledData
}

export function useAgentConversationRealtime() {
  const { validateRealtimeProfile } = useAuth()
  const t = useI18n()
  const selectedConversationId = useAgentConversationsStore(
    (state) => state.selectedConversationId
  )
  const setRealtimeStatus = useAgentConversationsStore(
    (state) => state.setRealtimeStatus
  )
  const realtimeRef = useRef<AgentRealtimeConnection | null>(null)
  const subscribedConversationIdRef = useRef<number | null>(null)
  const selectedConversationIdRef = useRef<number | null>(selectedConversationId)
  const currentUserIdRef = useRef<number>(readSession()?.user.id ?? 0)
  const validateRealtimeProfileRef = useRef(validateRealtimeProfile)

  useEffect(() => {
    selectedConversationIdRef.current = selectedConversationId
  }, [selectedConversationId])

  useEffect(() => {
    validateRealtimeProfileRef.current = validateRealtimeProfile
  }, [validateRealtimeProfile])

  useEffect(() => {
    const realtime = createRealtimeConnectionManager({
      createSocket: () => new WebSocket(createAdminWebSocketUrl()),
      beforeReconnect: () => validateRealtimeProfileRef.current(),
      onStatusChange: setRealtimeStatus,
      onOpen: (socket) => {
        console.info("[agent-realtime] websocket connected", {
          readyState: socket.readyState,
        })

        const conversationId = selectedConversationIdRef.current
        if (conversationId) {
          socket.send(
            JSON.stringify({
              type: "subscribe",
              topics: [`conversation:${conversationId}`],
            })
          )
          subscribedConversationIdRef.current = conversationId
        } else {
          subscribedConversationIdRef.current = null
        }
      },
      onMessage: (event, socket) => {
        try {
          const envelope = JSON.parse(event.data) as AgentRealtimeEnvelope
          const eventType = envelope.type ?? ""
          const payload = envelope.data
          const conversationId = payload?.conversationId ?? 0
          const eventId = envelope.eventId?.trim() ?? ""

          if (
            eventType === "" ||
            eventType === "connected" ||
            eventType === "pong" ||
            eventType === "subscribed" ||
            eventType === "unsubscribed"
          ) {
            return
          }

          if (eventId && socket.readyState === WebSocket.OPEN) {
            socket.send(JSON.stringify({ type: "ack", eventId }))
          }

          const store = useAgentConversationsStore.getState()
          if (eventType === "resyncRequired") {
            void store.resyncRealtimeData(conversationId).catch((error) => {
              toast.error(error instanceof Error ? error.message : t("conversation.syncConversationDataFailed"))
            })
            return
          }

          if (eventType === "message.created") {
            void handleAgentMessageCreated(payload, {
              currentUserId: currentUserIdRef.current,
              hidden: typeof document !== "undefined" && document.visibilityState !== "visible",
              activeConversationStatus: IMConversationStatus.Active,
            }, {
              normalize: normalizeRealtimeMessage,
              applyMessage: store.applyRealtimeMessageCreated,
              refresh: store.resyncRealtimeData,
              notify: (conversationId) => {
                showNotification(t("conversation.newMessage"), t("conversation.newMessage"), () => {
                  void store.selectConversation(conversationId)
                })
              },
            }).catch((error) => {
              toast.error(error instanceof Error ? error.message : t("conversation.syncMessagesFailed"))
            })
            return
          }

          if (eventType === "message.recalled" && payload?.messageId) {
            if (isFullAgentMessageRecalledPayload(payload)) {
              store.applyRealtimeMessageRecalled(payload.messageId, {
                sendStatus: payload.sendStatus,
                recalledAt: payload.recalledAt,
              })
            } else {
              void store.resyncRealtimeData(conversationId).catch((error) => {
                toast.error(error instanceof Error ? error.message : t("conversation.syncMessagesFailed"))
              })
            }
            return
          }

          if (eventType.startsWith("conversation.") && payload) {
            store.applyRealtimeConversationChanged(payload)
            if (shouldReloadConversationListForRealtimePatch(payload)) {
              void store.resyncRealtimeData(conversationId).catch((error) => {
                toast.error(error instanceof Error ? error.message : t("conversation.syncConversationListFailed"))
              })
            }
          }
        } catch {
          // ignore invalid ws payload
        }
      },
      onClose: (event, socket) => {
        console.log("[agent-realtime] websocket closed", {
          readyState: socket.readyState,
          code: event.code,
          reason: event.reason,
          wasClean: event.wasClean,
        })
        subscribedConversationIdRef.current = null
      },
      onError: (_event, socket) => {
        console.log("[agent-realtime] websocket error", {
          readyState: socket.readyState,
        })
      },
      onConnectError: (error) => {
        toast.error(error instanceof Error ? error.message : t("conversation.realtimeConnectFailed"))
      },
    })

    realtimeRef.current = realtime
    realtime.connect()

    return () => {
      realtimeRef.current = null
      realtime.disconnect()
      subscribedConversationIdRef.current = null
    }
  }, [setRealtimeStatus, t])

  useEffect(() => {
    const socket = realtimeRef.current?.getSocket()
    if (!socket || socket.readyState !== WebSocket.OPEN) {
      return
    }

    const previousConversationId = subscribedConversationIdRef.current
    const nextConversationId = selectedConversationId ?? null

    if (previousConversationId && previousConversationId !== nextConversationId) {
      socket.send(
        JSON.stringify({
          type: "unsubscribe",
          topics: [`conversation:${previousConversationId}`],
        })
      )
    }

    if (nextConversationId && nextConversationId !== previousConversationId) {
      socket.send(
        JSON.stringify({
          type: "subscribe",
          topics: [`conversation:${nextConversationId}`],
        })
      )
      subscribedConversationIdRef.current = nextConversationId
      return
    }

    if (!nextConversationId) {
      subscribedConversationIdRef.current = null
    }
  }, [selectedConversationId])
}
