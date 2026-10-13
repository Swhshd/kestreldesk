"use client"

import {
  createContext,
  useCallback,
  useContext,
  useRef,
  useState,
  type ReactNode,
} from "react"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { useI18n } from "@/i18n/provider"

type ConfirmOptions = {
  title?: ReactNode
  description?: ReactNode
  confirmText?: string
  cancelText?: string
  variant?: "default" | "destructive"
}

type ConfirmContextValue = {
  confirm: (options: ConfirmOptions) => Promise<boolean>
}

type ConfirmState = ConfirmOptions & {
  open: boolean
}

const ConfirmContext = createContext<ConfirmContextValue | null>(null)

const defaultState: ConfirmState = {
  open: false,
  variant: "default",
}

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const t = useI18n()
  const [state, setState] = useState<ConfirmState>(defaultState)
  const resolverRef = useRef<((value: boolean) => void) | null>(null)

  const close = useCallback((result: boolean) => {
    resolverRef.current?.(result)
    resolverRef.current = null
    setState((current) => ({ ...current, open: false }))
  }, [])

  const confirm = useCallback((options: ConfirmOptions) => {
    if (resolverRef.current) {
      resolverRef.current(false)
    }

    setState({
      open: true,
      title: options.title ?? t("confirm.title"),
      description: options.description ?? t("confirm.description"),
      confirmText: options.confirmText ?? t("confirm.confirm"),
      cancelText: options.cancelText ?? t("confirm.cancel"),
      variant: options.variant ?? defaultState.variant,
    })

    return new Promise<boolean>((resolve) => {
      resolverRef.current = resolve
    })
  }, [t])

  return (
    <ConfirmContext.Provider value={{ confirm }}>
      {children}
      <Dialog
        open={state.open}
        onOpenChange={(open) => {
          if (!open) {
            close(false)
          }
        }}
      >
        <DialogContent className="sm:max-w-md" showCloseButton>
          <DialogHeader>
            <DialogTitle>{state.title}</DialogTitle>
            <DialogDescription>{state.description}</DialogDescription>
          </DialogHeader>
          <DialogFooter className="p-2">
            <Button
              type="button"
              variant="outline"
              onClick={() => close(false)}
            >
              {state.cancelText}
            </Button>
            <Button
              type="button"
              variant={state.variant}
              onClick={() => close(true)}
            >
              {state.confirmText}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </ConfirmContext.Provider>
  )
}

export function useConfirm() {
  const ctx = useContext(ConfirmContext)
  if (!ctx) {
    throw new Error("useConfirm must be used within ConfirmProvider")
  }
  return ctx.confirm
}

export type { ConfirmOptions }
