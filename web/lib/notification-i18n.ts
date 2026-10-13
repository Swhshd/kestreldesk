import { normalizeLocale } from "@/i18n/config"
import { translateMessage } from "@/i18n/messages"

type LocalizableNotification = {
  title: string
  content: string
  notificationType: string
}

const TICKET_ASSIGNED_TITLE = "\u5de5\u5355\u6307\u6d3e\u63d0\u9192"
const CONVERSATION_TRANSFERRED_TITLE = "\u4f1a\u8bdd\u8f6c\u63a5\u63d0\u9192"
const CONVERSATION_AUTO_ASSIGNED_TITLE = "\u4f1a\u8bdd\u81ea\u52a8\u5206\u914d\u63d0\u9192"
const CONVERSATION_ASSIGNED_TITLE = "\u4f1a\u8bdd\u5206\u914d\u63d0\u9192"

const TICKET_ASSIGNED_PATTERN = /^\u5de5\u5355 (.+) \u5df2\u6307\u6d3e\u7ed9\u4f60$/
const CONVERSATION_ASSIGNED_PATTERN = /^\u4f1a\u8bdd #([0-9]+) \u5df2\u5206\u914d\u7ed9\u4f60$/
const ASSIGNMENT_REASON_PREFIX = "\u6307\u6d3e\u539f\u56e0: "
const CONVERSATION_ASSIGNMENT_REASON_PREFIX = "\u5206\u914d\u539f\u56e0: "
const TRANSFER_REASON_PREFIX = "\u8f6c\u63a5\u539f\u56e0: "

export function localizeNotificationItem<T extends LocalizableNotification>(
  notification: T,
  locale: string
): T {
  const normalizedLocale = normalizeLocale(locale)
  if (normalizedLocale !== "en-US") {
    return notification
  }
  if (notification.notificationType === "ticket_assigned") {
    return {
      ...notification,
      title: localizeNotificationTitle(notification.title, normalizedLocale),
      content: localizeTicketAssignedContent(notification.content, normalizedLocale),
    }
  }
  if (notification.notificationType === "conversation_assigned") {
    return {
      ...notification,
      title: localizeNotificationTitle(notification.title, normalizedLocale),
      content: localizeConversationAssignedContent(notification.content, normalizedLocale),
    }
  }
  return notification
}

function localizeTicketAssignedContent(content: string, locale: ReturnType<typeof normalizeLocale>) {
  const lines = splitNotificationLines(content)
  if (lines.length === 0) {
    return content
  }
  const match = lines[0].match(TICKET_ASSIGNED_PATTERN)
  if (match?.[1]) {
    lines[0] = translateMessage(locale, "notification.ticketAssignedLine", {
      ticketNo: match[1],
    })
  }
  return lines
    .map((line) =>
      line.startsWith(ASSIGNMENT_REASON_PREFIX)
        ? translateMessage(locale, "notification.assignmentReason", {
            reason: line.slice(ASSIGNMENT_REASON_PREFIX.length),
          })
        : line
    )
    .join("\n")
}

function localizeConversationAssignedContent(content: string, locale: ReturnType<typeof normalizeLocale>) {
  const lines = splitNotificationLines(content)
  if (lines.length === 0) {
    return content
  }
  const match = lines[0].match(CONVERSATION_ASSIGNED_PATTERN)
  if (match?.[1]) {
    lines[0] = translateMessage(locale, "notification.conversationAssignedLine", {
      conversationId: match[1],
    })
  }
  return lines
    .map((line) => {
      if (line.startsWith(CONVERSATION_ASSIGNMENT_REASON_PREFIX)) {
        return translateMessage(locale, "notification.assignmentReason", {
          reason: line.slice(CONVERSATION_ASSIGNMENT_REASON_PREFIX.length),
        })
      }
      if (line.startsWith(TRANSFER_REASON_PREFIX)) {
        return translateMessage(locale, "notification.transferReason", {
          reason: line.slice(TRANSFER_REASON_PREFIX.length),
        })
      }
      return line
    })
    .join("\n")
}

function localizeNotificationTitle(title: string, locale: ReturnType<typeof normalizeLocale>) {
  switch (title.trim()) {
    case TICKET_ASSIGNED_TITLE:
      return translateMessage(locale, "notification.ticketAssignedTitle")
    case CONVERSATION_TRANSFERRED_TITLE:
      return translateMessage(locale, "notification.conversationTransferredTitle")
    case CONVERSATION_AUTO_ASSIGNED_TITLE:
      return translateMessage(locale, "notification.conversationAutoAssignedTitle")
    case CONVERSATION_ASSIGNED_TITLE:
      return translateMessage(locale, "notification.conversationAssignedTitle")
    default:
      return title
  }
}

function splitNotificationLines(content: string) {
  const normalized = content.trim()
  if (!normalized) {
    return []
  }
  return normalized.split("\n")
}
