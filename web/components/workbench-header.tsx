"use client"

import { BellIcon, KeyRoundIcon, LogOutIcon, UserIcon } from "lucide-react"
import { useRouter } from "next/navigation"
import { useState } from "react"

import { ChangePasswordDialog } from "@/components/change-password-dialog"
import { PaletteToggle } from "@/components/palette-toggle"
import { RealtimeConnectionStatus } from "@/components/realtime-connection-status"
import { ThemeToggle } from "@/components/theme-toggle"
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useAuth } from "@/components/auth-provider"
import { useNotifications } from "@/components/notification-provider"
import { useI18n } from "@/i18n/provider"
import { useAgentConversationsStore } from "@/lib/stores/agent-conversations"

export function WorkbenchHeader() {
  const realtimeStatus = useAgentConversationsStore((state) => state.realtimeStatus)

  return (
    <header className="flex h-(--header-height) shrink-0 items-center border-b border-border/70 bg-background/88 backdrop-blur supports-[backdrop-filter]:bg-background/76">
      <div className="flex w-full min-w-0 items-center justify-end gap-3 px-3 lg:px-4">
        <div className="flex min-w-0 items-center justify-end gap-2">
          <div className="hidden sm:block">
            <RealtimeConnectionStatus status={realtimeStatus} compact />
          </div>
          <PaletteToggle />
          <ThemeToggle />
          <WorkbenchUserMenu />
        </div>
      </div>
    </header>
  )
}

function WorkbenchUserMenu() {
  const t = useI18n()
  const router = useRouter()
  const { session, signOut } = useAuth()
  const { unreadCount } = useNotifications()
  const [changePasswordOpen, setChangePasswordOpen] = useState(false)
  const user = {
    name: session?.user.nickname || session?.user.username || t("common.notSignedIn"),
    email: session?.user.username || t("common.guest"),
    avatar: session?.user.avatar || "",
  }
  const fallback = user.name.slice(0, 1).toUpperCase() || "U"

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              variant="ghost"
              size="icon"
              className="relative size-9 rounded-full"
              aria-label={user.name}
            />
          }
        >
          <Avatar className="size-8">
            <AvatarImage src={user.avatar} alt={user.name} />
            <AvatarFallback>
              {fallback || <UserIcon className="size-4" />}
            </AvatarFallback>
          </Avatar>
        </DropdownMenuTrigger>
        <DropdownMenuContent className="min-w-56" align="end" sideOffset={8}>
          <DropdownMenuGroup>
            <DropdownMenuLabel className="p-0 font-normal">
              <div className="flex items-center gap-2 px-1 py-1.5 text-left text-sm">
                <Avatar className="size-8">
                  <AvatarImage src={user.avatar} alt={user.name} />
                  <AvatarFallback>{fallback}</AvatarFallback>
                </Avatar>
                <div className="grid flex-1 text-left text-sm leading-tight">
                  <span className="truncate font-medium">{user.name}</span>
                  <span className="truncate text-xs text-muted-foreground">
                    {user.email}
                  </span>
                </div>
              </div>
            </DropdownMenuLabel>
          </DropdownMenuGroup>
          <DropdownMenuSeparator />
          <DropdownMenuGroup>
            <DropdownMenuItem
              onClick={() => {
                router.push("/dashboard/notifications")
              }}
              className="gap-2"
            >
              <BellIcon />
              <span className="flex-1">{t("nav.notifications")}</span>
              {unreadCount > 0 ? (
                <Badge className="h-5 min-w-5 px-1.5">
                  {unreadCount > 99 ? "99+" : unreadCount}
                </Badge>
              ) : null}
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={() => {
                setChangePasswordOpen(true)
              }}
            >
              <KeyRoundIcon />
              {t("nav.changePassword")}
            </DropdownMenuItem>
          </DropdownMenuGroup>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            onClick={() => {
              void signOut()
            }}
          >
            <LogOutIcon />
            {t("nav.signOut")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <ChangePasswordDialog
        open={changePasswordOpen}
        onOpenChange={setChangePasswordOpen}
        onSuccess={signOut}
      />
    </>
  )
}
