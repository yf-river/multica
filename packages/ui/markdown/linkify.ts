import LinkifyIt from 'linkify-it'

// 本文件负责 URL 与文件路径的检测：用 linkify-it 识别 URL/邮箱，用自定义正则识别本地文件路径，并提供给 markdown 预处理做自动链接。
/**
 * Linkify：面向 markdown 预处理的 URL 与文件路径检测。
 *
 * 用经过大规模验证的 linkify-it 做 URL 检测，另加自定义正则识别本地文件路径。
 */

// 保留模糊检测，让 linkify-it 能识别 `www.` 开头的 URL。
// 下面 collectLinkifyMatches 会再套用更严格的共享产品策略。
const linkify = new LinkifyIt()

// 文件路径检测器用到的常见源码/配置文件扩展名。
const FILE_EXTENSIONS =
  'ts|tsx|js|jsx|mjs|cjs|md|json|yaml|yml|py|go|rs|css|scss|less|html|htm|txt|log|sh|bash|zsh|swift|kt|java|c|cpp|h|hpp|rb|php|xml|toml|ini|cfg|conf|env|sql|graphql|vue|svelte|astro|prisma|dockerfile|makefile|gitignore'

// 文件路径正则：检测以 /、~/ 或 ./ 开头、后接路径字符和文件扩展名的路径。
const FILE_PATH_REGEX = new RegExp(
  `(?:^|[\\s([{<])((\\/|~\\/|\\.\\/)[\\w\\-./@]+\\.(?:${FILE_EXTENSIONS}))(?=[\\s)\\]}.,;:!?>]|$)`,
  'gi'
)

// 应作为 URL 结尾的中日韩全角标点。
// linkify-it 只把 ASCII 标点当作 URL 边界，所以在中文/日文里，URL 后面跟着
// 如 "。" 时会把标点和直到下一个空白的所有字符都吞进 href。我们在这里截断
// 到这些字符第一次出现的位置。字符集与 mattermost/marked#22 的修复保持一致。
const CJK_URL_TERMINATOR_REGEX =
  /[！-／：-＠［-｀｛-～、。「-】]/

// linkify-it 会当作 URL 字符、但实际上在裸 URL 末尾几乎总是作者用来闭合强调/
// 删除线标记（`**url**`、`*url*`、`~~url~~`）的 markdown 行内格式分隔符。我们
// 去掉末尾连续的一串它们，让外层 markdown 仍能正常解析——与 GFM 自身的自动链接
// 尾标点裁剪行为一致。一个真的以 `*`/`~` 结尾的 URL 会失去该字符，和 GitHub 上
// 的行为完全相同（MUL-4242）。
const TRAILING_MD_DELIMITER = /[*~]+$/

/**
 * 检测到的链接记录。
 */
interface DetectedLink {
  /** 链接类型：URL / 邮箱 / 文件 */
  type: 'url' | 'email' | 'file'
  /** 链接显示文本 */
  text: string
  /** 链接目标地址 */
  url: string
  /** 在原文中的起始下标 */
  start: number
  /** 在原文中的结束下标 */
  end: number
}

/**
 * 代码范围（一段区间），用于标记代码块/行内代码/公式的位置。
 */
export interface CodeRange {
  start: number
  end: number
}

/**
 * 自动外链的产品策略。
 *
 * 检测器只在候选通过校验后才会调用它。本函数决定哪些已验证的文本形态可以
 * 在无需用户显式操作时安全地转成链接：HTTP(S) URL、`www.` URL 和邮箱地址。
 * 特别是，其它不带协议的域名一律保持纯文本，这也避免把 `plan.md` 这类文件名
 * 误判成网站。
 *
 * 让编辑器和 markdown 渲染器共用这个判断，保证输入、粘贴和只读展示三处行为
 * 不会再次漂移。
 */
export function shouldAutoLink(value: string): boolean {
  return (
    /^https?:\/\//i.test(value) ||
    /^www\./i.test(value) ||
    /^[^\s@/:]+@[^\s@/:]+\.[^\s@/:]+$/.test(value)
  )
}

/**
 * 找出文本里所有代码块、行内代码、数学公式的区间。
 * 这些区间应在链接检测时被排除。
 *
 * @param text 原文
 * @returns 代码/公式区间数组
 */
