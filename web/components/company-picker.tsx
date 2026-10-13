"use client"

import { useEffect, useMemo, useState } from "react"
import { ChevronsUpDownIcon, PlusIcon } from "lucide-react"
import { toast } from "sonner"

import { DashboardCrudFormDialog } from "@/components/dashboard/crud"
import { Button } from "@/components/ui/button"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import {
  createCompany,
  fetchCompanies,
  fetchCompany,
  type AdminCompany,
  type CreateAdminCompanyPayload,
} from "@/lib/api/company"
import { useI18n } from "@/i18n/provider"
import { cn } from "@/lib/utils"

type CompanyPickerProps = {
  value: string
  onChange: (value: string) => void
  disabled?: boolean
  placeholder?: string
}

export function CompanyPicker({
  value,
  onChange,
  disabled = false,
  placeholder,
}: CompanyPickerProps) {
  const t = useI18n()
  const resolvedPlaceholder = placeholder ?? t("companyPicker.placeholder")
  const [open, setOpen] = useState(false)
  const [keyword, setKeyword] = useState("")
  const [loading, setLoading] = useState(false)
  const [options, setOptions] = useState<AdminCompany[]>([])
  const [selectedCompany, setSelectedCompany] = useState<AdminCompany | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [createSaving, setCreateSaving] = useState(false)

  const trimmedKeyword = keyword.trim()
  const normalizedKeyword = trimmedKeyword.toLowerCase()

  useEffect(() => {
    let cancelled = false
    if (!open) {
      return
    }
    setLoading(true)
    void (async () => {
      try {
        const data = await fetchCompanies({
          status: 0,
          page: 1,
          limit: 20,
          name: trimmedKeyword || undefined,
        })
        if (cancelled) {
          return
        }
        setOptions(data.results)
      } catch (error) {
        if (!cancelled) {
          setOptions([])
          toast.error(error instanceof Error ? error.message : t("companyPicker.loadFailed"))
        }
      } finally {
        if (!cancelled) {
          setLoading(false)
        }
      }
    })()

    return () => {
      cancelled = true
    }
  }, [open, trimmedKeyword, t])

  useEffect(() => {
    let cancelled = false
    const companyId = Number(value)
    if (companyId <= 0) {
      setSelectedCompany(null)
      return
    }
    if (selectedCompany?.id === companyId) {
      return
    }
    void (async () => {
      try {
        const data = await fetchCompany(companyId)
        if (!cancelled) {
          setSelectedCompany(data)
        }
      } catch {
        if (!cancelled) {
          setSelectedCompany(null)
        }
      }
    })()
    return () => {
      cancelled = true
    }
  }, [selectedCompany?.id, value])

  const canCreate = useMemo(() => {
    if (!trimmedKeyword) {
      return false
    }
    return !options.some((item) => item.name.trim().toLowerCase() === normalizedKeyword)
  }, [normalizedKeyword, options, trimmedKeyword])

  const buttonLabel =
    Number(value) > 0 ? selectedCompany?.name || t("companyPicker.fallback", { id: value }) : resolvedPlaceholder

  function handleSelectCompany(company: AdminCompany) {
    setSelectedCompany(company)
    onChange(String(company.id))
    setOpen(false)
    setKeyword("")
  }

  function handleClear() {
    setSelectedCompany(null)
    onChange("0")
    setOpen(false)
    setKeyword("")
  }

  async function handleCreateCompany(payload: CreateAdminCompanyPayload) {
    setCreateSaving(true)
    try {
      const created = await createCompany(payload)
      setSelectedCompany(created)
      onChange(String(created.id))
      setCreateOpen(false)
      setOpen(false)
      setKeyword("")
      toast.success(t("companyPicker.created", { name: created.name }))
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("companyPicker.createFailed"))
      throw error
    } finally {
      setCreateSaving(false)
    }
  }

  return (
    <>
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger
          render={
            <Button
              variant="outline"
              role="combobox"
              className="w-full justify-between font-normal"
              disabled={disabled}
            />
          }
        >
          <span className={cn("truncate", Number(value) > 0 ? "text-foreground" : "text-muted-foreground")}>
            {buttonLabel}
          </span>
          <ChevronsUpDownIcon className="ml-2 size-4 shrink-0 opacity-50" />
        </PopoverTrigger>
        <PopoverContent className="w-(--radix-popover-trigger-width) p-0" align="start">
          <Command shouldFilter={false}>
            <CommandInput
              value={keyword}
              onValueChange={setKeyword}
              placeholder={t("companyPicker.searchPlaceholder")}
            />
            <CommandList>
              {loading ? <CommandEmpty>{t("companyPicker.loading")}</CommandEmpty> : null}
              {!loading && options.length === 0 ? <CommandEmpty>{t("companyPicker.empty")}</CommandEmpty> : null}
              {!loading ? (
                <CommandGroup heading={t("companyPicker.results")}>
                  <CommandItem
                    value="none"
                    data-checked={Number(value) <= 0}
                    onSelect={handleClear}
                  >
                    <span>{t("companyPicker.none")}</span>
                  </CommandItem>
                  {options.map((item) => (
                    <CommandItem
                      key={item.id}
                      value={`${item.name} ${item.code}`}
                      data-checked={item.id === Number(value)}
                      onSelect={() => handleSelectCompany(item)}
                    >
                      <div className="flex min-w-0 flex-col">
                        <span className="truncate">{item.name}</span>
                        {item.code ? (
                          <span className="truncate text-xs text-muted-foreground">{item.code}</span>
                        ) : null}
                      </div>
                    </CommandItem>
                  ))}
                </CommandGroup>
              ) : null}
              {canCreate ? (
                <>
                  <CommandSeparator />
                  <CommandGroup heading={t("companyPicker.actions")}>
                    <CommandItem
                      value={`create ${trimmedKeyword}`}
                      onSelect={() => setCreateOpen(true)}
                    >
                      <PlusIcon className="size-4" />
                      <span className="truncate">{t("companyPicker.create", { name: trimmedKeyword })}</span>
                    </CommandItem>
                  </CommandGroup>
                </>
              ) : null}
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>

      <DashboardCrudFormDialog<AdminCompany, CreateAdminCompanyPayload>
        open={createOpen}
        saving={createSaving}
        item={null}
        itemId={null}
        fields={[
          {
            name: "name",
            label: t("company.columnName"),
            placeholder: t("company.namePlaceholder"),
            defaultValue: trimmedKeyword,
            required: true,
            requiredMessage: t("company.nameRequired"),
            trim: true,
          },
          {
            name: "code",
            label: t("company.columnCode"),
            placeholder: t("company.optional"),
            trim: true,
          },
          {
            name: "remark",
            label: t("company.columnRemark"),
            placeholder: t("company.remarkPlaceholder"),
            type: "textarea",
            rows: 4,
            trim: true,
          },
        ]}
        transformSubmitValues={(values) => ({
          name: String(values.name ?? ""),
          code: String(values.code ?? ""),
          remark: String(values.remark ?? ""),
        })}
        labels={{
          createTitle: t("company.createTitle"),
          editTitle: t("company.editTitle"),
          create: t("company.create"),
          save: t("company.save"),
          saving: t("company.saving"),
          cancel: t("company.cancel"),
          loadingDetail: t("company.loadingDetail"),
          required: t("company.nameRequired"),
          invalidNumber: t("company.nameRequired"),
          minValue: () => t("company.nameRequired"),
          maxValue: () => t("company.nameRequired"),
        }}
        onOpenChange={setCreateOpen}
        onSubmit={handleCreateCompany}
      />
    </>
  )
}
