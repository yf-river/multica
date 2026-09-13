// 本文件提供代码块组件：用 Shiki 做语法高亮，支持 terminal/minimal/full 三种模式、语言别名、高亮结果缓存与复制按钮，另含行内代码 InlineCode。
import * as React from 'react'
import { codeToHtml, bundledLanguages, type BundledLanguage } from 'shiki'
import { Copy, Check } from "lucide-react"
import { useTranslation } from "react-i18next"
import { Button } from "@multica/ui/components/ui/button"
import { Tooltip, TooltipTrigger, TooltipContent } from "@multica/ui/components/ui/tooltip"
import { cn } from '@multica/ui/lib/utils'
import { copyText } from '../lib/clipboard'
import {
  CODE_LIGATURE_CLASS,
  CODE_LIGATURE_DESCENDANT_CLASS,
} from '@multica/ui/lib/code-style'

/**
 * 代码块组件的 props 定义。
 */
export interface CodeBlockProps {
  /** 要显示的代码文本 */
  code: string
  /** 编程语言标识 */
  language?: string
  /** 外层 class 名 */
  className?: string
  /**
   * 渲染模式会影响代码块样式：
   * - 'terminal'：最简，保留控制字符原样
   * - 'minimal'：干净代码，基础样式
   * - 'full'：丰富样式，带背景、复制按钮等
   */
  mode?: 'terminal' | 'minimal' | 'full'
}

// 把常见语言别名映射到 Shiki 的语言名
const LANGUAGE_ALIASES: Record<string, BundledLanguage> = {
  js: 'javascript',
  ts: 'typescript',
  py: 'python',
  sh: 'bash',
  zsh: 'bash',
  yml: 'yaml',
  rb: 'ruby',
  rs: 'rust',
  kt: 'kotlin',
  'objective-c': 'objc',
  objc: 'objc'
}

// 高亮结果的简单 LRU 缓存
const highlightCache = new Map<string, string>()
const CACHE_MAX_SIZE = 200

/** 生成缓存键：语言 + 代码内容 */
function getCacheKey(code: string, lang: string): string {
  return `${lang}:${code}`
}

/** 判断语言是否合法（先查别名，再看是否为 Shiki 内置语言） */
function isValidLanguage(lang: string): lang is BundledLanguage {
  const normalized = LANGUAGE_ALIASES[lang] || lang
  return normalized in bundledLanguages
}

/**
 * CodeBlock：用 Shiki 做语法高亮的代码块。
 *
 * 用 Shiki 双主题 + CSS 变量实现明暗切换。不需要 JS 检测暗色模式——主题切换
 * 完全交给 CSS 处理（见 globals.css 里的 .shiki/.dark .shiki 规则）。
 *
 * @see https://shiki.style/guide/dual-themes
 *
 * @param props 见 CodeBlockProps 定义
 * @returns 高亮后的代码块元素
 */
