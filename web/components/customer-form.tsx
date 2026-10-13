"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { zodResolver } from "@hookform/resolvers/zod"
import {
  Controller,
  useFieldArray,
  useForm,
  type Resolver,
  type UseFormReturn,
} from "react-hook-form"
import { PlusIcon, Trash2Icon } from "lucide-react"
import { z } from "zod/v4"

import { CompanyPicker } from "@/components/company-picker"
import { OptionCombobox } from "@/components/option-combobox"
import { Button } from "@/components/ui/button"
import {
  Field,
  FieldContent,
  FieldError,
  FieldLabel,
} from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { fetchCustomerContacts, type AdminCustomerContact } from "@/lib/api/customer-contact"
import {
  fetchCustomer,
  type AdminCustomer,
  type SaveCustomerProfilePayload,
} from "@/lib/api/customer"
import { ContactType, Gender } from "@/lib/generated/enums"
import { useI18n } from "@/i18n/provider"

const genderValueOptions = [
  String(Gender.Unknown),
  String(Gender.Male),
  String(Gender.Female),
] as const

const contactTypeValues = [
  ContactType.Mobile,
  ContactType.Email,
  ContactType.Other,
] as const

const contactRowSchema = z.object({
  id: z.number().optional(),
  contactType: z.enum(contactTypeValues),
  contactValue: z.string(),
  remark: z.string(),
  isPrimary: z.boolean(),
})

export type CustomerFormValues = {
  name: string
  gender: (typeof genderValueOptions)[number]
  companyId: string
  remark: string
  contacts: CustomerContactFormRow[]
}

export type CustomerContactFormRow = {
  id?: number
  contactType: (typeof contactTypeValues)[number]
  contactValue: string
  remark: string
  isPrimary: boolean
}

function defaultContactRow(isPrimary: boolean): CustomerContactFormRow {
  return {
    contactType: ContactType.Mobile,
    contactValue: "",
    remark: "",
    isPrimary,
  }
}

const emptyCustomerForm: CustomerFormValues = {
  name: "",
  gender: "0",
  companyId: "0",
  remark: "",
  contacts: [defaultContactRow(true)],
}

function buildCustomerMainFromAdmin(item: AdminCustomer | null): Omit<CustomerFormValues, "contacts"> {
  if (!item) {
    return {
      name: "",
      gender: "0",
      companyId: "0",
      remark: "",
    }
  }
  return {
    name: item.name,
    gender: String(item.gender) as "0" | "1" | "2",
    companyId: String(item.companyId ?? 0),
    remark: item.remark ?? "",
  }
}

function buildContactsFromApi(list: AdminCustomerContact[]): CustomerContactFormRow[] {
  if (list.length === 0) {
    return [defaultContactRow(true)]
  }
  return list.map((c) => ({
    id: c.id,
    contactType: c.contactType as CustomerContactFormRow["contactType"],
    contactValue: c.contactValue ?? "",
    remark: c.remark ?? "",
    isPrimary: c.isPrimary,
  }))
}

/** Filters empty rows and keeps at most one primary contact. */
export function normalizeContactsForSubmit(rows: CustomerContactFormRow[]): CustomerContactFormRow[] {
  const withValue = rows.filter((r) => r.contactValue.trim() !== "")
  if (withValue.length === 0) {
    return []
  }
  const primaryIdx = withValue.findIndex((r) => r.isPrimary)
  if (primaryIdx < 0) {
    return withValue.map((r, i) => ({ ...r, isPrimary: i === 0 }))
  }
  return withValue.map((r, i) => ({
    ...r,
    isPrimary: i === primaryIdx,
  }))
}

export type CustomerFormSavePayload = SaveCustomerProfilePayload

type CustomerFormFieldsProps = {
  form: UseFormReturn<CustomerFormValues>
  fieldIdPrefix?: string
  remarkRows?: number
}

