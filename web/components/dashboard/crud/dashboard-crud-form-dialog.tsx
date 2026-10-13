"use client"

import { useEffect, useMemo, useState } from "react"
import { zodResolver } from "@hookform/resolvers/zod"
import type { Resolver } from "react-hook-form"
import { useForm } from "react-hook-form"
import { z } from "zod/v4"

import { ProjectDialog } from "@/components/project-dialog"
import { Button } from "@/components/ui/button"
import {
  buildDashboardCrudFormValues,
  normalizeDashboardCrudSubmitValues,
  type DashboardCrudFormInputValue,
  type DashboardCrudFormField,
  type DashboardCrudFormOption,
} from "./dashboard-crud-utils"
import { DashboardCrudFieldControl } from "./dashboard-crud-field-control"

type DashboardCrudFormDialogProps<TItem, TPayload> = {
  open: boolean
  saving: boolean
  item: TItem | null
  itemId: number | null
  fields: DashboardCrudFormField<TItem>[]
  fetchDetail?: (id: number) => Promise<TItem>
  transformSubmitValues?: (
    values: Record<string, string | number | boolean | string[] | number[]>,
    context: { mode: "create" | "edit"; item: TItem | null }
  ) => TPayload
  labels: {
    createTitle: string
    editTitle: string
    create: string
    save: string
    saving: string
    cancel: string
    loadingDetail: string
    required: string
    invalidNumber: string
    minValue: (min: number) => string
    maxValue: (max: number) => string
  }
  onOpenChange: (open: boolean) => void
  onSubmit: (payload: TPayload) => Promise<void>
}

function createFormSchema<TItem>(
  fields: ReadonlyArray<DashboardCrudFormField<TItem>>,
  labels: DashboardCrudFormDialogProps<TItem, unknown>["labels"]
) {
  const shape: Record<string, z.ZodType> = {}

  fields.forEach((field) => {
    if (field.type === "section" || field.type === "group") {
      return
    }
    if (field.type === "switch" || field.type === "checkbox") {
      shape[field.name] = field.required
        ? z.boolean().refine((value) => value, field.requiredMessage ?? labels.required)
        : z.boolean()
      return
    }
    if (field.type === "multiSelect") {
      shape[field.name] = field.required
        ? z.array(z.string()).min(1, field.requiredMessage ?? labels.required)
        : z.array(z.string())
      return
    }
    if (field.type === "custom") {
      shape[field.name] = z.any()
      return
    }
    let schema = field.trim ? z.string().trim() : z.string()
    if (field.required) {
      schema = schema.min(1, field.requiredMessage ?? labels.required)
    }
    if (field.pattern) {
      schema = schema.regex(field.pattern, field.patternMessage ?? labels.required)
    }
    if (field.type === "number") {
      schema = schema.refine((value) => {
        if (!value.trim()) return !field.required
        return Number.isFinite(Number(value))
      }, labels.invalidNumber)
      if (field.min !== undefined) {
        schema = schema.refine((value) => !value.trim() || Number(value) >= field.min!, {
          message: labels.minValue(field.min),
        })
      }
      if (field.max !== undefined) {
        schema = schema.refine((value) => !value.trim() || Number(value) <= field.max!, {
          message: labels.maxValue(field.max),
        })
      }
    }
    if (field.type === "json" && field.validateJson !== false) {
      schema = schema.refine((value) => {
        if (!value.trim()) return !field.required
        try {
          JSON.parse(value)
          return true
        } catch {
          return false
        }
      }, field.patternMessage ?? labels.required)
    }
    shape[field.name] = schema
  })

  return z.object(shape)
}

function normalizeFormLayoutFields<TItem>(fields: DashboardCrudFormField<TItem>[]) {
  return fields.map((field) =>
    ["textarea", "json", "code", "custom", "section", "group"].includes(
      field.type ?? ""
    )
      ? { ...field, colSpan: field.colSpan ?? 2 }
      : field
  )
}

