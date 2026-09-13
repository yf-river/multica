/**
 * 面向 markdown 内容的“文件卡片”预处理。
 *
 * 把文件卡片语法转换成 HTML div，再由 react-markdown 用自定义 `div` 组件渲染。
 *
 * 支持两种语法：
 * 1. `!file[name](url)` — 新的无歧义语法（无需校验域名）
 * 2. `[name](cdnUrl)` — 旧语法，通过 CDN 域名匹配，需独占一行
 *
 * 输出：`<div data-type="fileCard" data-href="url" data-filename="name"></div>`
 *
 * 所有函数都是纯函数——无全局状态，也不从 core/ 或 views/ 导入。
 */

const IMAGE_EXTS = /\.(png|jpe?g|gif|webp|svg|ico|bmp|tiff?)$/i

// 需与 packages/core/types/attachment-url.ts 里的 UUID_RE 保持一致。
const ATTACHMENT_UUID_SOURCE =
  '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}'
const ATTACHMENT_DOWNLOAD_URL_SOURCE = `/api/attachments/${ATTACHMENT_UUID_SOURCE}/download`
const ATTACHMENT_DOWNLOAD_URL_RE = new RegExp(
  `^${ATTACHMENT_DOWNLOAD_URL_SOURCE}$`,
)

/**
 * `!file[name](url)` 语法里允许出现的 URL 形式。
 *
 * 只允许：
 * - `/uploads/...` 站内相对路径（无 LOCAL_UPLOAD_BASE_URL 时的 LocalStorage 后端）
 * - `/api/attachments/<UUID>/download` 站内相对附件下载
 * - `http(s)://...` 绝对 URL（S3 / CloudFront / 托管域名）
 *
 * 其它一律拒绝——如 `javascript:`、`data:`、协议相对地址 `//host/x`、其它
 * 接口 `/api/…`、路径穿越 `/../…`——避免存储的文件卡片被用来做越界跳转。
 */
export const FILE_CARD_URL_PATTERN = new RegExp(
  `/uploads/[^)]*|https?:\\/\\/[^)]+|${ATTACHMENT_DOWNLOAD_URL_SOURCE}`,
)

/** 渲染器在打开 `data-href` 前用来校验其前缀是否合法的判断函数。 */
export function isAllowedFileCardHref(href: string): boolean {
  return (
    /^(https?:\/\/|\/uploads\/)/i.test(href) ||
    ATTACHMENT_DOWNLOAD_URL_RE.test(href)
  )
}

/**
 * 新语法：!file[name](url) —— 无歧义，无需域名匹配。
 * 标签字符类里排除了反斜杠，让 "\x" 这种串只能被 \\. 消费——重叠的可选分支
 * 会以 2^n 的复杂度回溯（ReDoS 风险，GitHub #4881）。这段会在每次评论/描述
 * 渲染时运行。
 */
const NEW_FILE_CARD_RE = new RegExp(
  `^!file\\[((?:\\\\.|[^\\]\\\\])*)\\]\\((${FILE_CARD_URL_PATTERN.source})\\)$`,
)

/** 旧语法：独占一行的 [name](cdnUrl)，靠 CDN 域名匹配。 */
const FILE_LINK_LINE = /^\[([^\]]+)\]\((https?:\/\/[^)]+)\)$/

/** 转义 HTML 属性中的特殊字符，防止注入。 */
function escapeAttr(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;')
}

/** 根据文件名和 URL 生成文件卡片的 HTML div。 */
function toFileCardHtml(filename: string, url: string): string {
  return `<div data-type="fileCard" data-href="${escapeAttr(url)}" data-filename="${escapeAttr(filename)}"></div>`
}

/**
 * 判断 URL 是否指向我们的上传 CDN。
 *
 * 用 `cdnDomain`（如 "multica-static.copilothub.ai"）做精确域名匹配，同时
 * 也匹配任何 `.amazonaws.com` 子域，作为直连 S3 地址的兜底。
 */
export function isCdnUrl(url: string, cdnDomain: string): boolean {
  try {
    const u = new URL(url)
    return u.hostname === cdnDomain || u.hostname.endsWith('.amazonaws.com')
  } catch {
    return false
  }
}

/**
 * 判断 CDN URL 是否为应渲染成文件卡片的非图片文件。
 * 图片 URL（png、jpg 等）会被排除——它们渲染成行内图片。
 */
export function isFileCardUrl(url: string, cdnDomain: string): boolean {
  try {
    return isCdnUrl(url, cdnDomain) && !IMAGE_EXTS.test(new URL(url).pathname)
  } catch {
    return false
  }
}

/**
 * 预处理 markdown，把文件卡片语法转换成 HTML div。
 *
 * 同时处理 `!file[name](url)`（新语法）和旧的 `[name](cdnUrl)` 行。
 * 只匹配独占一行的内容——行内链接不动。
 *
 * @param markdown 原始 markdown 字符串
 * @param cdnDomain 用于旧语法链接检测的 CDN 域名（如 "multica-static.copilothub.ai"）
 * @returns 转换后的 markdown 文本
 */
export function preprocessFileCards(markdown: string, cdnDomain: string): string {
  return markdown
    .split('\n')
    .map((line) => {
      const trimmed = line.trim()

      // 新语法：!file[name](url) —— 一定是文件卡片，无需域名校验。
      const newMatch = trimmed.match(NEW_FILE_CARD_RE)
      if (newMatch) {
        const filename = newMatch[1]!.replace(/\\([[\]\\()])/g, '$1')
        return toFileCardHtml(filename, newMatch[2]!)
      }

      // 旧语法：独占一行的 [name](cdnUrl) —— 用 CDN 域名匹配。
      const match = trimmed.match(FILE_LINK_LINE)
      if (!match) return line
      const filename = match[1]!
      const url = match[2]!
      if (!isFileCardUrl(url, cdnDomain)) return line
      return toFileCardHtml(filename, url)
    })
    .join('\n')
}