function CustomerFormFields({
  form,
  fieldIdPrefix = "customer",
  remarkRows = 4,
}: CustomerFormFieldsProps) {
  const t = useI18n()
  const {
    control,
    register,
    formState: { errors },
    watch,
    setValue,
    getValues,
  } = form
  const { fields, append, remove } = useFieldArray({ control, name: "contacts" })
  const genderOptions = useMemo(
    () => [
      { value: String(Gender.Unknown), label: t("customerForm.genderUnknown") },
      { value: String(Gender.Male), label: t("customerForm.genderMale") },
      { value: String(Gender.Female), label: t("customerForm.genderFemale") },
    ],
    [t]
  )
  const contactTypeOptions = useMemo(
    () => [
      { value: ContactType.Mobile, label: t("customerForm.contactMobile") },
      { value: ContactType.Email, label: t("customerForm.contactEmail") },
      { value: ContactType.Other, label: t("customerForm.contactOther") },
    ],
    [t]
  )

  const id = (suffix: string) => `${fieldIdPrefix}-${suffix}`

  function setPrimaryIndex(index: number) {
    fields.forEach((_, i) => {
      setValue(`contacts.${i}.isPrimary`, i === index)
    })
  }

  function addContactRow() {
    append(defaultContactRow(fields.length === 0))
  }

  function removeContactRow(index: number) {
    const wasPrimary = watch(`contacts.${index}.isPrimary`)
    remove(index)
    if (wasPrimary) {
      requestAnimationFrame(() => {
        const list = getValues("contacts")
        if (list.length > 0) {
          list.forEach((_, i) => setValue(`contacts.${i}.isPrimary`, i === 0))
        }
      })
    }
  }

  return (
    <div className="space-y-8">
      <div className="space-y-3">
        <h3 className="text-sm font-semibold text-muted-foreground">{t("customerForm.sectionCustomer")}</h3>
        <div className="space-y-4">
          <Field data-invalid={!!errors.name}>
            <FieldLabel htmlFor={id("name")}>{t("customerForm.name")}</FieldLabel>
            <FieldContent>
              <Input
                id={id("name")}
                placeholder={t("customerForm.namePlaceholder")}
                aria-invalid={!!errors.name}
                autoComplete="off"
                {...register("name")}
              />
              <FieldError errors={[errors.name]} />
            </FieldContent>
          </Field>

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field data-invalid={!!errors.gender}>
              <FieldLabel htmlFor={id("gender")}>{t("customerForm.gender")}</FieldLabel>
              <FieldContent>
                <Controller
                  control={control}
                  name="gender"
                  render={({ field }) => (
                    <OptionCombobox
                      value={field.value}
                      options={genderOptions}
                      placeholder={t("customerForm.gender")}
                      onChange={field.onChange}
                    />
                  )}
                />
                <FieldError errors={[errors.gender]} />
              </FieldContent>
            </Field>

            <Field data-invalid={!!errors.companyId}>
              <FieldLabel htmlFor={id("company")}>{t("customerForm.company")}</FieldLabel>
              <FieldContent>
                <Controller
                  control={control}
                  name="companyId"
                  render={({ field }) => (
                    <CompanyPicker
                      value={field.value}
                      onChange={field.onChange}
                    />
                  )}
                />
                <FieldError errors={[errors.companyId]} />
              </FieldContent>
            </Field>
          </div>

          <Field data-invalid={!!errors.remark}>
            <FieldLabel htmlFor={id("remark")}>{t("customerForm.remark")}</FieldLabel>
            <FieldContent>
              <Textarea
                id={id("remark")}
                placeholder={t("customerForm.optional")}
                rows={remarkRows}
                aria-invalid={!!errors.remark}
                {...register("remark")}
              />
              <FieldError errors={[errors.remark]} />
            </FieldContent>
          </Field>
        </div>
      </div>

      <div className="space-y-3">
        <h3 className="text-sm font-semibold text-muted-foreground">{t("customerForm.sectionContacts")}</h3>
        <div className="hidden gap-2 border-b border-border pb-2 text-xs font-medium text-muted-foreground sm:grid sm:grid-cols-[108px_minmax(0,1fr)_minmax(0,1fr)_5.5rem_2.25rem] sm:items-center sm:gap-x-2">
          <span>{t("customerForm.type")}</span>
          <span>{t("customerForm.contact")}</span>
          <span>{t("customerForm.remark")}</span>
          <span className="text-center">{t("customerForm.primary")}</span>
          <span className="sr-only">{t("customerForm.actions")}</span>
        </div>

        <div className="space-y-1">
          {fields.map((field, index) => {
            const err = errors.contacts?.[index]
            return (
              <div
                key={field.id}
                className="grid grid-cols-1 gap-2 border-b border-border py-2 last:border-b-0 sm:grid-cols-[108px_minmax(0,1fr)_minmax(0,1fr)_5.5rem_2.25rem] sm:items-center sm:gap-x-2"
              >
                <div className="min-w-0 space-y-1 sm:space-y-0">
                  <span className="text-xs text-muted-foreground sm:hidden">{t("customerForm.type")}</span>
                  <Controller
                    control={control}
                    name={`contacts.${index}.contactType`}
                    render={({ field: f }) => (
                      <OptionCombobox
                        value={f.value}
                        options={contactTypeOptions}
                        placeholder={t("customerForm.type")}
                        onChange={f.onChange}
                      />
                    )}
                  />
                </div>

                <Field data-invalid={!!err?.contactValue} className="min-w-0 gap-1 sm:gap-0">
                  <FieldLabel className="text-xs text-muted-foreground sm:sr-only">{t("customerForm.contact")}</FieldLabel>
                  <FieldContent>
                    <Input
                      placeholder={
                        watch(`contacts.${index}.contactType`) === ContactType.Email
                          ? t("customerForm.emailPlaceholder")
                          : t("customerForm.contactPlaceholder")
                      }
                      aria-invalid={!!err?.contactValue}
                      {...register(`contacts.${index}.contactValue`)}
                    />
                    <FieldError errors={[err?.contactValue]} />
                  </FieldContent>
                </Field>

                <Field className="min-w-0 gap-1 sm:gap-0">
                  <FieldLabel htmlFor={id(`tag-${index}`)} className="text-xs text-muted-foreground sm:sr-only">
                    {t("customerForm.remark")}
                  </FieldLabel>
                  <FieldContent>
                    <Input
                      id={id(`tag-${index}`)}
                      placeholder={t("customerForm.optional")}
                      {...register(`contacts.${index}.remark`)}
                    />
                  </FieldContent>
                </Field>

                <div className="flex items-center justify-start gap-2 sm:justify-center">
                  <span className="text-xs text-muted-foreground sm:hidden">{t("customerForm.primaryContact")}</span>
                  <input
                    type="radio"
                    className="size-4 shrink-0 accent-primary"
                    name={id("primary-group")}
                    checked={watch(`contacts.${index}.isPrimary`)}
                    onChange={() => setPrimaryIndex(index)}
                    id={id(`primary-${index}`)}
                    aria-label={t("customerForm.setPrimary")}
                  />
                  <label htmlFor={id(`primary-${index}`)} className="hidden cursor-pointer text-sm sm:inline">
                    {t("customerForm.primary")}
                  </label>
                </div>

                <div className="flex justify-end sm:justify-center">
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="text-muted-foreground hover:text-destructive"
                    onClick={() => removeContactRow(index)}
                    aria-label={t("customerForm.deleteContact")}
                  >
                    <Trash2Icon className="size-4" />
                  </Button>
                </div>
              </div>
            )
          })}
        </div>

        <Button type="button" variant="outline" size="sm" className="gap-1" onClick={addContactRow}>
          <PlusIcon className="size-4" />
          {t("customerForm.addContact")}
        </Button>
      </div>
    </div>
  )
}

