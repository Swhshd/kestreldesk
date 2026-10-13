export type AgentConversationUxMessage = {
  senderType: string
  senderId: number
}

export function canSelfClaimAgentConversation(
  roles: readonly string[],
  permissions: readonly string[],
  isPendingConversation: boolean,
  currentAssigneeId: number,
  isAIOnlyConversation: boolean
) {
  return (
    roles.includes("cs_user") &&
    permissions.includes("conversation.claim") &&
    isPendingConversation &&
    currentAssigneeId === 0 &&
    !isAIOnlyConversation
  )
}

export function canRecallAgentMessage(
  permissions: readonly string[],
  message: AgentConversationUxMessage,
  currentUserId: number
) {
  return (
    permissions.includes("conversation.recall") &&
    currentUserId > 0 &&
    message.senderType === "agent" &&
    message.senderId === currentUserId
  )
}
