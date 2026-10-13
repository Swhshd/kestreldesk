// This file has been modified for the KestrelDesk project.

import { request } from "@/lib/api/client"
import { translateCurrentMessage } from "@/i18n/messages"
import { readSupportChatRuntimeConfig } from "@/lib/sdk/runtime-config"
import { generateUUID } from "@/lib/utils"
import {
  activateCustomerSessionScope,
  beginCustomerSessionAttempt,
  captureCustomerSessionScope,
  invalidateCustomerSessionScope,
  isCustomerSessionAttemptCurrent,
  isCustomerSessionScopeCurrent,
  type CustomerSessionAttempt,
  type CustomerSessionScope,
} from "@/lib/api/im-session-scope"

export type Paging = {
  page: number
  limit: number
  total: number
}

export type PageResult<T> = {
  results?: T[] | null
  page: Paging
  cursor?: string
  hasMore?: boolean
}

export type ImConversationTag = {
  id: number
  name: string
  color: string
}

export type ImConversationParticipant = {
  id: number
  participantType: string
  participantId: number
  externalParticipantId?: string
  joinedAt?: string
  leftAt?: string
  status: number
}

export type ImConversation = {
  id: number
  channelId: number
  customerName: string
  status: number
  serviceMode: number
  priority: number
  currentAssigneeId: number
  currentAssigneeName?: string
  currentTeamId?: number
  lastMessageId: number
  lastMessageAt?: string
  lastActiveAt?: string
  lastMessageSummary?: string
  customerUnreadCount: number
  agentUnreadCount: number
  customerLastReadMessageId: number
  customerLastReadAt?: string
  agentLastReadMessageId: number
  agentLastReadAt?: string
  closedAt?: string
  tags?: ImConversationTag[]
  participants?: ImConversationParticipant[]
}

export type ImConversationDetail = ImConversation

export type ImMessage = {
  id: number
  conversationId: number
  workflowRunId?: number
  clientMsgId?: string
  senderType: string
  senderId: number
  senderName?: string
  senderAvatar?: string
  messageType: string
  content: string
  payload?: string
  sendStatus: number
  sentAt?: string
  deliveredAt?: string
  readAt?: string
  customerRead: boolean
  customerReadAt?: string
  agentRead: boolean
  agentReadAt?: string
  recalledAt?: string
  quotedMessageId?: number
}

export type ImAsset = {
  id: number
  assetId: string
  provider: string
  storageKey: string
  filename: string
  fileSize: number
  mimeType: string
  status: number
  url: string
  createdAt: string
  updatedAt: string
  createUserId: number
  createUserName: string
  updateUserId: number
  updateUserName: string
}

export type ImWidgetConfig = {
  channelId?: string
  channelType?: string
  userToken?: string
  title?: string
  subtitle?: string
  themeColor?: string
  position?: "left" | "right"
  width?: string
}

export type ImCustomerSessionCustomer = {
  id: number
  name: string
}

export type ImCustomerSessionExchangeResponse = {
  customerSessionToken: string
  expiresAt: string
  identityKey: string
  customer: ImCustomerSessionCustomer
}

export type ImCustomerSession = ImCustomerSessionExchangeResponse & {
  channelId: string
}

const GUEST_STORAGE_KEY = "cs_ai_agent_im_guest_id"
const CUSTOMER_SESSION_STORAGE_KEY = "cs_ai_agent_customer_session"
const CUSTOMER_SESSION_TOKEN_HEADER = "X-Customer-Session-Token"
const CUSTOMER_SESSION_EXPIRES_HEADER = "X-Customer-Session-Expires-At"
const API_BASE_URL =
  process.env.NEXT_PUBLIC_API_BASE_URL?.trim() || ""
const OPEN_IM_CHANNEL_ID =
  process.env.NEXT_PUBLIC_OPEN_IM_CHANNEL_ID?.trim() || ""

let entryUserTokenExchangeKey = ""

function buildGuestId() {
  return `guest_${generateUUID()}`
}

export function getGuestId() {
  if (typeof window === "undefined") {
    return ""
  }
  const existing = window.localStorage.getItem(GUEST_STORAGE_KEY)?.trim()
  if (existing) {
    return existing
  }
  const guestId = buildGuestId()
  window.localStorage.setItem(GUEST_STORAGE_KEY, guestId)
  return guestId
}

