export function getSupportConsultationHref(defaultWebChannelId?: string): string | null {
  const channelId = defaultWebChannelId?.trim()
  return channelId ? `/support/chat?channelId=${encodeURIComponent(channelId)}` : null
}