export function CodeBlock({
  code,
  language = 'text',
  className,
  mode = 'full'
}: CodeBlockProps): React.JSX.Element {
  const { t } = useTranslation("ui")
  const [highlighted, setHighlighted] = React.useState<string | null>(null)
  const [isLoading, setIsLoading] = React.useState(true)
  const [copied, setCopied] = React.useState(false)

  // 解析语言别名——保留为 string 以便回退到 'text'
  const langLower = language.toLowerCase()
  const resolvedLang: string = LANGUAGE_ALIASES[langLower] || langLower

  React.useEffect(() => {
    let cancelled = false

    async function highlight(): Promise<void> {
      const cacheKey = getCacheKey(code, resolvedLang)

      const cached = highlightCache.get(cacheKey)
      if (cached) {
        if (!cancelled) {
          setHighlighted(cached)
          setIsLoading(false)
        }
        return
      }

      try {
        // 用合法语言，否则回退到纯文本
        const lang = isValidLanguage(resolvedLang) ? resolvedLang : 'text'

        // 双主题：Shiki 一次输出两个主题对应的 CSS 变量。
        // 明暗切换由 CSS 通过 .dark 选择器处理（见 globals.css）。
        const html = await codeToHtml(code, {
          lang,
          themes: {
            light: 'github-light',
            dark: 'github-dark',
          },
          defaultColor: false,
        })

        // 缓存结果；超限时淘汰最早插入的一项（近似 LRU）
        if (highlightCache.size >= CACHE_MAX_SIZE) {
          const firstKey = highlightCache.keys().next().value
          if (firstKey) highlightCache.delete(firstKey)
        }
        highlightCache.set(cacheKey, html)

        if (!cancelled) {
          setHighlighted(html)
          setIsLoading(false)
        }
      } catch (error) {
        // 出错时回退到纯文本
        console.warn(`Shiki highlighting failed for language "${resolvedLang}":`, error)
        if (!cancelled) {
          setHighlighted(null)
          setIsLoading(false)
        }
      }
    }

    highlight()

    return () => {
      cancelled = true
    }
  }, [code, resolvedLang])

  const handleCopy = React.useCallback(async () => {
    if (await copyText(code)) {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }
  }, [code])

  // terminal 模式：原始等宽文本，最简样式
  if (mode === 'terminal') {
    return (
      <pre className={cn('font-mono text-body whitespace-pre-wrap', CODE_LIGATURE_CLASS, className)}>
        <code className={cn('font-mono', CODE_LIGATURE_CLASS)}>{code}</code>
      </pre>
    )
  }

  // minimal 模式：只要语法高亮，不加额外装饰
  if (mode === 'minimal') {
    if (isLoading || !highlighted) {
      return (
        <pre className={cn('font-mono text-body whitespace-pre-wrap', CODE_LIGATURE_CLASS, className)}>
          <code className={cn('font-mono', CODE_LIGATURE_CLASS)}>{code}</code>
        </pre>
      )
    }

    return (
      <div
        className={cn(
          'font-mono text-body [&_pre]:!bg-transparent [&_pre]:!p-0 [&_pre]:whitespace-pre-wrap [&_pre]:break-all [&_code]:!bg-transparent [&_code]:font-mono [&_pre]:font-mono',
          CODE_LIGATURE_CLASS,
          CODE_LIGATURE_DESCENDANT_CLASS,
          className
        )}
        dangerouslySetInnerHTML={{ __html: highlighted }}
      />
    )
  }

  // full 模式：丰富样式，带头部栏和复制按钮
  return (
    <div
      className={cn(
        'relative group rounded-lg overflow-hidden border bg-muted/30 mb-4 last:mb-0',
        className
      )}
    >
      {/* 语言标签 + 复制按钮 */}
      <div className="flex items-center justify-between px-3 py-1.5 bg-muted/50 border-b text-caption">
        <span className="text-muted-foreground font-medium uppercase tracking-wide">
          {resolvedLang !== 'text' ? resolvedLang : t(($) => $.plain_text)}
        </span>
        <Tooltip>
          <TooltipTrigger
            render={
              <Button
                variant="ghost"
                size="icon-xs"
                onClick={handleCopy}
                className="opacity-0 group-hover:opacity-100 transition-opacity text-muted-foreground hover:text-foreground"
                aria-label={t(($) => $.copy_code)}
              >
                {copied ? (
                  <Check className="size-3.5 text-success" />
                ) : (
                  <Copy className="size-3.5" />
                )}
              </Button>
            }
          />
          <TooltipContent>{t(($) => $.copy_code)}</TooltipContent>
        </Tooltip>
      </div>

      {/* 代码内容 */}
      <div className="p-3 overflow-x-auto">
        {isLoading || !highlighted ? (
          <pre className={cn('font-mono text-body whitespace-pre-wrap break-all', CODE_LIGATURE_CLASS)}>
            <code className={cn('font-mono', CODE_LIGATURE_CLASS)}>{code}</code>
          </pre>
        ) : (
          <div
            className={cn(
              'font-mono text-body [&_pre]:!bg-transparent [&_pre]:!m-0 [&_pre]:!p-0 [&_pre]:whitespace-pre-wrap [&_pre]:break-all [&_code]:!bg-transparent [&_code]:font-mono [&_pre]:font-mono',
              CODE_LIGATURE_CLASS,
              CODE_LIGATURE_DESCENDANT_CLASS
            )}
            dangerouslySetInnerHTML={{ __html: highlighted }}
          />
        )}
      </div>
    </div>
  )
}

/**
 * InlineCode：带样式的行内代码片段。
 * 特点：淡背景（3%）、淡边框（5%）。
 *
 * @param props 包含 children 与可选 className
 * @returns 行内 code 元素
 */
export function InlineCode({
  children,
  className
}: {
  children: React.ReactNode
  className?: string
}): React.JSX.Element {
  return (
    <code
      className={cn(
        'px-1.5 py-0.5 rounded bg-foreground/[0.03] border border-foreground/[0.05] font-mono text-body text-foreground',
        CODE_LIGATURE_CLASS,
        className
      )}
    >
      {children}
    </code>
  )
}