function getRuntimeImConfig() {
  const widgetConfig = readSupportChatRuntimeConfig()
  const baseUrl = (widgetConfig.apiBaseUrl || widgetConfig.baseUrl || API_BASE_URL)
    .trim()
    .replace(/\/$/, "")
  return {
    baseUrl,
    channelId: widgetConfig.channelId || OPEN_IM_CHANNEL_ID,
    externalId: (widgetConfig.externalId || "").trim(),
    externalName: (widgetConfig.externalName || "").trim(),
    userToken: (widgetConfig.userToken || "").trim(),
  }
}

function parseExpiresAt(value: string) {
  if (typeof value !== "string") {
    return 0
  }
  const normalized = value.trim().replace(" ", "T")
  const timestamp = Date.parse(normalized)
  return Number.isFinite(timestamp) ? timestamp : 0
}

function hasUsableCustomerSession(session: ImCustomerSession | null): session is ImCustomerSession {
  return Boolean(
    session &&
    typeof session.customerSessionToken === "string" && session.customerSessionToken.trim() &&
    typeof session.expiresAt === "string" && parseExpiresAt(session.expiresAt) > 0 &&
    typeof session.identityKey === "string" && session.identityKey.trim() &&
    typeof session.channelId === "string" && session.channelId.trim() &&
    Number.isInteger(session.customer?.id) && session.customer.id > 0 &&
    typeof session.customer.name === "string"
  )
}

function isCustomerSessionValid(
  session: ImCustomerSession | null,
  channelId?: string,
  identityKey?: string
): session is ImCustomerSession {
  if (!hasUsableCustomerSession(session)) {
    return false
  }
  if (channelId && session.channelId !== channelId) {
    return false
  }
  if (identityKey && session.identityKey !== identityKey) {
    return false
  }
  return parseExpiresAt(session.expiresAt) > Date.now() + 5000
}

export function readCustomerSession(): ImCustomerSession | null {
  if (typeof window === "undefined") {
    return null
  }
  const raw = window.sessionStorage.getItem(CUSTOMER_SESSION_STORAGE_KEY)
  if (!raw) {
    return null
  }
  try {
    return JSON.parse(raw) as ImCustomerSession
  } catch {
    window.sessionStorage.removeItem(CUSTOMER_SESSION_STORAGE_KEY)
    return null
  }
}

function writeCustomerSession(session: ImCustomerSession) {
  if (typeof window === "undefined") {
    return
  }
  window.sessionStorage.setItem(CUSTOMER_SESSION_STORAGE_KEY, JSON.stringify(session))
}

export function getCustomerSessionToken() {
  const config = getRuntimeImConfig()
  const session = readCustomerSession()
  return isCustomerSessionValid(session, config.channelId)
    ? session?.customerSessionToken ?? ""
    : ""
}

export function applyCustomerSessionRefresh(payload: {
  customerSessionToken?: string
  expiresAt?: string
} | undefined, scope: CustomerSessionScope | null): void {
  if (!isCustomerSessionScopeCurrent(scope)) {
    return
  }
  const token = payload?.customerSessionToken?.trim()
  const expiresAt = payload?.expiresAt?.trim()
  if (!token || !expiresAt) {
    return
  }
  const current = readCustomerSession()
  if (!current || current.customer.id !== scope?.customerId || current.channelId !== scope.channelId) {
    return
  }
  writeCustomerSession({
    ...current,
    customerSessionToken: token,
    expiresAt,
  })
}

function applyCustomerSessionHeaders(response: Response, scope: CustomerSessionScope | null): void {
  applyCustomerSessionRefresh({
    customerSessionToken: response.headers.get(CUSTOMER_SESSION_TOKEN_HEADER) ?? "",
    expiresAt: response.headers.get(CUSTOMER_SESSION_EXPIRES_HEADER) ?? "",
  }, scope)
}

function createChannelHeaders() {
  const config = getRuntimeImConfig()
  return {
    "X-Channel-Id": config.channelId,
  }
}

type RuntimeImConfig = ReturnType<typeof getRuntimeImConfig>

function guestIdentityKey(config: RuntimeImConfig) {
  return `guest:${config.externalId || getGuestId()}`
}

function customerSessionRequestKey(config: RuntimeImConfig, createGuestId = true) {
  const externalId = config.userToken ? "" : config.externalId || (createGuestId
    ? getGuestId()
    : typeof window === "undefined" ? "" : window.localStorage.getItem(GUEST_STORAGE_KEY)?.trim() || "")
  return JSON.stringify(config.userToken
    ? [config.channelId, "user", config.userToken]
    : [config.channelId, "guest", externalId])
}

