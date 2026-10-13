"use client"

import { useAgentConversationRealtime } from "@/hooks/use-agent-conversation-realtime"

export function AgentRealtimeProvider() {
  useAgentConversationRealtime()

  return null
}
