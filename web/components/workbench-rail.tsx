"use client"

import { MessagesSquareIcon, TicketCheckIcon } from "lucide-react"
import Link from "next/link"
import { usePathname } from "next/navigation"

import { useI18n } from "@/i18n/provider"
import { cn } from "@/lib/utils"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { WorkspaceSwitcher } from "@/components/workspace-switcher"

const workbenchRailItems = [
  {
    key: "conversations",
    titleKey: "nav.conversations",
    href: "/workbench",
    icon: MessagesSquareIcon,
  },
  {
    key: "tickets",
    titleKey: "nav.tickets",
    href: "/workbench/tickets",
    icon: TicketCheckIcon,
  },
]

function normalizePath(path: string | null | undefined) {
  if (!path) {
    return ""
  }
  return path.length > 1 ? path.replace(/\/+$/, "") : path
}

export function WorkbenchRail() {
  const t = useI18n()
  const pathname = usePathname()
  const currentPath = normalizePath(pathname)

  return (
    <aside className="flex h-svh w-16 shrink-0 flex-col items-center border-r border-border/70 bg-sidebar px-2 py-3 text-sidebar-foreground">
      <div className="mb-4 flex w-full justify-center">
        <WorkspaceSwitcher currentWorkspace="workbench" variant="rail" />
      </div>
      <nav className="flex w-full flex-col items-center gap-2">
        {workbenchRailItems.map((item) => {
          const Icon = item.icon
          const itemPath = normalizePath(item.href)
          const isActive =
            item.key === "conversations"
              ? currentPath === "/workbench"
              : currentPath === itemPath || currentPath.startsWith(`${itemPath}/`)
          const title = t(item.titleKey)

          return (
            <Tooltip key={item.key}>
              <TooltipTrigger
                render={
                  <Link
                    href={item.href}
                    aria-label={title}
                    aria-current={isActive ? "page" : undefined}
                    className={cn(
                      "flex size-11 items-center justify-center rounded-lg bg-transparent text-sidebar-foreground/65 transition-colors hover:text-sidebar-foreground",
                      isActive &&
                        "bg-sidebar-primary text-sidebar-primary-foreground hover:bg-sidebar-primary hover:text-sidebar-primary-foreground"
                    )}
                  />
                }
              >
                <Icon className="size-4" />
                <span className="sr-only">{title}</span>
              </TooltipTrigger>
              <TooltipContent side="right" align="center">
                {title}
              </TooltipContent>
            </Tooltip>
          )
        })}
      </nav>
    </aside>
  )
}
