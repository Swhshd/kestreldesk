"use client"

import { useEffect, useMemo } from "react"

import { cn } from "@/lib/utils"
import { useI18n } from "@/i18n/provider"

type JsonCodeEditorProps = {
  value: string
  onChange: (value: string) => void
  onValidationChange?: (error: string | null) => void
  disabled?: boolean
  className?: string
}

function validateJson(value: string, fallbackMessage: string) {
  const text = value.trim()
  if (!text) {
    return null
  }
  try {
    JSON.parse(text)
    return null
  } catch (error) {
    return error instanceof Error ? error.message : fallbackMessage
  }
}

export function JsonCodeEditor({
  value,
  onChange,
  onValidationChange,
  disabled = false,
  className,
}: JsonCodeEditorProps) {
  const t = useI18n()
  const error = useMemo(() => validateJson(value, t("json.invalid")), [t, value])
  const lineCount = Math.max(1, value.split("\n").length)

  useEffect(() => {
    onValidationChange?.(error)
  }, [error, onValidationChange])

  return (
    <div className={cn("rounded-lg border bg-slate-950", className)}>
      <div className="flex items-center justify-between border-b border-slate-800 px-3 py-2">
        <span className="font-mono text-[11px] uppercase tracking-[0.2em] text-slate-400">JSON</span>
        <span
          className={cn(
            "text-xs",
            error ? "text-rose-300" : "text-emerald-300"
          )}
        >
          {error ? t("json.invalidLabel") : t("json.valid")}
        </span>
      </div>
      <div className="flex min-h-52">
        <div className="select-none border-r border-slate-800 bg-slate-900/70 px-3 py-3 font-mono text-xs leading-6 text-slate-500">
          {Array.from({ length: lineCount }, (_, index) => (
            <div key={index + 1}>{index + 1}</div>
          ))}
        </div>
        <textarea
          value={value}
          onChange={(event) => onChange(event.target.value)}
          disabled={disabled}
          spellCheck={false}
          className="min-h-52 flex-1 resize-y bg-transparent px-4 py-3 font-mono text-sm leading-6 text-slate-100 outline-none placeholder:text-slate-500 disabled:cursor-not-allowed disabled:opacity-60"
          placeholder={`{\n  "key": "value"\n}`}
        />
      </div>
      <div className="border-t border-slate-800 px-3 py-2 text-xs text-slate-400">
        {error || t("json.readyHint")}
      </div>
    </div>
  )
}
