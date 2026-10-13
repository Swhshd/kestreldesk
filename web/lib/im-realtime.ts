import { createWebSocketBaseUrl } from "@/lib/api/websocket"
import { getCustomerSessionToken, type ImMessage } from "@/lib/api/im"
import { readSupportChatRuntimeConfig } from "@/lib/sdk/runtime-config"
import type {
  RealtimeConversationPatch,
  RealtimeMessageCreatedPayload,
} from "@/lib/im-realtime-state"

export type ImRealtimeEnvelope = {
  type: string
  topic?: string
  data?: RealtimeMessageCreatedPayload<ImMessage> &
    RealtimeConversationPatch & {
      customerSessionToken?: string
      expiresAt?: string
    }
  payload?: RealtimeMessageCreatedPayload<ImMessage> &
    RealtimeConversationPatch & {
      customerSessionToken?: string
      expiresAt?: string
    }
}

export function createImRealtimeConnection() {
  const config = readSupportChatRuntimeConfig()
  const apiBaseUrl = (config.apiBaseUrl || "").trim()
  const baseUrl = apiBaseUrl
    ? apiBaseUrl.replace(/^http/, "ws").replace(/\/$/, "")
    : createWebSocketBaseUrl()
  const channelId = encodeURIComponent(config.channelId || "")
  const customerSessionToken = getCustomerSessionToken()
  return new WebSocket(
    `${baseUrl}/api/ws/open?channelId=${channelId}&customerSessionToken=${encodeURIComponent(customerSessionToken)}`
  )
}
