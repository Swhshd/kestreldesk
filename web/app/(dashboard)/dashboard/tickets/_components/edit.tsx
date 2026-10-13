"use client"

// This file has been modified for the KestrelDesk project.

import { useEffect, useMemo, useState } from "react"
import { zodResolver } from "@hookform/resolvers/zod"
import type { Resolver } from "react-hook-form"
import { Controller, useForm } from "react-hook-form"
import { z } from "zod/v4"

import { ContentEditor } from "@/components/content-editor"
import { OptionCombobox } from "@/components/option-combobox"
import { ProjectDialog } from "@/components/project-dialog"
import { isRichTextEmpty } from "@/components/safe-rich-html"
import { TagSelector } from "@/components/tag-selector"
import { useAuth } from "@/components/auth-provider"
import { Button } from "@/components/ui/button"
import {
  Field,
  FieldContent,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  fetchAgentProfilesAll,
  fetchTagsAll,
  type TagTree,
} from "@/lib/api/admin"
import {
  fetchTicketCreateAssigneeOptions,
  fetchTicketDetail,
  type CreateTicketPayload,
  type TicketItem,
  type UpdateTicketPayload,
} from "@/lib/api/ticket"
import {
  getTicketAssigneeField,
  getTicketAssigneeMode,
  type TicketAssigneeCandidate,
} from "@/lib/ticket-assignee-policy"
import { useI18n } from "@/i18n/provider"

type TFunction = (key: string, values?: Record<string, string | number>) => string

type EditDialogProps = {
  open: boolean
  saving: boolean
  itemId: number | null
  initialValues?: Partial<CreateTicketPayload>
  fixedConversationId?: number
  fixedCustomerId?: number
  fixedCustomerLabel?: string
  titleOverride?: string
  descriptionOverride?: string
  onOpenChange: (open: boolean) => void
  onSubmit: (payload: CreateTicketPayload | UpdateTicketPayload) => Promise<void>
}

function createSchema(t: TFunction) {
  return z.object({
  title: z.string().trim().min(1, t("ticket.titleRequired")),
  description: z.string().refine((value) => !isRichTextEmpty(value), t("ticket.descriptionRequired")),
  currentAssigneeId: z.coerce.number().int().min(0).optional(),
  tagIds: z.array(z.number().int().positive()).default([]),
  })
}

type EditForm = {
  title: string
  description: string
  currentAssigneeId?: number
  tagIds: number[]
}

const emptyForm: EditForm = {
  title: "",
  description: "",
  currentAssigneeId: 0,
  tagIds: [],
}

function buildForm(item: TicketItem | null): EditForm {
  if (!item) {
    return emptyForm
  }
  return {
    title: item.title ?? "",
    description: item.description ?? "",
    currentAssigneeId: item.currentAssigneeId ?? 0,
    tagIds: (item.tags ?? []).map((tag) => tag.id),
  }
}

function buildInitialForm(initialValues?: Partial<CreateTicketPayload>): EditForm {
  return {
    title: initialValues?.title?.trim() ?? "",
    description: initialValues?.description ?? "",
    currentAssigneeId: initialValues?.currentAssigneeId ?? 0,
    tagIds: initialValues?.tagIds ?? [],
  }
}

function buildPayload(form: EditForm): CreateTicketPayload {
  const currentAssigneeId = form.currentAssigneeId ?? 0
  return {
    title: form.title.trim(),
    description: form.description.trim(),
    currentAssigneeId,
    tagIds: form.tagIds,
  }
}

export function EditDialog({
  open,
  saving,
  itemId,
  initialValues,
  fixedConversationId,
  fixedCustomerId,
  fixedCustomerLabel,
  titleOverride,
  descriptionOverride,
  onOpenChange,
  onSubmit,
}: EditDialogProps) {
  if (!open) {
    return null
  }
  return (
    <TicketEditDialogBody
      key={itemId ? `edit-${itemId}` : "create"}
      open={open}
      saving={saving}
      itemId={itemId}
      initialValues={initialValues}
      fixedConversationId={fixedConversationId}
      fixedCustomerId={fixedCustomerId}
      fixedCustomerLabel={fixedCustomerLabel}
      titleOverride={titleOverride}
      descriptionOverride={descriptionOverride}
      onOpenChange={onOpenChange}
      onSubmit={onSubmit}
    />
  )
}