export function findCodeRanges(text: string): CodeRange[] {
  const ranges: CodeRange[] = []

  // 找围栏代码块（```...```）
  const fencedRegex = /```[\s\S]*?```/g
  let match
  while ((match = fencedRegex.exec(text)) !== null) {
    ranges.push({ start: match.index, end: match.index + match[0].length })
  }

  // 找显示数学公式块（$$...$$）
  const displayMathRegex = /\$\$[\s\S]*?\$\$/g
  while ((match = displayMathRegex.exec(text)) !== null) {
    const pos = match.index
    const insideOther = ranges.some((r) => pos >= r.start && pos < r.end)
    if (!insideOther) {
      ranges.push({ start: pos, end: pos + match[0].length })
    }
  }

  // 找行内数学公式（$...$）
  const inlineMathRegex = /(?<!\$)\$(?!\$)([^$\n]+)\$(?!\$)/g
  while ((match = inlineMathRegex.exec(text)) !== null) {
    const pos = match.index
    const insideOther = ranges.some((r) => pos >= r.start && pos < r.end)
    if (!insideOther) {
      ranges.push({ start: pos, end: pos + match[0].length })
    }
  }

  // 找行内代码（`...`）
  // 但跳过转义的反引号，以及围栏块内部的代码
  const inlineRegex = /(?<!`)`(?!`)([^`\n]+)`(?!`)/g
  while ((match = inlineRegex.exec(text)) !== null) {
    const pos = match.index
    // 检查是否在围栏块或公式块内部
    const insideOther = ranges.some((r) => pos >= r.start && pos < r.end)
    if (!insideOther) {
      ranges.push({ start: pos, end: pos + match[0].length })
    }
  }

  return ranges
}

/**
 * 判断某个位置是否落在任一代码区间内。
 */
export function isInsideCode(pos: number, ranges: CodeRange[]): boolean {
  return ranges.some((r) => pos >= r.start && pos < r.end)
}

function isEscaped(text: string, index: number): boolean {
  let slashCount = 0
  for (let i = index - 1; i >= 0 && text[i] === '\\'; i--) {
    slashCount++
  }
  return slashCount % 2 === 1
}

function findMatchingBracket(text: string, openIndex: number): number {
  let depth = 0

  for (let i = openIndex; i < text.length; i++) {
    if (isEscaped(text, i)) continue

    const char = text[i]
    if (char === '[') {
      depth++
    } else if (char === ']') {
      depth--
      if (depth === 0) return i
    }
  }

  return -1
}

function findInlineLinkEnd(text: string, openParenIndex: number): number {
  let depth = 0

  for (let i = openParenIndex; i < text.length; i++) {
    if (isEscaped(text, i)) continue

    const char = text[i]
    if (char === '(') {
      depth++
    } else if (char === ')') {
      depth--
      if (depth === 0) return i + 1
    }
  }

  return -1
}

/**
 * 找出已有 markdown 链接/图片的区间，避免自动链接在其标签或目标地址里
 * 再套一层链接。
 */
export function findMarkdownLinkRanges(text: string): CodeRange[] {
  const ranges: CodeRange[] = []

  for (let i = 0; i < text.length; i++) {
    if (text[i] !== '[' || isEscaped(text, i)) continue
    if (ranges.some((r) => i >= r.start && i < r.end)) continue

    const labelEnd = findMatchingBracket(text, i)
    if (labelEnd === -1) continue

    const start = i > 0 && text[i - 1] === '!' && !isEscaped(text, i - 1) ? i - 1 : i
    const nextChar = text[labelEnd + 1]

    if (nextChar === '(') {
      const end = findInlineLinkEnd(text, labelEnd + 1)
      if (end !== -1) {
        ranges.push({ start, end })
        i = end - 1
      }
      continue
    }

    if (nextChar === '[') {
      const referenceEnd = findMatchingBracket(text, labelEnd + 1)
      if (referenceEnd !== -1) {
        ranges.push({ start, end: referenceEnd + 1 })
        i = referenceEnd
      }
    }
  }

  return ranges
}

/**
 * 判断给定位置的链接是否已经是一个 markdown 链接。
 * 会识别 [text](url) 或 [text][ref] 这类形态。
 */
function isAlreadyLinked(text: string, linkStart: number, linkEnd: number): boolean {
  // 检查前面是否紧跟着 ](，说明我们处在 markdown 链接的目标地址里。
  // 形态：[text](URL)——我们在判断 URL 是不是我们的链接。
  const before = text.slice(Math.max(0, linkStart - 2), linkStart)
  if (before.endsWith('](')) return true

  // 检查前面是否紧跟着 ][，对应引用式链接。
  if (before.endsWith('][')) return true

  // 检查链接文本是否被 [] 包裹。
  // 形态：[URL](href)——URL 被当作链接文本使用。
  const charBefore = text[linkStart - 1]
  const charAfter = text[linkEnd]
  if (charBefore === '[' && charAfter === ']') return true

  return false
}

/**
 * 判断两个区间是否重叠。
 */
export function rangesOverlap(
  a: { start: number; end: number },
  b: { start: number; end: number }
): boolean {
  return a.start < b.end && b.start < a.end
}

/**
 * 对 `text` 运行 linkify-it，把规范化后的链接记录压入 `out`，并按 `offset` 偏移。
 * 这里做了两处 linkify-it 自身不做的边界修正：
 * - 中日韩标点会结束一个 URL（linkify-it 只在 ASCII 处断开）；当它跨中日韩标点
 *   吞并了多个 URL 时，我们截断并重新扫描尾部；
 * - 从 URL 末尾去掉一串 markdown 分隔符（`*`、`~`），让 `**url**` 的闭合强调
 *   标记留在链接外面。
 */
