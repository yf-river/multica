/**
 * File card preprocessing for markdown content.
 *
 * Converts file-card syntax into HTML divs that can be rendered by
 * react-markdown with a custom `div` component.
 *
 * The current syntax is `!file[name](url)`. Its explicit prefix keeps file
 * cards distinct from ordinary Markdown links.
 *
 * Output: `<div data-type="fileCard" data-href="url" data-filename="name"></div>`
 *
 * All functions are pure — no global state, no imports from core/ or views/.
 */

// Keep in sync with UUID_RE in packages/core/types/attachment-url.ts.
const ATTACHMENT_UUID_SOURCE =
  '[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}'
const ATTACHMENT_DOWNLOAD_URL_SOURCE = `/api/attachments/${ATTACHMENT_UUID_SOURCE}/download`
const ATTACHMENT_DOWNLOAD_URL_RE = new RegExp(
  `^${ATTACHMENT_DOWNLOAD_URL_SOURCE}$`,
)

/**
 * URL alternation accepted inside `!file[name](url)` markdown.
 *
 * Restricted to:
 * - `/uploads/...` site-relative paths (LocalStorage backend with no LOCAL_UPLOAD_BASE_URL)
 * - `/api/attachments/<UUID>/download` site-relative attachment downloads
 * - `http(s)://...` absolute URLs (S3 / CloudFront / hosted)
 *
 * Anything else — `javascript:`, `data:`, protocol-relative `//host/x`, other
 * APIs `/api/…`, path-traversal `/../…` — is rejected so a stored file-card
 * cannot be turned into an out-of-band navigation.
 */
export const FILE_CARD_URL_PATTERN = new RegExp(
  `/uploads/[^)]*|https?:\\/\\/[^)]+|${ATTACHMENT_DOWNLOAD_URL_SOURCE}`,
)

/** Prefix test applied by renderers to validate `data-href` before opening it. */
export function isAllowedFileCardHref(href: string): boolean {
  return (
    /^(https?:\/\/|\/uploads\/)/i.test(href) ||
    ATTACHMENT_DOWNLOAD_URL_RE.test(href)
  )
}

/** New syntax: !file[name](url) — unambiguous, no hostname matching needed. */
const NEW_FILE_CARD_RE = new RegExp(
  `^!file\\[((?:\\\\.|[^\\]])*)\\]\\((${FILE_CARD_URL_PATTERN.source})\\)$`,
)

function escapeAttr(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;')
}

function toFileCardHtml(filename: string, url: string): string {
  return `<div data-type="fileCard" data-href="${escapeAttr(url)}" data-filename="${escapeAttr(filename)}"></div>`
}

/**
 * Preprocess markdown to convert file-card syntax into HTML divs.
 */
export function preprocessFileCards(markdown: string): string {
  return markdown
    .split('\n')
    .map((line) => {
      const trimmed = line.trim()

      const newMatch = trimmed.match(NEW_FILE_CARD_RE)
      if (newMatch) {
        const filename = newMatch[1]!.replace(/\\([[\]\\()])/g, '$1')
        return toFileCardHtml(filename, newMatch[2]!)
      }
      return line
    })
    .join('\n')
}