export function beginCustomerSessionBootstrap(): CustomerSessionAttempt {
  return beginCustomerSessionAttempt(customerSessionRequestKey(getRuntimeImConfig()))
}

function canReuseSession(config: RuntimeImConfig, cached: ImCustomerSession | null) {
  if (config.userToken) {
    return entryUserTokenExchangeKey === `${config.channelId}:${config.userToken}` &&
      isCustomerSessionValid(cached, config.channelId)
  }
  return isCustomerSessionValid(cached, config.channelId, guestIdentityKey(config))
}

export function canReuseCustomerSession(): boolean {
  return canReuseSession(getRuntimeImConfig(), readCustomerSession())
}

export function isCustomerSessionContinuityLost(): boolean {
  const config = getRuntimeImConfig()
  const cached = readCustomerSession()
  if (!hasUsableCustomerSession(cached) || parseExpiresAt(cached.expiresAt) <= Date.now()) {
    return true
  }
  if (cached.channelId !== config.channelId) {
    return true
  }
  // A changed host token requires exchange, but does not itself identify a new customer.
  return config.userToken
    ? !cached.identityKey.startsWith("user:")
    : cached.identityKey !== guestIdentityKey(config)
}

export function departCustomerSessionIdentity(): void {
  if (captureCustomerSessionScope()) {
    invalidateCustomerSessionScope()
  }
  if (typeof window !== "undefined") {
    window.sessionStorage.removeItem(CUSTOMER_SESSION_STORAGE_KEY)
  }
  entryUserTokenExchangeKey = ""
}

function assertCustomerSessionAttempt(attempt: CustomerSessionAttempt, config?: RuntimeImConfig) {
  if (!isCustomerSessionAttemptCurrent(attempt) ||
    (config && attempt.requestKey !== customerSessionRequestKey(config, false))) {
    throw new Error(translateCurrentMessage("api.customerSessionNotReady"))
  }
}

function createExchangeHeaders(config: RuntimeImConfig) {
  const headers: Record<string, string> = {
    "X-Channel-Id": config.channelId,
  }
  if (config.userToken) {
    headers.Authorization = `Bearer ${config.userToken}`
  } else {
    headers["X-External-Id"] = config.externalId || getGuestId()
    if (config.externalName) {
      headers["X-External-Name"] = encodeURIComponent(config.externalName)
    }
    const cached = readCustomerSession()
    // Direct cache reuse has a five-second margin; exchange proof uses actual expiry.
    if (hasUsableCustomerSession(cached) &&
      cached.channelId === config.channelId &&
      cached.identityKey === guestIdentityKey(config) &&
      parseExpiresAt(cached.expiresAt) > Date.now()) {
      headers[CUSTOMER_SESSION_TOKEN_HEADER] = cached.customerSessionToken
    }
  }
  return {
    ...headers,
  }
}

function createImHeaders() {
  const sessionToken = getCustomerSessionToken()
  if (!sessionToken) {
    throw new Error(translateCurrentMessage("api.customerSessionNotReady"))
  }
  return {
    ...createChannelHeaders(),
    Authorization: `Bearer ${sessionToken}`,
  }
}

function createRequestOptions(
  init?: RequestInit
): RequestInit & {
  baseUrl?: string
  skipAuth?: boolean
  onResponse?: (response: Response) => void
} {
  const scope = captureCustomerSessionScope()
  return {
    ...init,
    skipAuth: true,
    headers: {
      ...createImHeaders(),
      ...(init?.headers as Record<string, string> | undefined),
    },
    onResponse: (response) => applyCustomerSessionHeaders(response, scope),
    baseUrl: getRuntimeImConfig().baseUrl,
  }
}

function toQueryString(query?: Record<string, string | number | undefined>) {
  if (!query) {
    return ""
  }

  const params = new URLSearchParams()
  Object.entries(query).forEach(([key, value]) => {
    if (value === undefined || value === "") {
      return
    }
    params.set(key, String(value))
  })
  const output = params.toString()
  return output ? `?${output}` : ""
}