function collectLinkifyMatches(text: string, offset: number, out: DetectedLink[]): void {
  const matches = linkify.match(text)
  if (!matches) return

  for (const match of matches) {
    const cjkIdx = match.text.search(CJK_URL_TERMINATOR_REGEX)
    if (cjkIdx === 0) continue // 匹配以中日韩标点开头——跳过

    const truncate = cjkIdx > 0
    // URL 文本截到第一个中日韩标点为止，再去掉末尾一串 markdown 分隔符
    // （如 `**url**` 的闭合 `**`）。matchText 始终是 match.text 的前缀，
    // 所以链接结束位置就是 match.index 加上它的长度。
    const matchText = (truncate ? match.text.slice(0, cjkIdx) : match.text).replace(
      TRAILING_MD_DELIMITER,
      ''
    )

    if (matchText.length > 0 && shouldAutoLink(matchText)) {
      // 当我们裁剪过原始匹配时，用 linkify-it 加上的协议前缀（如 "http://" /
      // "mailto:"）加上裁剪后的文本重建 href；否则沿用 linkify-it 规范化后的 url。
      const trimmed = matchText.length !== match.text.length
      const schemePrefix = match.url.slice(0, match.url.length - match.text.length)
      const matchUrl =
        match.schema === '' && /^www\./i.test(matchText)
          ? `https://${matchText}`
          : trimmed
            ? schemePrefix + matchText
            : match.url

      out.push({
        type: match.schema === 'mailto:' ? 'email' : 'url',
        text: matchText,
        url: matchUrl,
        start: match.index + offset,
        end: match.index + matchText.length + offset
      })
    }

    if (truncate) {
      // 重新扫描中日韩标点之后的尾部——linkify-it 贪婪地把它吞进去了，
      // 所以标点后可能存在的其它 URL 从未被输出。
      const tailStart = match.index + cjkIdx + 1
      collectLinkifyMatches(text.slice(tailStart), offset + tailStart, out)
      return
    }
  }
}

/**
 * 检测文本里所有链接（URL、邮箱、文件路径）。
 *
 * @param text 原文
 * @returns 检测到的链接数组（按位置排序）
 */
export function detectLinks(text: string): DetectedLink[] {
  const links: DetectedLink[] = []

  // 1. 用 linkify-it 检测 URL 和邮箱，并做边界修正。
  collectLinkifyMatches(text, 0, links)

  // 2. 用自定义正则检测文件路径
  // 重置正则状态
  FILE_PATH_REGEX.lastIndex = 0
  let fileMatch
  while ((fileMatch = FILE_PATH_REGEX.exec(text)) !== null) {
    const path = fileMatch[1]
    if (!path) continue // 没有捕获组则跳过

    // 计算实际起始位置（跳过前导空白/标点）
    const fullMatch = fileMatch[0]
    const pathOffset = fullMatch.indexOf(path)
    const start = fileMatch.index + pathOffset

    // 检查是否与 URL 匹配重叠（URL 优先）
    const pathRange = { start, end: start + path.length }
    const overlapsUrl = links.some((link) => rangesOverlap(pathRange, link))
    if (overlapsUrl) continue

    links.push({
      type: 'file',
      text: path,
      url: path, // 文件路径原样传给 onFileClick 处理
      start,
      end: start + path.length
    })
  }

  // 按位置排序
  return links.sort((a, b) => a.start - b.start)
}

/**
 * 预处理文本，把原始 URL 和文件路径转成 markdown 链接。
 * 会跳过代码块和已经链接过的内容。
 *
 * Tiptap 编辑器和只读的 react-markdown 渲染器共用此函数，保证两个界面
 * 的链接化行为一致。URL 末尾的 markdown 分隔符会被排除（见
 * collectLinkifyMatches），这样 `**url**` 能保持加粗、链接也干净（MUL-4242）。
 *
 * @param text 原始文本
 * @returns 链接化后的 markdown 文本
 */
export function preprocessLinks(text: string): string {
  // 快速检查——没有潜在链接就直接返回
  if (!linkify.pretest(text) && !/[~/.]\//.test(text)) {
    return text
  }

  const codeRanges = findCodeRanges(text)
  const markdownLinkRanges = findMarkdownLinkRanges(text)
  const links = detectLinks(text)

  if (links.length === 0) return text

  // 构建结果，把原始链接转成 markdown 链接
  let result = ''
  let lastIndex = 0

  for (const link of links) {
    // 在代码块内则跳过
    if (isInsideCode(link.start, codeRanges)) continue

    // 在已有 markdown 链接或图片内则跳过。
    if (markdownLinkRanges.some((range) => rangesOverlap(link, range))) continue

    // 已经是 markdown 链接则跳过
    if (isAlreadyLinked(text, link.start, link.end)) continue

    // 追加链接之前的文本
    result += text.slice(lastIndex, link.start)

    // 转成 markdown 链接
    result += `[${link.text}](${link.url})`

    lastIndex = link.end
  }

  // 追加剩余文本
  result += text.slice(lastIndex)

  return result
}

/**
 * 测试文本里是否含有任何可检测的链接。
 * 用于优化——没有链接时跳过预处理。
 */
export function hasLinks(text: string): boolean {
  return linkify.pretest(text) || /[~/.]\/[\w]/.test(text)
}
