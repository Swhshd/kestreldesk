import MarkdownIt from "markdown-it"

import { translateCurrentMessage } from "@/i18n/messages"

export type MessageAssetPayload = {
  assetId: string
  filename?: string
  fileSize?: number
  mimeType?: string
  url?: string
}

const messageMarkdown = new MarkdownIt({
  html: false,
  linkify: true,
  breaks: true,
})

function t(key: string) {
  return translateCurrentMessage(key)
}

export function parseMessageAssetPayload(payload?: string): MessageAssetPayload | null {
  if (!payload?.trim()) {
    return null
  }
  try {
    const parsed = JSON.parse(payload) as MessageAssetPayload
    if (!parsed?.assetId?.trim()) {
      return null
    }
    return parsed
  } catch {
    return null
  }
}

export function renderIMMessageHTML(message: {
  messageType: string
  content: string
  payload?: string
}) {
  if (message.messageType === "html") {
    return message.content
  }

  const asset = parseMessageAssetPayload(message.payload)
  if (message.messageType === "image") {
    if (asset?.url) {
      return `<p><img src="${escapeHTMLAttr(asset.url)}" alt="${escapeHTMLAttr(
        asset.filename || "image"
      )}"></p>`
    }
    return `<p>${escapeHTML(t("supportChat.imageSummary"))}</p>`
  }

  if (message.messageType === "attachment") {
    if (asset?.url) {
      const title = escapeHTML(asset.filename || message.content || t("supportChat.attachmentSummary"))
      const meta = formatFileSize(asset.fileSize ?? 0)
      const metaHTML = meta ? `<div class="im-attachment-meta">${escapeHTML(meta)}</div>` : ""
      return `<div class="im-attachment"><a href="${escapeHTMLAttr(
        asset.url
      )}" target="_blank" rel="noreferrer" download="${escapeHTMLAttr(
        asset.filename || ""
      )}" class="im-attachment-link"><span class="im-attachment-icon" aria-hidden="true">${getAttachmentIconSVG()}</span><span class="im-attachment-content"><span class="im-attachment-title">${title}</span>${metaHTML}</span></a></div>`
    }
    return `<p>${escapeHTML(message.content || t("supportChat.attachmentSummary"))}</p>`
  }

  return renderTextMessageHTML(message.content || "")
}

export function summarizeIMMessage(message: {
  messageType: string
  content: string
  payload?: string
}) {
  if (message.messageType === "image") {
    return t("supportChat.imageSummary")
  }
  if (message.messageType === "attachment") {
    const asset = parseMessageAssetPayload(message.payload)
    return asset?.filename?.trim()
      ? `${t("supportChat.attachmentSummary")} ${asset.filename.trim()}`
      : t("supportChat.attachmentSummary")
  }
  if (message.messageType === "html") {
    const text = extractTextFromHTML(message.content)
    if (text.trim()) {
      return text.substring(0, 100)
    }
    if (message.content.includes("<img")) {
      return t("supportChat.imageSummary")
    }
    return t("supportChat.messageSummary")
  }
  return message.content?.substring(0, 100) || t("supportChat.messageSummary")
}

export function formatFileSize(size: number) {
  if (!Number.isFinite(size) || size <= 0) {
    return ""
  }
  const units = ["B", "KB", "MB", "GB"]
  let value = size
  let index = 0
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024
    index += 1
  }
  const digits = value >= 10 || index === 0 ? 0 : 1
  return `${value.toFixed(digits)} ${units[index]}`
}

function extractTextFromHTML(html: string): string {
  if (typeof document === "undefined") {
    return ""
  }
  const div = document.createElement("div")
  div.innerHTML = html
  return div.textContent || div.innerText || ""
}

function renderTextMessageHTML(content: string) {
  const value = content.trim()
  if (!value) {
    return "<p></p>"
  }
  return messageMarkdown.render(value)
}

function escapeHTML(value: string) {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;")
    .replaceAll("\n", "<br>")
}

function escapeHTMLAttr(value: string) {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll('"', "&quot;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
}

function getAttachmentIconSVG() {
  return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"></path><path d="M14 2v6h6"></path><path d="M9 15h6"></path><path d="M9 11h2"></path></svg>`
}
