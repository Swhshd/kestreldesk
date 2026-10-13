"use client"

import { useEffect, useState } from "react"
import { toast } from "sonner"

import { CustomerForm, type CustomerFormSavePayload } from "@/components/customer-form"
import { ProjectDialog } from "@/components/project-dialog"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { linkConversationToCustomer } from "@/lib/api/agent"
import { fetchCustomers, saveCustomerProfile, type AdminCustomer } from "@/lib/api/customer"
import { linkTicketToCustomer } from "@/lib/api/ticket"
import { useI18n } from "@/i18n/provider"

export type CustomerLinkOrCreateDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** When present, links an existing or newly created customer to this conversation. */
  conversationId?: number | null
  /** When present, links an existing or newly created customer to this ticket. */
  ticketId?: number | null
  /** Called after linking succeeds, or after creating without a linked context. */
  onSuccess?: () => void | Promise<void>
}

const createFormId = "customer-link-or-create-form"

export function CustomerLinkOrCreateDialog({
  open,
  onOpenChange,
  conversationId,
  ticketId,
  onSuccess,
}: CustomerLinkOrCreateDialogProps) {
  const t = useI18n()
  const [searchText, setSearchText] = useState("")
  const [searching, setSearching] = useState(false)
  const [results, setResults] = useState<AdminCustomer[]>([])
  const [showCreate, setShowCreate] = useState(false)
  const [linkingId, setLinkingId] = useState<number | null>(null)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!open) {
      return
    }
    setSearchText("")
    setResults([])
    setShowCreate(false)
    setLinkingId(null)
  }, [open])

  const runSearch = async () => {
    const q = searchText.trim()
    if (!q) {
      toast.error(t("customerLink.keywordRequired"))
      return
    }
    setSearching(true)
    try {
      const data = await fetchCustomers({
        keyword: q,
        page: 1,
        limit: 50,
        status: 0,
      })
      setResults(data.results)
      if (data.results.length === 0) {
        toast.message(t("customerLink.noMatch"))
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("customerLink.searchFailed"))
    } finally {
      setSearching(false)
    }
  }

  const handleLinkExisting = async (customer: AdminCustomer) => {
    const customerName = customer.name || t("customerLink.fallbackName", { id: customer.id })
    if (!conversationId && !ticketId) {
      toast.success(t("customerLink.selected", { name: customerName }))
      onOpenChange(false)
      await onSuccess?.()
      return
    }
    setLinkingId(customer.id)
    try {
      if (conversationId) {
        await linkConversationToCustomer({
          conversationId,
          customerId: customer.id,
        })
      }
      if (ticketId) {
        await linkTicketToCustomer({
          ticketId,
          customerId: customer.id,
        })
      }
      toast.success(t("customerLink.linked"))
      onOpenChange(false)
      await onSuccess?.()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("customerLink.linkFailed"))
    } finally {
      setLinkingId(null)
    }
  }

  const onCreateSave = async (payload: CustomerFormSavePayload) => {
    setSaving(true)
    try {
      const created = await saveCustomerProfile(payload)
      if (conversationId) {
        await linkConversationToCustomer({
          conversationId,
          customerId: created.id,
        })
      }
      if (ticketId) {
        await linkTicketToCustomer({
          ticketId,
          customerId: created.id,
        })
      }
      if (conversationId || ticketId) {
        toast.success(
          conversationId
            ? t("customerLink.createdAndLinkedConversation")
            : t("customerLink.createdAndLinkedTicket")
        )
      } else {
        toast.success(t("customerLink.created"))
      }
      onOpenChange(false)
      await onSuccess?.()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("customerLink.saveFailed"))
    } finally {
      setSaving(false)
    }
  }

  const description = (
    <>
      {t("customerLink.descriptionPrefix")}
      {conversationId || ticketId
        ? conversationId
          ? t("customerLink.descriptionLinkConversation")
          : t("customerLink.descriptionLinkTicket")
        : t("customerLink.descriptionNoContext")}
      {t("customerLink.descriptionCreatePrefix")}
      {conversationId || ticketId
        ? conversationId
          ? t("customerLink.descriptionCreateConversation")
          : t("customerLink.descriptionCreateTicket")
        : t("customerLink.descriptionCreateNoContext")}
    </>
  )

  return (
    <ProjectDialog
      open={open}
      onOpenChange={(nextOpen) => onOpenChange(nextOpen)}
      title={t("customerLink.title")}
      description={description}
      allowFullscreen
      size="xl"
      footer={
        <div className="flex w-full flex-wrap items-center justify-end gap-2">
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
          >
            {t("customerLink.close")}
          </Button>
          {showCreate ? (
            <Button type="submit" form={createFormId} disabled={saving}>
              {saving
                ? t("customerLink.submitting")
                : conversationId
                  ? t("customerLink.createAndLinkConversation")
                  : ticketId
                    ? t("customerLink.createAndLinkTicket")
                    : t("customerLink.createCustomer")}
            </Button>
          ) : null}
        </div>
      }
    >
      <div className="flex flex-col gap-4">
        <div className="flex gap-2">
          <Input
            placeholder={t("customerLink.searchPlaceholder")}
            value={searchText}
            onChange={(e) => setSearchText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                void runSearch();
              }
            }}
          />
          <Button
            type="button"
            variant="secondary"
            disabled={searching}
            onClick={() => void runSearch()}
          >
            {searching ? t("customerLink.searching") : t("customerLink.search")}
          </Button>
        </div>

        {results.length > 0 ? (
          <ul className="max-h-48 space-y-1.5 overflow-y-auto rounded-md border border-border p-2 text-sm">
            {results.map((row) => (
              <li
                key={row.id}
                className="flex items-center justify-between gap-2 rounded border border-transparent px-2 py-1.5 hover:bg-muted/40"
              >
                <div className="min-w-0">
                  <div className="truncate font-medium flex items-center gap-2">
                    <span>{row.name || t("customerLink.fallbackName", { id: row.id })}</span>
                    <span className="text-muted-foreground">
                      {row.primaryMobile}
                    </span>
                    <span className="text-muted-foreground">
                      {row.primaryEmail}
                    </span>
                  </div>
                  {row.company?.name ? (
                    <div className="truncate text-muted-foreground text-xs">
                      {row.company.name}
                    </div>
                  ) : null}
                </div>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  className="shrink-0"
                  disabled={linkingId !== null}
                  onClick={() => void handleLinkExisting(row)}
                >
                  {linkingId === row.id
                    ? t("customerLink.processing")
                    : conversationId
                      ? t("customerLink.link")
                      : t("customerLink.select")}
                </Button>
              </li>
            ))}
          </ul>
        ) : null}

        <div className="border-t border-border pt-2">
          <button
            type="button"
            className="text-sm text-primary underline-offset-4 hover:underline"
            onClick={() => setShowCreate((v) => !v)}
          >
            {showCreate ? t("customerLink.collapseCreate") : t("customerLink.showCreate")}
          </button>
        </div>

        {showCreate ? (
          <CustomerForm
            formId={createFormId}
            onSave={onCreateSave}
            fieldIdPrefix="link-or-create"
            remarkRows={2}
            className="flex flex-col gap-3 rounded-lg border border-border bg-muted/10 p-3"
          />
        ) : null}
      </div>
    </ProjectDialog>
  );
}
