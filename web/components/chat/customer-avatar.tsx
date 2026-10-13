import type { ReactNode } from "react"

import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { cn } from "@/lib/utils"

type CustomerAvatarProps = {
  alt?: string
  className?: string
  fallback: ReactNode
  src?: string
}

export function CustomerAvatar({ alt = "", className, fallback, src }: CustomerAvatarProps) {
  return (
    <Avatar data-avatar-role="customer" className={cn("customer-identity-avatar", className)}>
      {src?.trim() ? <AvatarImage src={src} alt={alt} /> : null}
      <AvatarFallback className="customer-identity-avatar-fallback">{fallback}</AvatarFallback>
    </Avatar>
  )
}
