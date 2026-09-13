// 本文件是 Markdown 渲染核心：基于 react-markdown 提供 terminal/minimal/full 三种渲染模式，支持代码高亮、GFM、可点击链接/文件路径、mention 渲染与流式记忆化优化。
import * as React from 'react'
import ReactMarkdown, { type Components } from 'react-markdown'
import rehypeKatex from 'rehype-katex'
import rehypeRaw from 'rehype-raw'
import rehypeSanitize from 'rehype-sanitize'
import remarkBreaks from 'remark-breaks'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import { FileText, Download } from 'lucide-react'
import { cn } from '@multica/ui/lib/utils'
import { CODE_LIGATURE_CLASS } from '@multica/ui/lib/code-style'
import { CodeBlock, InlineCode } from './CodeBlock'
import { isAllowedFileCardHref, preprocessFileCards } from './file-cards'
import { preprocessLinks } from './linkify'
import { preprocessIssueIdentifiers } from './issue-identifiers'
import { preprocessMentionShortcodes } from './mentions'
import { markdownSanitizeSchema, markdownUrlTransform } from './sanitize'
import 'katex/dist/katex.min.css'
import './markdown.css'

/**
 * Markdown 的渲染模式，控制排版精细程度：
 *
 * - 'terminal'：原始输出、最少格式化，保留控制字符原样
 *   适合：调试输出、原始日志，想看清到底有什么内容时用
 *
 * - 'minimal'：干净的渲染，带语法高亮但不加多余装饰
 *   适合：聊天消息、行内内容，既要可读又不想太花哨
 *
 * - 'full'：丰富的渲染，漂亮的表格、带样式的代码块、讲究的排版
 *   适合：文档、长文内容，注重观感时用
 */
export type RenderMode = 'terminal' | 'minimal' | 'full'

/**
 * Markdown 组件的 props 定义。
 */
export interface MarkdownProps {
  /** 要渲染的 markdown 原始文本 */
  children: string
  /**
   * 渲染模式，控制格式化程度
   * @default 'minimal'
   */
  mode?: RenderMode
  /** 额外的外层 class 名 */
  className?: string
  /**
   * 用于记忆化的消息 ID（可选）。
   * 传入后会把已解析的块缓存起来，避免流式输出时反复解析。
   */
  id?: string
  /**
   * 点击 URL 时的回调
   */
  onUrlClick?: (url: string) => void
  /**
   * 点击文件路径时的回调
   */
  onFileClick?: (path: string) => void
  /**
   * mention 链接（如 mention://issue/UUID）的自定义渲染器。
   * 不传时，mention 会渲染成一个简单带样式的 span。
   */
  renderMention?: (props: { type: string; id: string }) => React.ReactNode
  /**
   * 用于文件卡片检测的 CDN 域名（如 "multica-static.copilothub.ai"）。
   * 传入后启用文件卡片的预处理和渲染。
   */
  cdnDomain?: string
  /**
   * 图片渲染器的可选覆盖。传入后替换默认的受限尺寸 `<img>`。
   * views 包的包装器用它注入统一的 `<Attachment>` 组件，让聊天消息里的图片
   * 和评论一样拥有悬停工具栏 / 灯箱 / 预览弹窗等能力。
   */
  renderImage?: (props: { src: string; alt: string }) => React.ReactNode
  /**
   * 文件卡片渲染器的可选覆盖。传入后替换默认的简化卡片（文件名 + 下载按钮）。
   * 用法和 `renderImage` 一样，用于桥接进 views 包的 `<Attachment>` 组件。
   */
  renderFileCard?: (props: { href: string; filename: string }) => React.ReactNode
  /**
   * 为 true 时，裸的 issue 编号（如 `MUL-123`、`TES-1`）会被改写成
   * `mention://issue/<identifier>` 链接，让 `renderMention` 能把它解析成可跳转的
   * issue 标签。默认关闭——只在 `renderMention` 能解析编号的界面上启用（见
   * packages/views/common/markdown.tsx 的应用包装器）。检测是 markdown 感知的：
   * 代码块、已有链接、URL、文件/路径 token 都会被跳过。
   */
  autolinkIssueIdentifiers?: boolean
}

// 文件路径检测正则：匹配以 /、~/ 或 ./ 开头的路径
const FILE_PATH_REGEX =
  /^(?:\/|~\/|\.\/)[\w\-./@]+\.(?:ts|tsx|js|jsx|mjs|cjs|md|json|yaml|yml|py|go|rs|css|scss|less|html|htm|txt|log|sh|bash|zsh|swift|kt|java|c|cpp|h|hpp|rb|php|xml|toml|ini|cfg|conf|env|sql|graphql|vue|svelte|astro|prisma)$/i

