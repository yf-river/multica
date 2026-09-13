import {
  detectLinks,
  findCodeRanges,
  findMarkdownLinkRanges,
  isInsideCode,
  rangesOverlap,
} from './linkify'

// 本文件负责把“裸 issue 编号”（如 MUL-123）自动改写成 mention 链接，供 markdown 预处理阶段调用。
/**
 * Linear 风格的裸 issue 编号自动链接，用于 markdown 预处理。
 *
 * 把 `MUL-123` / `TES-1` 这类裸编号改写成规范的 mention 链接
 * `[MUL-123](mention://issue/MUL-123)`，这样共享的 markdown `a`/mention 渲染器
 * 就能把它们路由成可跳转的 issue 标签。
 *
 * 本模块刻意保持纯函数（packages/ui 层）：不访问工作区或 API。它只负责“检测”
 * 候选编号——某个编号是否对应当前工作区里真实存在的 issue，交给 views 层通过
 * 精确匹配查询来判定。解析不到的候选会渲染成普通文本，所以这里检测偏多是
 * 无害的（最多一次去重查询）。我们仍会跳过代码、已有链接、URL 和文件/路径
 * token，保证非正文内容永不被改写。
 *
 * 注意：`mention://issue/<id>` 这个传输形式同时用于真实 UUID mention 和裸编号。
 * 渲染端靠 id 段的形状（isIssueIdentifier）区分——UUID 永远不会匹配编号模式，
 * 所以分发是无歧义的。
 */

// 检测用正则（全局匹配）。前缀是一个大写字母接一串大写字母数字，再跟 `-<数字>`。
// 刻意区分大小写：正文里的小写 `abc-1` 几乎不会是 issue 引用，匹配它反而会把
// 普通的连字符单词错误地变成链接。两侧的环视排除了字母数字、`_` 和 `-`，
// 确保只匹配独立 token。
const IDENTIFIER_RE = /(?<![A-Za-z0-9_-])([A-Z][A-Z0-9]*-\d+)(?![A-Za-z0-9_-])/g

// 锚定的单 token 形式——渲染端用它区分裸编号（经 `mention://issue/<identifier>`
// 携带）和真实 UUID。
export const ISSUE_IDENTIFIER_PATTERN = /^[A-Z][A-Z0-9]*-\d+$/

/**
 * 当 `value` 是裸 issue 编号（如 "MUL-123"）时返回 true。UUID 永远不会匹配
 * （小写十六进制、四个短横），因此能在渲染时干净地把自动链接的编号和真实
 * mention UUID 区分开。
 */
export function isIssueIdentifier(value: string): boolean {
  return ISSUE_IDENTIFIER_PATTERN.test(value)
}

/**
 * 把裸 issue 编号改写成规范的 `mention://issue/<identifier>` markdown 链接。
 * 必须在 `preprocessLinks`/`preprocessFileCards` 之前运行，让这些步骤把改写后的
 * 片段当作已存在的 markdown 链接而跳过。
 *
 * 跳过的上下文（永不改写）：
 *   - 围栏代码块、行内代码和数学公式（findCodeRanges）
 *   - 已有的 markdown 链接 / 图片（findMarkdownLinkRanges）
 *   - 已检测到的 URL / 邮箱 / 文件路径（detectLinks）
 *   - 带点的文件名后缀如 `ABC-123.ts`、路径片段如 `FOO-1/bar`
 */
export function preprocessIssueIdentifiers(text: string): string {
  // 快速早退：完全没有 `<大写>…-<数字>` 形态就直接返回。
  if (!/[A-Z][A-Z0-9]*-\d/.test(text)) return text

  const codeRanges = findCodeRanges(text)
  const linkRanges = findMarkdownLinkRanges(text)
  const detectedLinks = detectLinks(text)

  IDENTIFIER_RE.lastIndex = 0
  let result = ''
  let lastIndex = 0
  let match: RegExpExecArray | null

  while ((match = IDENTIFIER_RE.exec(text)) !== null) {
    const identifier = match[1]
    if (!identifier) continue
    const start = match.index
    const end = start + identifier.length
    const range = { start, end }

    // 在围栏/行内代码或数学公式里。
    if (isInsideCode(start, codeRanges)) continue
    // 在已有 markdown 链接/图片里（标签或目标地址）。
    if (linkRanges.some((r) => rangesOverlap(range, r))) continue
    // 在已检测到的 URL / 邮箱 / 文件路径里。
    if (detectedLinks.some((l) => rangesOverlap(range, l))) continue

    // 带点后缀如 `ABC-123.ts`（文件名）或 `ABC-123.tar.gz`：`.` 后面紧跟字母数字
    // 说明这个 token 是更大点分名字的一部分。而空白/行尾前的句号是句子结尾
    // （如 "see MUL-1."），仍应可点击。
    const after = text[end]
    if (after === '.' && /[A-Za-z0-9]/.test(text[end + 1] ?? '')) continue
    // 路径片段如 `FOO-1/bar` 或 `foo/BAR-1`——任一侧有 `/` 说明是路径而非独立引用。
    if (after === '/' || text[start - 1] === '/') continue
    // 左侧嵌在点分名字里（`file.MUL-1`）。
    if (text[start - 1] === '.') continue

    result += text.slice(lastIndex, start)
    result += `[${identifier}](mention://issue/${identifier})`
    lastIndex = end
  }

  if (lastIndex === 0) return text
  result += text.slice(lastIndex)
  return result
}
