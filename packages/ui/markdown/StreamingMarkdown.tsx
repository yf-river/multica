// 本文件提供流式 markdown 渲染：把内容切成块并逐块记忆化，流式输出时只有最后一个（正在生成的）块会反复重渲染，已完成块保持稳定不变。
import * as React from 'react'
import { Markdown, type RenderMode } from './Markdown'

/**
 * 流式 markdown 组件的 props 定义。
 */
export interface StreamingMarkdownProps {
  /** 当前已生成的全部内容文本 */
  content: string
  /** 是否正在流式输出中 */
  isStreaming: boolean
  /** 渲染模式 */
  mode?: RenderMode
  /** 外层 class 名 */
  className?: string
  /** 点击 URL 的回调 */
  onUrlClick?: (url: string) => void
  /** 点击文件路径的回调 */
  onFileClick?: (path: string) => void
  /** mention 链接的自定义渲染器 */
  renderMention?: (props: { type: string; id: string }) => React.ReactNode
  /** 文件卡片检测用的 CDN 域名 */
  cdnDomain?: string
}

/**
 * 内容块：一段文本，标记是否为代码块。
 */
interface Block {
  /** 该块的内容 */
  content: string
  /** 是否为代码块（``` 包裹） */
  isCodeBlock: boolean
}

/**
 * djb2 哈希（XOR 变体），作者 Daniel J. Bernstein。
 * 用来给已完成的文本块生成稳定的 React key。
 *
 * - 5381：经验选定的初始值，碰撞更少
 * - (hash << 5) + hash：等价于 hash * 33
 * - ^ charCode：XOR 变体，Bernstein 更推荐这种而非加法版本
 * - >>> 0：转成无符号 32 位整数
 *
 * 不是加密哈希——只是对短字符串分布良好且够快。
 * @see http://www.cse.yorku.ca/~oz/hash.html
 */
function simpleHash(str: string): string {
  let hash = 5381
  for (let i = 0; i < str.length; i++) {
    hash = ((hash << 5) + hash) ^ str.charCodeAt(i)
  }
  return (hash >>> 0).toString(36)
}

/**
 * 把内容拆成块（段落和代码块）。
 *
 * 块边界：
 * - 空行（段落分隔）
 * - 代码围栏（```）
 *
 * 刻意保持简单——纯字符串扫描，不做逐行正则。
 *
 * @param content 原始 markdown 文本
 * @returns 拆分后的内容块数组
 */
function splitIntoBlocks(content: string): Block[] {
  const blocks: Block[] = []
  const lines = content.split('\n')
  let currentBlock = ''
  let inCodeBlock = false
  let inMathBlock = false

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? ''

    // 检查代码围栏（行首 ```，后面可选语言标识）
    if (line.startsWith('```')) {
      if (!inCodeBlock) {
        // 进入代码块：先把当前段落冲刷出去
        if (currentBlock.trim()) {
          blocks.push({ content: currentBlock.trim(), isCodeBlock: false })
          currentBlock = ''
        }
        inCodeBlock = true
        currentBlock = line + '\n'
      } else {
        // 结束代码块
        currentBlock += line
        blocks.push({ content: currentBlock, isCodeBlock: true })
        currentBlock = ''
        inCodeBlock = false
      }
    } else if (inCodeBlock) {
      // 代码块内部：追加这一行
      currentBlock += line + '\n'
    // 检查显示数学公式围栏（$$）
    } else if (line.trim() === '$$') {
      if (!inMathBlock) {
        // 进入公式块：先把当前段落冲刷出去
        if (currentBlock.trim()) {
          blocks.push({ content: currentBlock.trim(), isCodeBlock: false })
          currentBlock = ''
        }
        inMathBlock = true
        currentBlock = line + '\n'
      } else {
        // 结束公式块
        currentBlock += line
        blocks.push({ content: currentBlock, isCodeBlock: false })
        currentBlock = ''
        inMathBlock = false
      }
    } else if (inMathBlock) {
      // 公式块内部：追加这一行（不按空行切分）
      currentBlock += line + '\n'
    } else if (line === '') {
      // 代码块外的空行 = 段落边界
      if (currentBlock.trim()) {
        blocks.push({ content: currentBlock.trim(), isCodeBlock: false })
        currentBlock = ''
      }
    } else {
      // 普通文本行
      if (currentBlock) {
        currentBlock += '\n' + line
      } else {
        currentBlock = line
      }
    }
  }

  // 冲刷剩余内容
  if (currentBlock) {
    blocks.push({
      content: inCodeBlock || inMathBlock ? currentBlock : currentBlock.trim(),
      isCodeBlock: inCodeBlock
    })
  }

  return blocks
}