/**
 * 根据渲染模式生成 react-markdown 的自定义组件映射。
 *
 * @param mode 渲染模式（terminal/minimal/full）
 * @param onUrlClick 点击普通 URL 的回调
 * @param onFileClick 点击文件路径的回调
 * @param renderMention mention 链接的自定义渲染器
 * @param renderImage 图片渲染器的覆盖
 * @param renderFileCard 文件卡片渲染器的覆盖
 * @returns react-markdown 用的组件映射表
 */
function createComponents(
  mode: RenderMode,
  onUrlClick?: (url: string) => void,
  onFileClick?: (path: string) => void,
  renderMention?: (props: { type: string; id: string }) => React.ReactNode,
  renderImage?: (props: { src: string; alt: string }) => React.ReactNode,
  renderFileCard?: (props: { href: string; filename: string }) => React.ReactNode,
): Partial<Components> {
  const baseComponents: Partial<Components> = {
    // 文件卡片：拦截 preprocessFileCards 生成的 <div data-type="fileCard"> 并渲染成卡片
    div: ({ node, children, ...props }) => {
      const dataType = node?.properties?.dataType as string | undefined
      if (dataType === 'fileCard') {
        const rawHref = (node?.properties?.dataHref as string) || ''
        const href = isAllowedFileCardHref(rawHref) ? rawHref : ''
        const filename = (node?.properties?.dataFilename as string) || ''
        if (renderFileCard) {
          return <>{renderFileCard({ href, filename })}</>
        }
        return (
          <div className="my-1 flex items-center gap-2 rounded-md border border-border bg-muted/50 px-2.5 py-1 transition-colors hover:bg-muted">
            <FileText className="size-4 shrink-0 text-muted-foreground" />
            <div className="min-w-0 flex-1">
              <p className="truncate text-body">{filename}</p>
            </div>
            {href && (
              <button
                type="button"
                className="shrink-0 rounded-md p-1 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
                onClick={() => window.open(href, '_blank', 'noopener,noreferrer')}
              >
                <Download className="size-3.5" />
              </button>
            )}
          </div>
        )
      }
      return <div {...props}>{children}</div>
    },
    // 图片：用受限尺寸渲染上传的图片
    img: ({ src, alt }) => {
      if (renderImage) {
        return <>{renderImage({ src: typeof src === 'string' ? src : '', alt: alt ?? '' })}</>
      }
      return (
        <img
          src={src}
          alt={alt ?? ""}
          className="max-w-full h-auto rounded-md my-2"
          loading="lazy"
        />
      )
    },
    // 链接：加上点击回调，或者渲染成 mention
    a: ({ href, children }) => {
      // mention 链接形式：mention://member/id、mention://agent/id、mention://issue/id、mention://project/id、mention://all/all
      if (href?.startsWith('mention://')) {
        const mentionMatch = href.match(/^mention:\/\/(member|agent|issue|project|all)\/(.+)$/)
        if (mentionMatch?.[1] && mentionMatch[2]) {
          const type = mentionMatch[1]
          const id = mentionMatch[2]

          if (renderMention) {
            // 自定义渲染器对自己不处理的类型可以返回 null/undefined 来“放弃”，
            // 这样会落到下面默认的带样式 span，保证内容永远不会静默消失。
            const rendered = renderMention({ type, id })
            if (rendered) return <>{rendered}</>
          }

          // 兜底：渲染成一个简单的带样式 span
          return (
            <span className="text-primary font-semibold mx-0.5">
              {children}
            </span>
          )
        }
        return (
          <span className="text-primary font-semibold mx-0.5">
            {children}
          </span>
        )
      }

      if (href?.startsWith('slash://skill/')) {
        return (
          <span className="slash-command text-primary font-semibold mx-0.5">
            {children}
          </span>
        )
      }

      const handleClick = (e: React.MouseEvent): void => {
        e.preventDefault()
        if (href) {
          // 判断是否为文件路径
          if (FILE_PATH_REGEX.test(href) && onFileClick) {
            onFileClick(href)
          } else if (onUrlClick) {
            onUrlClick(href)
          } else {
            // 兜底：在新窗口打开
            window.open(href, '_blank', 'noopener,noreferrer')
          }
        }
      }

      return (
        <a
          href={href}
          onClick={handleClick}
          className="text-primary hover:underline cursor-pointer"
        >
          {children}
        </a>
      )
    }
  }

  // terminal 模式：最少格式化
  if (mode === 'terminal') {
    return {
      ...baseComponents,
      // 代码不做特殊处理，只用等宽字体
      code: ({ children }) => <code className={cn('font-mono', CODE_LIGATURE_CLASS)}>{children}</code>,
      pre: ({ children }) => (
        <pre className={cn('font-mono whitespace-pre-wrap my-2', CODE_LIGATURE_CLASS)}>
          {children}
        </pre>
      ),
      // 段落间距最小化
      p: ({ children }) => <p className="my-1">{children}</p>,
      // 简单列表
      ul: ({ children }) => <ul className="list-disc list-inside my-1">{children}</ul>,
      ol: ({ children }) => <ol className="list-decimal list-inside my-1">{children}</ol>,
      li: ({ children }) => <li className="my-0.5">{children}</li>,
      // 朴素表格
      table: ({ children }) => <table className="my-2 font-mono text-body">{children}</table>,
      th: ({ children }) => <th className="text-left pr-4">{children}</th>,
      td: ({ children }) => <td className="pr-4">{children}</td>
    }
  }

  // minimal 模式：干净、带语法高亮
  if (mode === 'minimal') {
    return {
      ...baseComponents,
      // 行内代码 / 代码块统一处理入口
      code: ({ className, children, ...props }) => {
        const match = /language-(\w+)/.exec(className || '')
        const isBlock =
          'node' in props && props.node?.position?.start.line !== props.node?.position?.end.line

        // 代码块：用 full 模式的 CodeBlock 渲染
        if (match || isBlock) {
          const code = String(children).replace(/\n$/, '')
          return <CodeBlock code={code} language={match?.[1]} mode="full" className="my-1" />
        }

        // 行内代码
        return <InlineCode>{children}</InlineCode>
      },
      pre: ({ children }) => <>{children}</>,
      // 舒适的段落间距
      p: ({ children }) => <p className="my-2 leading-relaxed">{children}</p>,
      // 带样式列表
      ul: ({ children }) => (
        <ul className="my-2 space-y-1 ps-4 pe-2 list-disc marker:text-muted-foreground">
          {children}
        </ul>
      ),
      ol: ({ children }) => <ol className="my-2 space-y-1 pl-6 list-decimal">{children}</ol>,
      li: ({ children }) => <li>{children}</li>,
      // 简洁表格
      table: ({ children }) => (
        <div className="my-3 overflow-x-auto">
          <table className="min-w-full text-body">{children}</table>
        </div>
      ),
      thead: ({ children }) => <thead className="border-b">{children}</thead>,
      th: ({ children }) => (
        <th className="text-left py-2 px-3 font-semibold text-muted-foreground">{children}</th>
      ),
      td: ({ children }) => <td className="py-2 px-3 border-b border-border/50">{children}</td>,
      // 标题：H1/H2 同字号，靠字重区分
      h1: ({ children }) => <h1 className="font-sans text-title-sm font-bold mt-5 mb-3">{children}</h1>,
      h2: ({ children }) => (
        <h2 className="font-sans text-title-sm font-semibold mt-4 mb-3">{children}</h2>
      ),
      h3: ({ children }) => (
        <h3 className="font-sans text-body font-semibold mt-4 mb-2">{children}</h3>
      ),
      // 引用块
      blockquote: ({ children }) => (
        <blockquote className="border-l-2 border-muted-foreground/30 pl-3 my-2 text-muted-foreground italic">
          {children}
        </blockquote>
      ),
      // 水平分割线
      hr: () => <hr className="my-4 border-border" />,
      // 加粗 / 斜体
      strong: ({ children }) => <strong className="font-semibold">{children}</strong>,
      em: ({ children }) => <em className="italic">{children}</em>
    }
  }

  // full 模式：丰富样式
  return {
    ...baseComponents,
    // 完整代码块，带复制按钮
    code: ({ className, children, ...props }) => {
      const match = /language-(\w+)/.exec(className || '')
      const isBlock =
        'node' in props && props.node?.position?.start.line !== props.node?.position?.end.line

      if (match || isBlock) {
        const code = String(children).replace(/\n$/, '')
        return <CodeBlock code={code} language={match?.[1]} mode="full" className="my-1" />
      }

      return <InlineCode>{children}</InlineCode>
    },
    pre: ({ children }) => <>{children}</>,
    // 段落间距更宽松
    p: ({ children }) => <p className="my-3 leading-relaxed">{children}</p>,
    // 带样式列表
    ul: ({ children }) => (
      <ul className="my-3 space-y-1.5 ps-4 pe-2 list-disc marker:text-muted-foreground">
        {children}
      </ul>
    ),
    ol: ({ children }) => <ol className="my-3 space-y-1.5 pl-6 list-decimal">{children}</ol>,
    li: ({ children }) => <li className="leading-relaxed">{children}</li>,
    // 精美表格
    table: ({ children }) => (
      <div className="my-4 overflow-x-auto rounded-md border">
        <table className="min-w-full divide-y divide-border">{children}</table>
      </div>
    ),
    thead: ({ children }) => <thead className="bg-muted/50">{children}</thead>,
    tbody: ({ children }) => <tbody className="divide-y divide-border">{children}</tbody>,
    th: ({ children }) => <th className="text-left py-3 px-4 font-semibold text-body">{children}</th>,
    td: ({ children }) => <td className="py-3 px-4 text-body">{children}</td>,
    tr: ({ children }) => <tr className="hover:bg-muted/30 transition-colors">{children}</tr>,
    // 层次分明的标题
    h1: ({ children }) => <h1 className="font-sans text-title-sm font-bold mt-7 mb-4">{children}</h1>,
    h2: ({ children }) => (
      <h2 className="font-sans text-title-sm font-semibold mt-6 mb-3">{children}</h2>
    ),
    h3: ({ children }) => <h3 className="font-sans text-body font-semibold mt-5 mb-3">{children}</h3>,
    h4: ({ children }) => <h4 className="text-body font-semibold mt-3 mb-1">{children}</h4>,
    // 带样式引用块
    blockquote: ({ children }) => (
      <blockquote className="border-l-4 border-foreground/30 bg-muted/30 pl-4 pr-3 py-2 my-3 rounded-r-md">
        {children}
      </blockquote>
    ),
    // 任务列表（GFM）
    input: ({ type, checked }) => {
      if (type === 'checkbox') {
        return (
          <input
            type="checkbox"
            checked={checked}
            readOnly
            className="mr-2 rounded border-muted-foreground"
          />
        )
      }
      return <input type={type} />
    },
    // 水平分割线
    hr: () => <hr className="my-6 border-border" />,
    // 加粗 / 斜体 / 删除线
    strong: ({ children }) => <strong className="font-semibold">{children}</strong>,
    em: ({ children }) => <em className="italic">{children}</em>,
    del: ({ children }) => <del className="line-through text-muted-foreground">{children}</del>
  }
}

