// This file has been modified for the KestrelDesk project.
export type KestrelDeskConfig = {
  channelId: string
  baseUrl?: string
  apiBaseUrl?: string
  widgetBaseUrl?: string
  /** Stable external visitor ID. Uses the browser-local visitor ID when omitted. */
  externalId?: string
  /** Visitor display name, only used when first exchanging for a chat token. */
  externalName?: string
  /** Gets the user JWT issued by the host system before opening support. */
  getUserToken?: () => string | Promise<string>
  title?: string
  subtitle?: string
  language?: string
  position?: "left" | "right"
  themeColor?: string
  width?: string
}

export type SupportChatRuntimeConfig = Omit<KestrelDeskConfig, "getUserToken"> & {
  /** Used only by /support/chat to exchange for a chat token; not part of KestrelDeskConfig. */
  userToken?: string
}

export type KestrelDeskWidget = {
  mount: (config?: KestrelDeskConfig) => void
  destroy: () => void
  open: () => Promise<void>
  close: () => void
  getChatUrl: () => Promise<string>
}

declare global {
  interface Window {
    KestrelDeskConfig?: KestrelDeskConfig
    KestrelDeskWidget?: KestrelDeskWidget
    __CS_AI_AGENT_WIDGET_CONFIG__?: SupportChatRuntimeConfig
    __CS_AI_AGENT_WIDGET_STATE__?: unknown
  }
}
