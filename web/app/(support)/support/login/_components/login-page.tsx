"use client"

// This file has been modified for the KestrelDesk project.

import Image from "next/image"
import Link from "next/link"
import { useEffect, useState, type ReactNode } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { KeyRoundIcon, Loader2Icon, TriangleAlertIcon } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { SupportPageContent, SupportPageShell } from "@/app/(support)/support/_components/support-page-shell"
import { useSupportAuth } from "@/app/(support)/support/_components/support-auth-provider"
import { SupportFormField as LabeledField } from "@/app/(support)/support/_components/support-ui"
import { getSupportLoginDestination } from "@/app/(support)/support/_components/support-community-route"
import { useI18n } from "@/i18n/provider"
import { fetchPublicConfig, type PublicConfig } from "@/lib/api/config"
import { loginSupportCustomer, registerSupportCustomer, requestPasswordReset, resetPassword } from "@/lib/api/support"
import { cn } from "@/lib/utils"
import { getSupportAccountLoginDestination } from "@/lib/identity-boundary.mjs"

function detectWxWorkEnvironment() {
  if (typeof navigator === "undefined") return false
  return navigator.userAgent.toLowerCase().includes("wxwork")
}

export function SupportLoginPage() {
  const t = useI18n()
  const router = useRouter()
  const searchParams = useSearchParams()
  const { ready, session } = useSupportAuth()
  const [mode, setMode] = useState<"login" | "register">("login")
  const [publicConfig, setPublicConfig] = useState<PublicConfig | null>(null)
  const [publicConfigError, setPublicConfigError] = useState("")
  const [isWxWorkEnv, setIsWxWorkEnv] = useState(false)
  const [name, setName] = useState("")
  const [account, setAccount] = useState("")
  const [registerEmail, setRegisterEmail] = useState("")
  const [password, setPassword] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const nextDestination = getSupportLoginDestination(searchParams.get("next"))
  const wxworkError = searchParams.get("wxworkError")
  const oidcError = searchParams.get("oidcError")
  const passwordLoginEnabled = publicConfig?.passwordLoginEnabled !== false
  const providerCount = Number(publicConfig?.wxworkEnabled) + Number(publicConfig?.oidcEnabled)
  const hasAnyLoginMethod = passwordLoginEnabled || providerCount > 0

  useEffect(() => {
    if (ready && session) {
      router.replace(getSupportAccountLoginDestination(session.user.userType, nextDestination))
    }
  }, [nextDestination, ready, router, session])

  useEffect(() => {
    if (!passwordLoginEnabled && mode === "register") setMode("login")
  }, [mode, passwordLoginEnabled])

  useEffect(() => {
    if (wxworkError) toast.error(wxworkError)
  }, [wxworkError])

  useEffect(() => {
    if (oidcError) toast.error(oidcError)
  }, [oidcError])

  useEffect(() => {
    setIsWxWorkEnv(detectWxWorkEnvironment())
  }, [])

  useEffect(() => {
    let cancelled = false

    void fetchPublicConfig()
      .then((config) => {
        if (cancelled) return
        setPublicConfig(config)
        setPublicConfigError("")
      })
      .catch((error) => {
        if (cancelled) return
        setPublicConfig(null)
        setPublicConfigError(error instanceof Error ? error.message : t("api.requestFailed"))
      })

    return () => {
      cancelled = true
    }
  }, [t])

  const submit = async () => {
    if (submitting || !passwordLoginEnabled) return
    setSubmitting(true)
    try {
      await (mode === "login"
        ? loginSupportCustomer({ email: account, password })
        : registerSupportCustomer({ name, email: registerEmail, password }))
      toast.success(t("supportPublic.toast.loggedIn"))
      router.replace(nextDestination)
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("auth.loginFailed"))
    } finally {
      setSubmitting(false)
    }
  }

  const startWxWorkLogin = () => {
    const path = isWxWorkEnv ? "/api/auth/wxwork_login" : "/api/auth/wxwork_qr_login"
    window.location.href = `${path}?next=${encodeURIComponent(nextDestination)}`
  }

  const startOIDCLogin = () => {
    window.location.href = `/api/auth/oidc_login?next=${encodeURIComponent(nextDestination)}`
  }

  return (
    <SupportPageShell section="login">
      <SupportPageContent className="flex min-h-[calc(100svh-3.5rem)] items-start justify-center py-8 sm:items-center sm:py-12">
        <section className="w-full max-w-[440px] rounded-md bg-card px-5 py-6 sm:px-7 sm:py-8">
          <div className="mb-6 flex flex-col items-center gap-3 text-center">
            <Image src="/images/logo.svg" alt="" width={56} height={56} className="size-14 opacity-80" priority />
            <h1 className="text-2xl font-semibold tracking-tight">{t("supportPublic.login.welcomeBack")}</h1>
          </div>
          {!publicConfig && !publicConfigError ? (
            <LoginState icon={<Loader2Icon className="size-5 animate-spin" />} title={t("auth.loadingOptions")} />
          ) : null}
          {publicConfigError ? (
            <LoginState icon={<TriangleAlertIcon className="size-5" />} title={t("auth.optionsLoadFailed")} description={publicConfigError} destructive />
          ) : null}
          {publicConfig && !hasAnyLoginMethod ? (
            <LoginState icon={<TriangleAlertIcon className="size-5" />} title={t("supportPublic.login.noMethodsTitle")} description={t("supportPublic.login.noMethodsDescription")} />
          ) : null}
          {publicConfig && hasAnyLoginMethod ? (
            <div className="grid gap-5">
              {passwordLoginEnabled ? (
                <form
                  className="grid gap-4"
                  onSubmit={(event) => {
                    event.preventDefault()
                    void submit()
                  }}
                >
                  {mode === "register" ? (
                    <LabeledField label={t("supportPublic.login.name")}>
                      <Input value={name} onChange={(event) => setName(event.target.value)} placeholder={t("supportPublic.login.namePlaceholder")} className="bg-card" />
                    </LabeledField>
                  ) : null}
                  <LabeledField label={mode === "login" ? t("supportPublic.login.account") : t("supportPublic.login.email")}>
                    <Input
                      value={mode === "login" ? account : registerEmail}
                      onChange={(event) => (mode === "login" ? setAccount(event.target.value) : setRegisterEmail(event.target.value))}
                      placeholder={mode === "login" ? t("supportPublic.login.accountPlaceholder") : t("supportPublic.login.emailPlaceholder")}
                      autoComplete={mode === "login" ? "username" : "email"}
                      className="bg-card"
                    />
                  </LabeledField>
                  <LabeledField label={t("supportPublic.login.password")}>
                    <Input type="password" value={password} onChange={(event) => setPassword(event.target.value)} placeholder={t("supportPublic.login.passwordPlaceholder")} autoComplete={mode === "login" ? "current-password" : "new-password"} className="bg-card" />
                  </LabeledField>
                  {mode === "login" ? (
                    <Link href="/support/forgot-password" className="justify-self-end text-sm text-muted-foreground underline-offset-2 hover:underline">
                      {t("auth.forgotPassword")}
                    </Link>
                  ) : null}
                  <Button type="submit" disabled={submitting}>
                    {submitting ? t("supportPublic.actions.processing") : mode === "login" ? t("supportPublic.login.loginAction") : t("supportPublic.login.registerAction")}
                  </Button>
                  <Button type="button" variant="ghost" onClick={() => setMode(mode === "login" ? "register" : "login")}>
                    {mode === "login" ? t("supportPublic.login.switchToRegister") : t("supportPublic.login.switchToLogin")}
                  </Button>
                </form>
              ) : null}
              {providerCount > 0 ? (
                <div className="grid gap-3">
                  {passwordLoginEnabled ? <div className="flex items-center gap-3 text-xs text-muted-foreground before:h-px before:flex-1 before:bg-border after:h-px after:flex-1 after:bg-border">{t("auth.continueWith")}</div> : null}
                  <div className="grid gap-3">
                    {publicConfig.wxworkEnabled ? (
                      <Button type="button" variant="outline" onClick={startWxWorkLogin} aria-label={t("auth.wxworkSignIn")}>
                        <Image src="/images/wxwork.svg" alt="" width={16} height={16} className="size-4 shrink-0" />
                        {t("auth.wxworkSignIn")}
                      </Button>
                    ) : null}
                    {publicConfig.oidcEnabled ? (
                      <Button type="button" variant="outline" onClick={startOIDCLogin} aria-label={t("auth.oidcSignIn")}>
                        <KeyRoundIcon className="size-4 shrink-0" />
                        {t("auth.oidcSignIn")}
                      </Button>
                    ) : null}
                  </div>
                </div>
              ) : null}
            </div>
          ) : null}
        </section>
      </SupportPageContent>
    </SupportPageShell>
  )
}

