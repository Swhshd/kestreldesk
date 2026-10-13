"use client"

import { useRef } from "react"
import {
  BoldIcon,
  CodeIcon,
  Heading1Icon,
  ItalicIcon,
  LinkIcon,
  ListIcon,
  ListOrderedIcon,
  QuoteIcon,
} from "lucide-react"

import { Textarea } from "@/components/ui/textarea"

import { EditorToolbar } from "./toolbar"
import type { BaseEditorProps } from "./types"
import { useI18n } from "@/i18n/provider"

export type MarkdownEditorProps = BaseEditorProps & {
  value: string
  onChange: (nextValue: string) => void
  rows?: number
}

export function MarkdownEditor({
  value,
  onChange,
  placeholder = ".",
  disabled = false,
  rows = 16,
  className,
}: MarkdownEditorProps) {
  const t = useI18n()
  const textareaRef = useRef<HTMLTextAreaElement | null>(null)

  const handleWrapSelection = (prefix: string, suffix = prefix) => {
    const textarea = textareaRef.current
    if (!textarea || disabled) {
      return
    }
    const start = textarea.selectionStart ?? 0
    const end = textarea.selectionEnd ?? 0
    const selected = value.slice(start, end)
    const next = `${value.slice(0, start)}${prefix}${selected}${suffix}${value.slice(end)}`
    onChange(next)
    requestAnimationFrame(() => {
      textarea.focus()
      textarea.setSelectionRange(start + prefix.length, end + prefix.length)
    })
  }

  const handleInsertLinePrefix = (prefix: string) => {
    const textarea = textareaRef.current
    if (!textarea || disabled) {
      return
    }
    const start = textarea.selectionStart ?? 0
    const end = textarea.selectionEnd ?? 0
    const lineStart = value.lastIndexOf("\n", start - 1) + 1
    const lineEndRaw = value.indexOf("\n", end)
    const lineEnd = lineEndRaw === -1 ? value.length : lineEndRaw
    const selectedLines = value.slice(lineStart, lineEnd)
    const nextLines = selectedLines
      .split("\n")
      .map((line) => `${prefix}${line}`)
      .join("\n")
    const next = `${value.slice(0, lineStart)}${nextLines}${value.slice(lineEnd)}`
    onChange(next)
    requestAnimationFrame(() => {
      textarea.focus()
      textarea.setSelectionRange(lineStart, lineStart + nextLines.length)
    })
  }

  const handleInsertLink = () => {
    const textarea = textareaRef.current
    if (!textarea || disabled) {
      return
    }
    const start = textarea.selectionStart ?? 0
    const end = textarea.selectionEnd ?? 0
    const selected = value.slice(start, end) || t("editor.linkText")
    const markdown = `[${selected}](https://)`
    const next = `${value.slice(0, start)}${markdown}${value.slice(end)}`
    onChange(next)
    requestAnimationFrame(() => {
      textarea.focus()
      const urlStart = start + markdown.lastIndexOf("https://")
      textarea.setSelectionRange(urlStart, urlStart + "https://".length)
    })
  }

  const toolbarActions = [
    {
      key: "heading1",
      label: t("editor.heading1"),
      icon: Heading1Icon,
      disabled,
      onClick: () => handleInsertLinePrefix("# "),
    },
    { key: "separator-1", type: "separator" as const },
    {
      key: "bold",
      label: t("editor.bold"),
      icon: BoldIcon,
      disabled,
      onClick: () => handleWrapSelection("**"),
    },
    {
      key: "italic",
      label: t("editor.italic"),
      icon: ItalicIcon,
      disabled,
      onClick: () => handleWrapSelection("*"),
    },
    {
      key: "code",
      label: t("editor.inlineCode"),
      icon: CodeIcon,
      disabled,
      onClick: () => handleWrapSelection("`"),
    },
    { key: "separator-2", type: "separator" as const },
    {
      key: "bulletList",
      label: t("editor.bulletList"),
      icon: ListIcon,
      disabled,
      onClick: () => handleInsertLinePrefix("- "),
    },
    {
      key: "orderedList",
      label: t("editor.orderedList"),
      icon: ListOrderedIcon,
      disabled,
      onClick: () => handleInsertLinePrefix("1. "),
    },
    {
      key: "blockquote",
      label: t("editor.quote"),
      icon: QuoteIcon,
      disabled,
      onClick: () => handleInsertLinePrefix("> "),
    },
    {
      key: "link",
      label: t("editor.link"),
      icon: LinkIcon,
      disabled,
      onClick: handleInsertLink,
    },
  ] as const

  return (
    <div className="rounded-lg border bg-background">
      <EditorToolbar actions={toolbarActions} />
      <div className="p-2">
        <Textarea
          ref={textareaRef}
          value={value}
          rows={rows}
          disabled={disabled}
          placeholder={placeholder}
          className={`min-h-64 max-h-96 resize-y border-0 px-2 py-2 text-sm leading-7 shadow-none focus-visible:ring-0 ${className ?? ""}`}
          onChange={(event) => onChange(event.target.value)}
        />
      </div>
    </div>
  )
}