export function DashboardCrudFormDialog<TItem, TPayload>({
  open,
  saving,
  item,
  itemId,
  fields,
  fetchDetail,
  transformSubmitValues,
  labels,
  onOpenChange,
  onSubmit,
}: DashboardCrudFormDialogProps<TItem, TPayload>) {
  const layoutFields = useMemo(() => normalizeFormLayoutFields(fields), [fields])
  const initialValues = useMemo(
    () => buildDashboardCrudFormValues(fields, item),
    [fields, item]
  )
  const schema = useMemo(() => createFormSchema(fields, labels), [fields, labels])
  const resolver = useMemo(
    () =>
      zodResolver(schema as never) as Resolver<
        Record<string, DashboardCrudFormInputValue>,
        undefined,
        Record<string, DashboardCrudFormInputValue>
      >,
    [schema]
  )
  const [fetchedDetail, setFetchedDetail] = useState<{
    id: number
    item: TItem
  } | null>(null)
  const [fieldOptions, setFieldOptions] = useState<
    Record<string, ReadonlyArray<DashboardCrudFormOption>>
  >({})
  const form = useForm<
    Record<string, DashboardCrudFormInputValue>,
    undefined,
    Record<string, DashboardCrudFormInputValue>
  >({
    resolver,
    defaultValues: initialValues,
  })
  const {
    control,
    handleSubmit,
    register,
    reset,
    formState: { errors },
  } = form
  const formId = "dashboard-crud-edit-form"
  const mode = itemId ? "edit" : "create"
  const loadingDetail = Boolean(
    itemId && fetchDetail && fetchedDetail?.id !== itemId
  )
  const detailItem =
    itemId && fetchDetail && fetchedDetail?.id === itemId
      ? fetchedDetail.item
      : item

  useEffect(() => {
    let cancelled = false

    if (!itemId || !fetchDetail) {
      reset(initialValues)
      return
    }

    void fetchDetail(itemId)
      .then((detail) => {
        if (cancelled) return
        setFetchedDetail({ id: itemId, item: detail })
        reset(buildDashboardCrudFormValues(fields, detail))
      })

    return () => {
      cancelled = true
    }
  }, [fetchDetail, fields, initialValues, item, itemId, reset])

  useEffect(() => {
    if (!open) return

    let cancelled = false
    const loaders = fields
      .filter(
        (field) =>
          (field.type === "select" || field.type === "multiSelect") &&
          field.loadOptions
      )
      .map(async (field) => {
        const options = await field.loadOptions!()
        return [field.name, options] as const
      })

    if (loaders.length === 0) return

    void Promise.all(loaders).then((entries) => {
      if (cancelled) return
      setFieldOptions((current) => ({
        ...current,
        ...Object.fromEntries(entries),
      }))
    })

    return () => {
      cancelled = true
    }
  }, [fields, open])

  async function submit(values: Record<string, DashboardCrudFormInputValue>) {
    const normalizedValues = normalizeDashboardCrudSubmitValues(fields, values)
    const payload = transformSubmitValues
      ? transformSubmitValues(normalizedValues, { mode, item: detailItem })
      : (normalizedValues as TPayload)
    await onSubmit(payload)
  }

  if (!open || fields.length === 0) {
    return null
  }

  return (
    <ProjectDialog
      open={open}
      onOpenChange={onOpenChange}
      title={mode === "edit" ? labels.editTitle : labels.createTitle}
      size="md"
      allowFullscreen
      footer={
        <>
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
            disabled={saving}
          >
            {labels.cancel}
          </Button>
          <Button type="submit" form={formId} disabled={saving || loadingDetail}>
            {saving ? labels.saving : mode === "edit" ? labels.save : labels.create}
          </Button>
        </>
      }
    >
      {loadingDetail ? (
        <div className="flex items-center justify-center py-12">
          <div className="text-muted-foreground">{labels.loadingDetail}</div>
        </div>
      ) : (
        <form
          id={formId}
          onSubmit={handleSubmit(submit)}
          className="grid gap-4 md:grid-cols-2"
        >
          {layoutFields.map((field) => (
            <DashboardCrudFieldControl
              key={field.name}
              field={{
                ...field,
                options: fieldOptions[field.name] ?? field.options,
              }}
              control={control}
              form={form}
              register={register}
              error={errors[field.name]}
            />
          ))}
        </form>
      )}
    </ProjectDialog>
  )
}
