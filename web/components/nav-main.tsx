"use client"

import { ChevronRightIcon } from "lucide-react"
import Link from "next/link"
import { usePathname } from "next/navigation"
import { useState } from "react"

import {
  getDashboardNavSectionStorageKey,
  isDashboardNavItemActive,
  parseDashboardNavSectionOpenState,
} from "@/lib/navigation-active"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  SidebarGroup,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  useSidebar,
} from "@/components/ui/sidebar"

export function NavMain({
  icon,
  sectionKey,
  title,
  items,
}: {
  icon?: React.ReactNode
  sectionKey: string
  title: string
  items: ReadonlyArray<{
    title: string
    url: string
    icon?: React.ReactNode
  }>
}) {
  const pathname = usePathname()
  const { isMobile, state } = useSidebar()
  const storageKey = getDashboardNavSectionStorageKey(sectionKey)
  const [open, setOpen] = useState(() => {
    if (typeof window === "undefined") {
      return true
    }
    return parseDashboardNavSectionOpenState(window.localStorage.getItem(storageKey)) ?? true
  })

  const handleOpenChange = (nextOpen: boolean) => {
    setOpen(nextOpen)
    window.localStorage.setItem(storageKey, String(nextOpen))
  }

  if (state === "collapsed" && !isMobile) {
    return (
      <SidebarGroup className="px-2 py-0 first:pt-2 last:pb-2">
        <SidebarMenu>
          <SidebarMenuItem>
            <DropdownMenu>
              <DropdownMenuTrigger
                openOnHover
                render={<SidebarMenuButton />}
              >
                {icon}
                <span title={title}>{title}</span>
              </DropdownMenuTrigger>
              <DropdownMenuContent
                side="right"
                align="start"
                sideOffset={8}
                className="w-56"
              >
                <DropdownMenuGroup>
                  <DropdownMenuLabel className="truncate" title={title}>
                    {title}
                  </DropdownMenuLabel>
                  {items.map((item) => (
                    <DropdownMenuItem
                      key={item.title}
                      render={<Link href={item.url} />}
                      className="cursor-pointer"
                    >
                      <span className="truncate" title={item.title}>
                        {item.title}
                      </span>
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuGroup>
              </DropdownMenuContent>
            </DropdownMenu>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarGroup>
    )
  }

  return (
    <SidebarGroup className="px-2 py-0 first:pt-2 last:pb-2">
      <SidebarMenu>
        <Collapsible
          open={open}
          onOpenChange={handleOpenChange}
          className="group/collapsible"
          render={<SidebarMenuItem />}
        >
          <CollapsibleTrigger render={<SidebarMenuButton tooltip={title} />}>
            {icon}
            <span title={title}>{title}</span>
            <ChevronRightIcon className="ml-auto transition-transform duration-200 group-data-open/collapsible:rotate-90" />
          </CollapsibleTrigger>
          <CollapsibleContent>
            <SidebarMenuSub>
              {items.map((item) => (
                <SidebarMenuSubItem key={item.title}>
                  <SidebarMenuSubButton
                    render={<Link href={item.url} />}
                    isActive={isDashboardNavItemActive(pathname, item.url)}
                    tooltip={item.title}
                  >
                    <span title={item.title}>{item.title}</span>
                  </SidebarMenuSubButton>
                </SidebarMenuSubItem>
              ))}
            </SidebarMenuSub>
          </CollapsibleContent>
        </Collapsible>
      </SidebarMenu>
    </SidebarGroup>
  )
}
