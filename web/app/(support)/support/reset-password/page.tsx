import type { Metadata } from "next"
import { Suspense } from "react"

import { SupportResetPasswordForm } from "@/app/(support)/support/login/_components/login-page"

export const metadata: Metadata = {
  referrer: "no-referrer",
}

export default function SupportResetPasswordPage() {
  return (
    <Suspense fallback={<div className="min-h-[calc(100svh-3.5rem)]" />}>
      <SupportResetPasswordForm />
    </Suspense>
  )
}