/**
 * 记忆化的块组件。
 *
 * 只有内容或模式变化时才重新渲染。
 * key 由父组件基于内容哈希分配，所以相同内容根本不会触发渲染。
 */
const MemoizedBlock = React.memo(
  function Block({
    content,
    mode,
    className,
    onUrlClick,
    onFileClick,
    renderMention,
    cdnDomain
  }: {
    content: string
    mode: RenderMode
    className?: string
    onUrlClick?: (url: string) => void
    onFileClick?: (path: string) => void
    renderMention?: (props: { type: string; id: string }) => React.ReactNode
    cdnDomain?: string
  }) {
    return (
      <Markdown mode={mode} className={className} onUrlClick={onUrlClick} onFileClick={onFileClick} renderMention={renderMention} cdnDomain={cdnDomain}>
        {content}
      </Markdown>
    )
  },
  (prev, next) => {
    // 只有内容真正变化时才重新渲染
    return prev.content === next.content && prev.mode === next.mode && prev.className === next.className
  }
)
MemoizedBlock.displayName = 'MemoizedBlock'

/**
 * StreamingMarkdown：面向流式内容的优化版 markdown 渲染器。
 *
 * 把内容拆成块（段落、代码块）并独立记忆化，流式输出时只有最后一个（活动的）
 * 块会反复重渲染。
 *
 * 核心思路：已完成的块用内容哈希作为 React key。内容相同 = key 相同 =
 * React 完全跳过重渲染。
 *
 * @example
 * 内容："Hello\n\n```js\ncode\n```\n\nMore..."
 *
 * 块 1："Hello"            -> key="block-abc123" -> 已记忆化
 * 块 2："```js\ncode\n```" -> key="block-xyz789" -> 已记忆化
 * 块 3："More..."          -> key="active-2"     -> 持续重渲染
 *
 * @param props 见 StreamingMarkdownProps 定义
 * @returns 流式渲染的 markdown 内容
 */
export function StreamingMarkdown({
  content,
  isStreaming,
  mode = 'minimal',
  className,
  onUrlClick,
  onFileClick,
  renderMention,
  cdnDomain
}: StreamingMarkdownProps): React.JSX.Element {
  // 拆分成块，用 useMemo 避免重复计算。
  // 注意：必须无条件调用，以满足 React Hooks 规则。
  const blocks = React.useMemo(
    () => (isStreaming ? splitIntoBlocks(content) : []),
    [content, isStreaming]
  )

  // 非流式状态：直接用普通 Markdown（无需分块）
  if (!isStreaming) {
    return (
      <Markdown mode={mode} className={className} onUrlClick={onUrlClick} onFileClick={onFileClick} renderMention={renderMention} cdnDomain={cdnDomain}>
        {content}
      </Markdown>
    )
  }

  // 空内容：返回空片段，加载指示器交给父组件处理
  if (blocks.length === 0) {
    return <></>
  }

  return (
    <>
      {blocks.map((block, i) => {
        const isLastBlock = i === blocks.length - 1

        // 已完成块用内容哈希当 key -> 身份稳定 -> 走记忆化
        // 最后一个块用 "active" 前缀 -> 内容变化时总是重渲染
        const key = isLastBlock ? `active-${i}` : `block-${i}-${simpleHash(block.content)}`

        return (
          <MemoizedBlock
            key={key}
            content={block.content}
            mode={mode}
            className={className}
            onUrlClick={onUrlClick}
            onFileClick={onFileClick}
            renderMention={renderMention}
            cdnDomain={cdnDomain}
          />
        )
      })}
    </>
  )
}
