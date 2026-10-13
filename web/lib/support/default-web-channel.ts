import type { AdminChannel } from "@/lib/api/admin"

export type DefaultWebChannelOption = Pick<AdminChannel, "channelId" | "channelType" | "name" | "status">

export function getEligibleDefaultWebChannels(channels: DefaultWebChannelOption[]) {
  return channels.filter((channel) =>
    channel.channelType === "web" &&
    channel.status === 0 &&
    Boolean(channel.channelId?.trim())
  )
}
