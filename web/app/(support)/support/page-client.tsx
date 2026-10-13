"use client"

// This file has been modified for the KestrelDesk project.

import { useEffect, useState, type ReactNode } from "react"
import Link from "next/link"
import { ArrowRightIcon, BookOpenIcon, CircleHelpIcon, HeadphonesIcon } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { buttonVariants } from "@/components/ui/button"
import { SupportPageContent, SupportPageShell } from "@/app/(support)/support/_components/support-page-shell"
import { SupportSearchInput } from "@/app/(support)/support/_components/support-ui"
import { useI18n } from "@/i18n/provider"
import { postsHref } from "@/lib/api/support-community"
import { fetchSupportConfig } from "@/lib/api/support-config"
import { getSupportConsultationHref } from "@/lib/support/support-consultation-entry"
import { cn } from "@/lib/utils"

export function SupportCenterHome() {
  const t = useI18n()
  const [query, setQuery] = useState("")
  const [consultationHref, setConsultationHref] = useState<string | null | undefined>(undefined)

  useEffect(() => {
    void fetchSupportConfig()
      .then((config) => setConsultationHref(getSupportConsultationHref(config.defaultWebChannelId)))
      .catch(() => setConsultationHref(null))
  }, [])

  return (
    <SupportPageShell>
      <section className="kestreldesk-support-hero relative border-y border-border px-5 py-12 sm:px-8 sm:py-18">
        <div className="relative mx-auto max-w-3xl text-center">
          <Badge variant="secondary" className="mb-5 bg-white/70 px-3 py-1 text-primary dark:bg-card/80">
            {t("supportPublic.home.badge")}
          </Badge>
          <h1 className="text-balance text-3xl font-semibold tracking-tight sm:text-5xl">
            {t("supportPublic.home.title")}
          </h1>
          <p className="mx-auto mt-4 max-w-xl text-pretty text-sm leading-6 text-muted-foreground sm:text-base">
            {t("supportPublic.home.description")}
          </p>
          <div className="relative mx-auto mt-8 flex max-w-2xl flex-col gap-2 sm:flex-row">
            <SupportSearchInput
              value={query}
              onChange={setQuery}
              placeholder={t("supportPublic.home.searchPlaceholder")}
              hero
            />
            <Link
              className={cn(buttonVariants({ size: "lg" }), "h-13 rounded-md px-6")}
              href={`${postsHref()}${query ? `?title=${encodeURIComponent(query)}` : ""}`}
            >
              {t("supportPublic.actions.search")}
            </Link>
          </div>
        </div>
      </section>

      <SupportPageContent className="py-10 sm:py-14">
        <section className="grid gap-3 sm:grid-cols-3" aria-label={t("supportPublic.nav.siteNavigation")}>
          <SupportEntryCard
            href="/support/docs"
            icon={<BookOpenIcon />}
            title={t("supportPublic.home.helpTitle")}
            description={t("supportPublic.home.helpDescription")}
            accent="sky"
          />
          <SupportEntryCard
            href={postsHref()}
            icon={<CircleHelpIcon />}
            title={t("supportPublic.home.postsTitle")}
            description={t("supportPublic.home.postsDescription")}
            accent="violet"
          />
          <SupportEntryCard
            href={consultationHref ?? undefined}
            icon={<HeadphonesIcon />}
            title={t("supportPublic.home.chatTitle")}
            description={consultationHref === undefined
              ? t("common.loading")
              : consultationHref
                ? t("supportPublic.home.chatDescription")
                : t("supportPublic.home.chatUnavailable")}
            accent="emerald"
          />
        </section>
      </SupportPageContent>
    </SupportPageShell>
  )
}

function SupportEntryCard({
  href,
  icon,
  title,
  description,
  accent,
}: {
  href?: string
  icon: ReactNode
  title: string
  description: string
  accent: "sky" | "violet" | "emerald"
}) {
  const accentClass = {
    sky: "bg-sky-500/10 text-sky-600 dark:text-sky-400",
    violet: "bg-violet-500/10 text-violet-600 dark:text-violet-400",
    emerald: "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
  }[accent]

  const content = (
    <>
      <div className="flex items-start justify-between gap-4">
        <span className={cn("grid size-10 place-items-center rounded-md [&_svg]:size-5", accentClass)}>{icon}</span>
        {href ? <ArrowRightIcon className="mt-1 size-4 text-muted-foreground transition-transform group-hover:translate-x-0.5 group-hover:text-primary" /> : null}
      </div>
      <h3 className="mt-5 font-medium">{title}</h3>
      <p className="mt-1 text-sm leading-6 text-muted-foreground">{description}</p>
    </>
  )
  const className = "rounded-md border border-border p-5 text-left"

  if (!href) {
    return <div aria-disabled="true" className={cn(className, "bg-muted/40")}>{content}</div>
  }

  return (
    <Link
      href={href}
      className={cn(className, "group bg-card transition-colors duration-150 hover:border-primary/50 hover:bg-accent/30 focus-visible:ring-3 focus-visible:ring-ring")}
    >
      {content}
    </Link>
  )
}