function LoginState({ icon, title, description, destructive }: { icon: ReactNode; title: string; description?: string; destructive?: boolean }) {
  return (
    <div className={cn("flex min-h-64 flex-col items-center justify-center gap-3 text-center", destructive ? "text-destructive" : "text-muted-foreground")}>
      {icon}
      <div className="grid gap-1">
        <p className="text-sm font-medium text-foreground">{title}</p>
        {description ? <p className="text-sm text-muted-foreground">{description}</p> : null}
      </div>
    </div>
  )
}

export function SupportForgotPasswordForm() {
  const t = useI18n()
  const [identifier, setIdentifier] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [submitted, setSubmitted] = useState(false)
  const [error, setError] = useState("")

  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const normalizedIdentifier = identifier.trim()
    if (!normalizedIdentifier) {
      setError(t("auth.passwordRecovery.identifierRequired"))
      return
    }
    setError("")
    setSubmitting(true)
    try {
      await requestPasswordReset({ identifier: normalizedIdentifier })
      setSubmitted(true)
    } catch (requestError) {
      setError(requestError instanceof Error ? requestError.message : t("auth.passwordRecovery.requestFailed"))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <SupportPageShell section="login">
      <SupportPageContent className="flex min-h-[calc(100svh-3.5rem)] items-start justify-center py-8 sm:items-center sm:py-12">
        <section className="w-full max-w-[440px] rounded-md bg-card px-5 py-6 sm:px-7 sm:py-8">
          {submitted ? (
            <div className="grid gap-5 text-center" role="status">
              <div className="grid gap-2">
                <h1 className="text-2xl font-semibold tracking-tight">{t("auth.passwordRecovery.requestTitle")}</h1>
                <p className="text-sm text-muted-foreground">{t("auth.passwordRecovery.requestConfirmation")}</p>
              </div>
              <Link href="/support/login" className="text-sm underline-offset-2 hover:underline">
                {t("auth.passwordRecovery.returnToLogin")}
              </Link>
            </div>
          ) : (
            <div className="grid gap-5">
              <div className="grid gap-2 text-center">
                <h1 className="text-2xl font-semibold tracking-tight">{t("auth.passwordRecovery.requestTitle")}</h1>
                <p className="text-sm text-muted-foreground">{t("auth.passwordRecovery.requestDescription")}</p>
              </div>
              <form className="grid gap-4" onSubmit={(event) => void submit(event)}>
                <LabeledField label={t("auth.passwordRecovery.identifier")}>
                  <Input
                    name="identifier"
                    value={identifier}
                    onChange={(event) => setIdentifier(event.target.value)}
                    autoComplete="username"
                    className="bg-card"
                    required
                  />
                </LabeledField>
                {error ? <p className="text-sm text-destructive" role="alert">{error}</p> : null}
                <Button type="submit" disabled={submitting}>
                  {submitting ? t("auth.passwordRecovery.requesting") : t("auth.passwordRecovery.requestAction")}
                </Button>
                <Link href="/support/login" className="text-center text-sm text-muted-foreground underline-offset-2 hover:underline">
                  {t("auth.passwordRecovery.returnToLogin")}
                </Link>
              </form>
            </div>
          )}
        </section>
      </SupportPageContent>
    </SupportPageShell>
  )
}

