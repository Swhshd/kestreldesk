"use client"

import type { ComponentProps, ReactNode } from "react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Switch } from "@/components/ui/switch"
import type {
  DashboardCrudColumn,
  DashboardCrudRowAction,
} from "./dashboard-crud-page"

type BadgeVariant = ComponentProps<typeof Badge>["variant"]

export type DashboardCrudStatusColumnOptions<TItem, TStatus> = {
  key?: string
  label: ReactNode
  className?: string
  getStatus: (item: TItem) => TStatus
  getLabel: (status: TStatus, item: TItem) => ReactNode
  getBadgeVariant?: (status: TStatus, item: TItem) => BadgeVariant
  isEnabled?: (status: TStatus, item: TItem) => boolean
  toggle?: {
    disabled?: (item: TItem) => boolean
    getNextStatus: (item: TItem) => TStatus
    updateStatus: (item: TItem, nextStatus: TStatus) => Promise<unknown>
    successMessage: (item: TItem, nextStatus: TStatus) => string
    errorMessage: string
    ariaLabel: (item: TItem) => string
  }
}

export type DashboardCrudStatusToggleActionOptions<TItem, TStatus> = {
  key?: string
  icon?: ReactNode | ((item: TItem) => ReactNode)
  label: (item: TItem, nextStatus: TStatus) => ReactNode
  visible?: (item: TItem) => boolean
  disabled?: (item: TItem) => boolean
  getNextStatus: (item: TItem) => TStatus
  updateStatus: (item: TItem, nextStatus: TStatus) => Promise<unknown>
  successMessage: (item: TItem, nextStatus: TStatus) => string
  errorMessage: string
}

export function createDashboardStatusColumn<TItem, TStatus>({
  key = "status",
  label,
  className,
  getStatus,
  getLabel,
  getBadgeVariant,
  isEnabled,
  toggle,
}: DashboardCrudStatusColumnOptions<TItem, TStatus>): DashboardCrudColumn<TItem> {
  return {
    key,
    label,
    className,
    render: (item, { itemId, actionLoading, reload, setActionLoadingId }) => {
      const status = getStatus(item)
      const badge = (
        <Badge variant={getBadgeVariant?.(status, item) ?? "outline"}>
          {getLabel(status, item)}
        </Badge>
      )

      if (!toggle) {
        return badge
      }

      return (
        <div className="flex items-center gap-3">
          <Switch
            checked={isEnabled ? isEnabled(status, item) : Boolean(status)}
            disabled={actionLoading || toggle.disabled?.(item)}
            onCheckedChange={() => {
              void (async () => {
                setActionLoadingId(itemId)
                try {
                  const nextStatus = toggle.getNextStatus(item)
                  await toggle.updateStatus(item, nextStatus)
                  toast.success(toggle.successMessage(item, nextStatus))
                  await reload()
                } catch (error) {
                  toast.error(
                    error instanceof Error ? error.message : toggle.errorMessage
                  )
                } finally {
                  setActionLoadingId(null)
                }
              })()
            }}
            aria-label={toggle.ariaLabel(item)}
          />
          {badge}
        </div>
      )
    },
  }
}

export function createDashboardStatusToggleAction<TItem, TStatus>({
  key = "toggle-status",
  icon,
  label,
  visible,
  disabled,
  getNextStatus,
  updateStatus,
  successMessage,
  errorMessage,
}: DashboardCrudStatusToggleActionOptions<TItem, TStatus>): DashboardCrudRowAction<TItem> {
  return {
    key,
    icon,
    visible,
    disabled,
    label: (item) => label(item, getNextStatus(item)),
    run: async ({ item, reload }) => {
      try {
        const nextStatus = getNextStatus(item)
        await updateStatus(item, nextStatus)
        toast.success(successMessage(item, nextStatus))
        await reload()
      } catch (error) {
        toast.error(error instanceof Error ? error.message : errorMessage)
      }
    },
  }
}