/**
 * Markdown 组件：支持多种渲染模式、可定制的 markdown 渲染器。
 *
 * 能力清单：
 * - 三种渲染模式：terminal、minimal、full
 * - 通过 Shiki 做语法高亮
 * - 支持 GFM（表格、任务列表、删除线）
 * - 可点击的链接和文件路径
 * - 为流式输出做记忆化优化
 * - 通过 renderMention 属性接入可插拔的 mention 渲染
 *
 * @param props 见 MarkdownProps 定义
 * @returns markdown 渲染后的 React 元素
 */
export function Markdown({
  children,
  mode = 'minimal',
  className,
  onUrlClick,
  onFileClick,
  renderMention,
  renderImage,
  renderFileCard,
  cdnDomain,
  autolinkIssueIdentifiers
}: MarkdownProps): React.JSX.Element {
  const components = React.useMemo(
    () => createComponents(mode, onUrlClick, onFileClick, renderMention, renderImage, renderFileCard),
    [mode, onUrlClick, onFileClick, renderMention, renderImage, renderFileCard]
  )

  // 预处理：把 mention 短码、裸 issue 编号、原始 URL、文件卡片都转成可渲染内容。
  // issue 编号自动链接要放在 linkify / file-card 之前执行，这样后续这些步骤会把
  // 改写后的片段当作“已存在的 markdown 链接”而跳过它们。
  const processedContent = React.useMemo(
    () => {
      let result = preprocessMentionShortcodes(children)
      if (autolinkIssueIdentifiers) result = preprocessIssueIdentifiers(result)
      result = preprocessLinks(result)
      result = preprocessFileCards(result, cdnDomain ?? '')
      return result
    },
    [children, cdnDomain, autolinkIssueIdentifiers]
  )

  return (
    <div className={cn('markdown-content break-words', className)}>
      <ReactMarkdown
        remarkPlugins={[
          [remarkMath, { singleDollarTextMath: false }],
          remarkBreaks,
          [remarkGfm, { singleTilde: false }],
        ]}
        rehypePlugins={[rehypeRaw, [rehypeSanitize, markdownSanitizeSchema], rehypeKatex]}
        urlTransform={markdownUrlTransform}
        components={components}
      >
        {processedContent}
      </ReactMarkdown>
    </div>
  )
}

/**
 * MemoizedMarkdown：为流式场景优化的记忆化 Markdown。
 *
 * 把内容拆成块并分别记忆化，流式输出时只有新增/变化的块会重新渲染。
 */
export const MemoizedMarkdown = React.memo(Markdown, (prevProps, nextProps) => {
  // 如果传了 id，就优先用 id 做记忆化判断
  if (prevProps.id && nextProps.id) {
    return (
      prevProps.id === nextProps.id &&
      prevProps.children === nextProps.children &&
      prevProps.mode === nextProps.mode
    )
  }
  // 否则比较内容与模式
  return prevProps.children === nextProps.children && prevProps.mode === nextProps.mode
})
MemoizedMarkdown.displayName = 'MemoizedMarkdown'