type TicketEditDialogBodyProps = EditDialogProps

function TicketEditDialogBody({
  open,
  saving,
  itemId,
  initialValues,
  fixedConversationId,
  fixedCustomerId,
  fixedCustomerLabel,
  titleOverride,
  descriptionOverride,
  onOpenChange,
  onSubmit,
}: TicketEditDialogBodyProps) {
  const t = useI18n()
  const { session } = useAuth()
  const assigneeMode = getTicketAssigneeMode(session?.roles ?? [])
  const formId = "ticket-edit-form"
  const [loading, setLoading] = useState(false)
  const [tags, setTags] = useState<TagTree[]>([])
  const [agents, setAgents] = useState<TicketAssigneeCandidate[]>([])
  const schema = useMemo(() => createSchema(t), [t])
  const editFormResolver = useMemo(
    () => zodResolver(schema) as Resolver<EditForm>,
    [schema],
  )
  const form = useForm<EditForm>({
    resolver: editFormResolver,
    defaultValues: emptyForm,
  })
  const {
    register,
    control,
    handleSubmit,
    reset,
    formState: { errors },
  } = form

  useEffect(() => {
    async function loadDetail() {
      if (!itemId) {
        reset(buildInitialForm(initialValues))
        return
      }
      setLoading(true)
      try {
        const data = await fetchTicketDetail(itemId)
        reset(buildForm(data.ticket))
      } finally {
        setLoading(false)
      }
    }
    void loadDetail()
  }, [initialValues, itemId, reset])

  useEffect(() => {
    if (!open) {
      return
    }
    void (async () => {
      const assigneeRequest =
        assigneeMode === "team"
          ? fetchTicketCreateAssigneeOptions()
          : assigneeMode === "global"
            ? fetchAgentProfilesAll()
            : Promise.resolve([])
      const [tagData, agentData] = await Promise.all([
        fetchTagsAll(),
        assigneeRequest,
      ])
      setTags(Array.isArray(tagData) ? tagData : [])
      setAgents(Array.isArray(agentData) ? agentData : [])
    })()
  }, [assigneeMode, open])

  const assigneeField = getTicketAssigneeField({
    mode: assigneeMode,
    currentUserId: session?.user.id ?? 0,
    currentUserName: session?.user.nickname || session?.user.username || "",
    agents,
    noAssigneeLabel: t("ticket.noAssignee"),
    agentFallbackLabel: (id) => t("ticket.agentFallback", { id }),
  })

  async function onFormSubmit(values: EditForm) {
    const payload = buildPayload(values)
    if (itemId) {
      await onSubmit({
        ticketId: itemId,
        ...payload,
      })
      return
    }
    if (assigneeMode === "self") {
      delete payload.currentAssigneeId
    }
    await onSubmit({
      ...payload,
      source: fixedConversationId ? "conversation" : "manual",
      conversationId: fixedConversationId,
      customerId: fixedCustomerId,
    })
  }

  return (
    <ProjectDialog
      open={open}
      onOpenChange={onOpenChange}
      title={titleOverride || (itemId ? t("ticket.editTitle") : t("ticket.createTitle"))}
      description={descriptionOverride || t("ticket.dialogDescription")}
      size="lg"
      allowFullscreen
      footer={
        <>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={saving}
          >
            {t("ticket.cancel")}
          </Button>
          <Button type="submit" form={formId} disabled={saving || loading}>
            {saving ? t("ticket.saving") : itemId ? t("ticket.save") : t("ticket.create")}
          </Button>
        </>
      }
    >
      {loading ? (
        <div className="flex items-center justify-center py-12">
          <div className="text-muted-foreground">{t("ticket.loading")}</div>
        </div>
      ) : (
        <form id={formId} onSubmit={handleSubmit(onFormSubmit)} className="space-y-4">
          {!itemId && fixedConversationId ? (
            <section
              aria-label={t("ticket.ownershipContext")}
              className="rounded-md border bg-muted/30 p-4"
            >
              <dl className="grid gap-3 sm:grid-cols-3">
                <div className="space-y-1">
                  <dt className="text-sm text-muted-foreground">
                    {t("ticket.linkedCustomer")}
                  </dt>
                  <dd className="text-sm font-medium">
                    {fixedCustomerLabel?.trim() ||
                      (fixedCustomerId
                        ? t("ticket.customerIdFallback", { id: fixedCustomerId })
                        : t("ticket.customerUnknown"))}
                  </dd>
                </div>
                <div className="space-y-1">
                  <dt className="text-sm text-muted-foreground">
                    {t("ticket.sourceConversation")}
                  </dt>
                  <dd className="text-sm font-medium">#{fixedConversationId}</dd>
                </div>
                {assigneeField.kind === "fixed" ? (
                  <div className="space-y-1">
                    <dt className="text-sm text-muted-foreground">
                      {t("ticket.assignee")}
                    </dt>
                    <dd>
                      <output className="text-sm font-medium">
                        {assigneeField.label ||
                          t("ticket.agentFallback", { id: assigneeField.userId })}
                      </output>
                    </dd>
                  </div>
                ) : null}
              </dl>
            </section>
          ) : null}
          <FieldGroup>
            <Field data-invalid={!!errors.title}>
              <FieldLabel htmlFor="ticket-title">{t("ticket.title")}</FieldLabel>
              <FieldContent>
                <Input
                  id="ticket-title"
                  placeholder={t("ticket.titlePlaceholder")}
                  aria-invalid={!!errors.title}
                  {...register("title")}
                />
                <FieldError errors={[errors.title]} />
              </FieldContent>
            </Field>

            <Field data-invalid={!!errors.description}>
              <FieldLabel>{t("ticket.description")}</FieldLabel>
              <FieldContent>
                <Controller
                  control={control}
                  name="description"
                  render={({ field }) => (
                    <ContentEditor
                      value={{ mode: "html", raw: field.value ?? "" }}
                      onChange={(next) => field.onChange(next.raw)}
                      placeholder={t("ticket.descriptionRequired")}
                      disabled={saving || loading}
                      allowedModes={["html"]}
                      height={260}
                    />
                  )}
                />
                <FieldError errors={[errors.description]} />
              </FieldContent>
            </Field>

            {assigneeField.kind === "fixed" ? (
              fixedConversationId ? null : (
                <Field>
                  <FieldLabel>{t("ticket.assignee")}</FieldLabel>
                  <FieldContent>
                    <output
                      aria-label={t("ticket.assignee")}
                      className="flex min-h-9 items-center rounded-md border bg-muted/30 px-3 text-sm"
                    >
                      {assigneeField.label ||
                        t("ticket.agentFallback", { id: assigneeField.userId })}
                    </output>
                  </FieldContent>
                </Field>
              )
            ) : (
              <Field>
                <FieldLabel>{t("ticket.assignee")}</FieldLabel>
                <FieldContent>
                  <Controller
                    control={control}
                    name="currentAssigneeId"
                    render={({ field }) => (
                      <OptionCombobox
                        value={String(field.value ?? 0)}
                        onChange={(value) => field.onChange(Number(value))}
                        placeholder={t("ticket.selectAssignee")}
                        options={assigneeField.options}
                      />
                    )}
                  />
                </FieldContent>
              </Field>
            )}

            <Field>
              <FieldLabel>{t("ticket.ticketTags")}</FieldLabel>
              <FieldContent>
                <Controller
                  control={control}
                  name="tagIds"
                  render={({ field }) => (
                    <TagSelector
                      mode="multiple"
                      value={field.value}
                      onChange={field.onChange}
                      tags={tags}
                      placeholder={t("ticket.selectTags")}
                      selectedCountText={(count) => t("ticket.selectedTags", { count })}
                      searchPlaceholder={t("ticket.searchTags")}
                      emptyText={t("ticket.emptyTags")}
                    />
                  )}
                />
              </FieldContent>
            </Field>
          </FieldGroup>
        </form>
      )}
    </ProjectDialog>
  )
}