export function SupportResetPasswordForm() {
  const t = useI18n()
  const router = useRouter()
  const searchParams = useSearchParams()
  const token = searchParams.get("token") ?? ""
  const [newPassword, setNewPassword] = useState("")
  const [confirmPassword, setConfirmPassword] = useState("")
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState("")

  const submit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setError("")
    if (!token.trim()) {
      setError(t("auth.passwordRecovery.invalidToken"))
      return
    }
    if (!newPassword.trim()) {
      setError(t("auth.passwordRecovery.passwordRequired"))
      return
    }
    if (!confirmPassword.trim()) {
      setError(t("auth.passwordRecovery.confirmationRequired"))
      return
    }
    if (newPassword.length < 8) {
      setError(t("auth.passwordRecovery.passwordTooShort"))
      return
    }
    if (newPassword !== confirmPassword) {
      setError(t("auth.passwordRecovery.passwordMismatch"))
      return
    }

    setSubmitting(true)
    try {
      await resetPassword({ token, newPassword, confirmPassword })
      toast.success(t("auth.passwordRecovery.resetSuccess"))
      router.replace("/support/login")
    } catch (resetError) {
      setError(resetError instanceof Error ? resetError.message : t("auth.passwordRecovery.resetFailed"))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <SupportPageShell section="login">
      <SupportPageContent className="flex min-h-[calc(100svh-3.5rem)] items-start justify-center py-8 sm:items-center sm:py-12">
        <section className="w-full max-w-[440px] rounded-md bg-card px-5 py-6 sm:px-7 sm:py-8">
          <div className="grid gap-5">
            <div className="grid gap-2 text-center">
              <h1 className="text-2xl font-semibold tracking-tight">{t("auth.passwordRecovery.resetTitle")}</h1>
              <p className="text-sm text-muted-foreground">{t("auth.passwordRecovery.resetDescription")}</p>
            </div>
            <form className="grid gap-4" onSubmit={(event) => void submit(event)}>
              <LabeledField label={t("auth.passwordRecovery.newPassword")}>
                <Input
                  name="newPassword"
                  type="password"
                  value={newPassword}
                  onChange={(event) => setNewPassword(event.target.value)}
                  autoComplete="new-password"
                  className="bg-card"
                  minLength={8}
                  required
                />
              </LabeledField>
              <LabeledField label={t("auth.passwordRecovery.confirmPassword")}>
                <Input
                  name="confirmPassword"
                  type="password"
                  value={confirmPassword}
                  onChange={(event) => setConfirmPassword(event.target.value)}
                  autoComplete="new-password"
                  className="bg-card"
                  required
                />
              </LabeledField>
              {error ? <p className="text-sm text-destructive" role="alert">{error}</p> : null}
              <Button type="submit" disabled={submitting}>
                {submitting ? t("auth.passwordRecovery.resetting") : t("auth.passwordRecovery.resetAction")}
              </Button>
              <Link href="/support/login" className="text-center text-sm text-muted-foreground underline-offset-2 hover:underline">
                {t("auth.passwordRecovery.returnToLogin")}
              </Link>
            </form>
          </div>
        </section>
      </SupportPageContent>
    </SupportPageShell>
  )
}
