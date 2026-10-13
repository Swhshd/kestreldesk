type LoginBrandPanelProps = {
  brand: string
  title: string
  description: string
}

export function LoginBrandPanel({ brand, title, description }: LoginBrandPanelProps) {
  return (
    <div className="kestreldesk-brand-panel flex flex-col justify-center gap-5 border-b border-border p-6 md:border-r md:border-b-0 md:p-10">
      <div className="relative flex items-center gap-3">
        {/* The adjacent wordmark provides the accessible brand name. */}
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img src="/images/logo.svg" alt="" width="48" height="48" className="size-12 shrink-0" />
        <h2 className="text-2xl font-semibold tracking-tight md:text-3xl">{brand}</h2>
      </div>
      <div className="relative space-y-3">
        <p className="text-sm font-medium text-(--brand-panel-foreground)">{title}</p>
        <p className="max-w-sm text-sm leading-6 text-(--brand-panel-muted)">{description}</p>
      </div>
    </div>
  )
}
