import type { ReactNode } from "react"

import { CustomerAvatar } from "@/components/chat/customer-avatar"
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"

type SupportAccountAvatarProps = {
  alt: string
  className?: string
  fallback: ReactNode
  src: string
  userType: string
}

export function SupportAccountAvatar({
  alt,
  className,
  fallback,
  src,
  userType,
}: SupportAccountAvatarProps) {
  if (userType === "user") {
    return <CustomerAvatar alt={alt} className={className} fallback={fallback} src={src} />
  }

  return (
    <Avatar className={className}>
      <AvatarImage src={src} alt={alt} />
      <AvatarFallback>{fallback}</AvatarFallback>
    </Avatar>
  )
}