export type CustomerFormProps = {
  formId: string
  onSave: (payload: CustomerFormSavePayload) => Promise<void> | void
  itemId?: number | null
  fieldIdPrefix?: string
  remarkRows?: number
  className?: string
  onLoadingDetailChange?: (loading: boolean) => void
}

export function CustomerForm({
  formId,
  onSave,
  itemId,
  fieldIdPrefix = "customer",
  remarkRows = 4,
  className,
  onLoadingDetailChange,
}: CustomerFormProps) {
  const t = useI18n()
  const [loadingDetail, setLoadingDetail] = useState(() => Boolean(itemId))
  const customerFormSchema = useMemo(
    () =>
      z.object({
        name: z.string().trim().min(1, t("customerForm.nameRequired")),
        gender: z.enum(genderValueOptions, { message: t("customerForm.genderRequired") }),
        companyId: z.string().trim().regex(/^\d+$/, t("customerForm.companyRequired")),
        remark: z.string().trim(),
        contacts: z.array(contactRowSchema),
      }),
    [t]
  )
  const customerFormResolver = useMemo(
    () => zodResolver(customerFormSchema as never) as Resolver<CustomerFormValues>,
    [customerFormSchema]
  )

  const form = useForm<CustomerFormValues>({
    resolver: customerFormResolver,
    defaultValues: emptyCustomerForm,
  })
  const { handleSubmit, reset } = form
  const onLoadingDetailChangeRef = useRef(onLoadingDetailChange)
  onLoadingDetailChangeRef.current = onLoadingDetailChange

  useEffect(() => {
    async function loadDetail() {
      const notify = (loading: boolean) => {
        onLoadingDetailChangeRef.current?.(loading)
      }
      if (!itemId) {
        setLoadingDetail(false)
        notify(false)
        reset(emptyCustomerForm)
        return
      }
      setLoadingDetail(true)
      notify(true)
      try {
        const [customer, contacts] = await Promise.all([
          fetchCustomer(itemId),
          fetchCustomerContacts(itemId),
        ])
        reset({
          ...buildCustomerMainFromAdmin(customer),
          contacts: buildContactsFromApi(contacts),
        })
      } finally {
        setLoadingDetail(false)
        notify(false)
      }
    }
    void loadDetail()
  }, [itemId, reset])

  async function onFormSubmit(values: CustomerFormValues) {
    const contacts = normalizeContactsForSubmit(values.contacts as CustomerContactFormRow[])
    const body: SaveCustomerProfilePayload = {
      name: values.name.trim(),
      gender: Number(values.gender),
      companyId: Number(values.companyId),
      remark: values.remark.trim(),
      contacts: contacts.map((c) => ({
        id: c.id,
        contactType: c.contactType,
        contactValue: c.contactValue.trim(),
        remark: c.remark.trim(),
        isPrimary: c.isPrimary,
      })),
    }
    if (itemId) {
      body.id = itemId
    }
    await onSave(body)
  }

  if (loadingDetail) {
    return (
      <div className="flex items-center justify-center py-12">
        <div className="text-muted-foreground">{t("customerForm.loading")}</div>
      </div>
    )
  }

  return (
    <form id={formId} onSubmit={handleSubmit(onFormSubmit)} className={className}>
      <CustomerFormFields
        form={form}
        fieldIdPrefix={fieldIdPrefix}
        remarkRows={remarkRows}
      />
    </form>
  )
}