export async function exchangeCustomerSession(attempt?: CustomerSessionAttempt): Promise<ImCustomerSession> {
  // Reject stale supplied tickets before reading bindings or mutating any storage/scope.
  const ticket = attempt ?? beginCustomerSessionBootstrap()
  assertCustomerSessionAttempt(ticket)
  const config = getRuntimeImConfig()
  assertCustomerSessionAttempt(ticket, config)
  if (isCustomerSessionContinuityLost()) {
    departCustomerSessionIdentity()
  }
  const result = await request<ImCustomerSessionExchangeResponse>(
    "/api/customer/session_exchange",
    {
      method: "POST",
      skipAuth: true,
      baseUrl: config.baseUrl,
      headers: createExchangeHeaders(config),
    }
  )
  const currentConfig = getRuntimeImConfig()
  assertCustomerSessionAttempt(ticket, currentConfig)
  if (currentConfig.baseUrl !== config.baseUrl) {
    throw new Error(translateCurrentMessage("api.customerSessionNotReady"))
  }
  const session = {
    ...result,
    channelId: config.channelId,
  }
  if (!hasUsableCustomerSession(session) || parseExpiresAt(session.expiresAt) <= Date.now() ||
    (config.userToken
      ? !session.identityKey.startsWith("user:")
      : session.identityKey !== guestIdentityKey(config))) {
    throw new Error(translateCurrentMessage("api.customerSessionNotReady"))
  }
  writeCustomerSession(session)
  if (config.userToken) {
    entryUserTokenExchangeKey = `${config.channelId}:${config.userToken}`
  }
  activateCustomerSessionScope(session)
  return session
}

export async function ensureCustomerSession(attempt?: CustomerSessionAttempt): Promise<ImCustomerSession> {
  const ticket = attempt ?? beginCustomerSessionBootstrap()
  assertCustomerSessionAttempt(ticket)
  const config = getRuntimeImConfig()
  assertCustomerSessionAttempt(ticket, config)
  if (isCustomerSessionContinuityLost()) {
    departCustomerSessionIdentity()
  }
  const cached = readCustomerSession()
  if (cached && canReuseSession(config, cached)) {
    activateCustomerSessionScope(cached)
    return cached
  }
  return exchangeCustomerSession(ticket)
}

export function fetchImConversationDetail(id: number) {
  return request<ImConversationDetail>(`/api/conversation/${id}`, {
    ...createRequestOptions(),
  })
}

export function fetchImMessages(
  query?: Record<string, string | number | undefined>
) {
  return request<PageResult<ImMessage>>(
    `/api/message/list${toQueryString(query)}`,
    createRequestOptions()
  )
}

export function createOrMatchImConversation() {
  return request<ImConversation>("/api/conversation/create_or_match", {
    ...createRequestOptions({ method: "POST" }),
  })
}

export function fetchImWidgetConfig() {
  return request<ImWidgetConfig>(
    `/api/channel/config${toQueryString({
      channelId: getRuntimeImConfig().channelId,
    })}`,
    {
      skipAuth: true,
      baseUrl: getRuntimeImConfig().baseUrl,
      headers: createChannelHeaders(),
    }
  )
}

export function closeImConversation(conversationId: number) {
  return request<void>("/api/conversation/close", {
    ...createRequestOptions({
      method: "POST",
      body: JSON.stringify({ conversationId }),
    }),
  })
}

export function sendImMessage(payload: {
  conversationId: number
  messageType: string
  content: string
  payload?: string
  clientMsgId?: string
}) {
  return request<ImMessage>("/api/message/send", {
    ...createRequestOptions({
      method: "POST",
      body: JSON.stringify(payload),
    }),
  })
}

export function markImMessageRead(conversationId: number, messageId = 0) {
  return request<void>("/api/message/read", {
    ...createRequestOptions({
      method: "POST",
      body: JSON.stringify({ conversationId, messageId }),
    }),
  })
}

export function uploadImImage(conversationId: number, file: File) {
  const formData = new FormData()
  formData.set("conversationId", String(conversationId))
  formData.set("file", file)
  return request<ImAsset>("/api/message/upload_image", {
    ...createRequestOptions({
      method: "POST",
      body: formData,
    }),
  })
}

export function uploadImAttachment(conversationId: number, file: File) {
  const formData = new FormData()
  formData.set("conversationId", String(conversationId))
  formData.set("file", file)
  return request<ImAsset>("/api/message/upload_attachment", {
    ...createRequestOptions({
      method: "POST",
      body: formData,
    }),
  })
}
