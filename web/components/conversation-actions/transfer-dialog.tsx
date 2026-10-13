"use client"

import { zodResolver } from "@hookform/resolvers/zod"
import { ArrowRightLeftIcon } from "lucide-react"
import { useEffect, useMemo, useState } from "react"
import { Controller, Resolver, useForm } from "react-hook-form"
import { toast } from "sonner"
import { z } from "zod/v4"

import { OptionCombobox } from "@/components/option-combobox"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle
} from "@/components/ui/dialog"
import {
  Field,
  FieldContent,
  FieldError,
  FieldLabel,
} from "@/components/ui/field"
import { Textarea } from "@/components/ui/textarea"
import {
  assignConversation,
  transferConversation,
  fetchAgentProfilesAll,
  type AdminAgentProfile,
} from "@/lib/api/admin"
import { useI18n } from "@/i18n/provider"

type ConversationTransferDialogProps = {
  open: boolean
  mode: "assign" | "transfer"
  conversationId: number | null
  onOpenChange: (open: boolean) => void
  onSuccess?: () => Promise<void> | void
}

type TransferForm = {
  toUserId: string
  reason: string
}

const emptyForm: TransferForm = {
  toUserId: "",
  reason: "",
}

export function ConversationTransferDialog({
  open,
  mode,
  conversationId,
  onOpenChange,
  onSuccess,
}: ConversationTransferDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {open ? (
        <ConversationTransferDialogBody
          key={conversationId ? `transfer-${conversationId}` : "transfer"}
          mode={mode}
          conversationId={conversationId}
          onOpenChange={onOpenChange}
          onSuccess={onSuccess}
        />
      ) : null}
    </Dialog>
  )
}

type ConversationTransferDialogBodyProps = {
  mode: "assign" | "transfer"
  conversationId: number | null
  onOpenChange: (open: boolean) => void
  onSuccess?: () => Promise<void> | void
}

function ConversationTransferDialogBody({
  mode,
  conversationId,
  onOpenChange,
  onSuccess,
}: ConversationTransferDialogBodyProps) {
  const t = useI18n()
  const [saving, setSaving] = useState(false)
  const [loadingAgents, setLoadingAgents] = useState(false)
  const [agents, setAgents] = useState<AdminAgentProfile[]>([])
  const userOptions = agents.map((agent) => ({
    value: String(agent.userId),
    label: agent.displayName || agent.nickname || agent.username || t("conversationAction.agentFallback", { id: agent.userId }),
  }))

  const transferSchema = useMemo(
    () =>
      z.object({
        toUserId: z.string().trim().min(1, t("conversationAction.targetAgentRequired")),
        reason: z.string().trim(),
      }),
    [t]
  )
  const transferResolver = useMemo(
    () => zodResolver(transferSchema as never) as Resolver<TransferForm>,
    [transferSchema]
  )

  const form = useForm<TransferForm>({
    resolver: transferResolver,
    defaultValues: emptyForm,
  })
  const {
    control,
    handleSubmit,
    reset,
    register,
    formState: { errors },
  } = form

  useEffect(() => {
    reset(emptyForm)
  }, [conversationId, reset])

  useEffect(() => {
    setLoadingAgents(true)
    fetchAgentProfilesAll()
      .then((data) => {
        setAgents(data.filter((item) => item.serviceStatus === 0))
      })
      .catch((error) => {
        toast.error(error instanceof Error ? error.message : t("conversationAction.loadAgentsFailed"))
      })
      .finally(() => {
        setLoadingAgents(false)
      })
  }, [t])

  async function onFormSubmit(values: TransferForm) {
    if (!conversationId) {
      toast.error(t("conversationAction.conversationMissing"))
      return
    }

    const toUserId = Number(values.toUserId)
    const reason = values.reason.trim()

    setSaving(true)
    try {
      if (mode === "assign") {
        await assignConversation(conversationId, toUserId, reason)
        toast.success(t("conversationAction.assigned", { id: conversationId }))
      } else {
        await transferConversation(conversationId, toUserId, reason)
        toast.success(t("conversationAction.transferred", { id: conversationId }))
      }
      reset(emptyForm)
      onOpenChange(false)
      await onSuccess?.()
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : mode === "assign"
            ? t("conversationAction.assignFailed")
            : t("conversationAction.transferFailed")
      )
    } finally {
      setSaving(false)
    }
  }

  const isAssign = mode === "assign"

  return (
    <DialogContent className="max-w-lg gap-0 p-0 sm:max-w-lg">
      <DialogHeader className="px-6 pt-6">
        <DialogTitle>
          {isAssign ? t("conversationAction.assignTitle") : t("conversationAction.transferTitle")}
        </DialogTitle>
      </DialogHeader>
      <form onSubmit={handleSubmit(onFormSubmit)}>
        <div className="space-y-4 p-6">
          <Field data-invalid={!!errors.toUserId}>
            <FieldLabel htmlFor="conversation-transfer-user">
              {t("conversationAction.targetAgent")}
            </FieldLabel>
            <FieldContent>
              <Controller
                control={control}
                name="toUserId"
                render={({ field }) => (
                  <OptionCombobox
                    value={field.value}
                    options={userOptions}
                    placeholder={
                      loadingAgents
                        ? t("conversationAction.loading")
                        : t("conversationAction.selectTargetAgent")
                    }
                    searchPlaceholder={t("conversationAction.searchAgent")}
                    emptyText={t("conversationAction.emptyAgents")}
                    disabled={saving || loadingAgents}
                    onChange={field.onChange}
                  />
                )}
              />
              <FieldError errors={[errors.toUserId]} />
            </FieldContent>
          </Field>
          <Field data-invalid={!!errors.reason}>
            <FieldLabel htmlFor="conversation-transfer-reason">
              {isAssign ? t("conversationAction.assignNote") : t("conversationAction.transferReason")}
            </FieldLabel>
            <FieldContent>
              <Textarea
                id="conversation-transfer-reason"
                rows={4}
                placeholder={
                  isAssign
                    ? t("conversationAction.assignPlaceholder")
                    : t("conversationAction.transferPlaceholder")
                }
                aria-invalid={!!errors.reason}
                {...register("reason")}
              />
              <FieldError errors={[errors.reason]} />
            </FieldContent>
          </Field>
        </div>
        <DialogFooter className="mx-0 mb-0 px-6 py-4">
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={saving}
          >
            {t("conversationAction.cancel")}
          </Button>
          <Button type="submit" disabled={saving}>
            <ArrowRightLeftIcon />
            {saving
              ? isAssign
                ? t("conversationAction.assigning")
                : t("conversationAction.transferring")
              : isAssign
                ? t("conversationAction.confirmAssign")
                : t("conversationAction.confirmTransfer")}
          </Button>
        </DialogFooter>
      </form>
    </DialogContent>
  )
}
