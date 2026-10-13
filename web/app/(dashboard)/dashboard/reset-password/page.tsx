import type { Metadata } from "next"
import { Suspense } from "react"

import { DashboardResetPasswordForm } from "@/components/login-form"

export const metadata: Metadata = {
  referrer: "no-referrer",
}

export default function DashboardResetPasswordPage() {
  return (
    <Suspense fallback={<div className="min-h-96" />}>
      <DashboardResetPasswordForm />
    </Suspense>
  )
}
