/**
 * Stable URL helpers for attachment references that get persisted into
 * markdown bodies (issue descriptions, comment bodies, chat messages).
 *
 * Background — MUL-3130:
 *
 *   The original upload flow returned `att.url` as the link to embed in
 *   markdown. After PR #3903 (MUL-3132), `att.url` for the LocalStorage
 *   backend became a 30-minute HMAC-signed `/uploads/<key>?exp&sig` URL.
 *   That URL is short-lived by design — it's the in-memory token that
 *   keeps native browser <img>/<video> resource loads working without an
 *   Authorization header. Persisting it into a comment body means the
 *   comment's images break the moment the signature expires (default
 *   30 min). The same problem applies to CloudFront-mode `download_url`
 *   values, which are also signed redirects.
 *
 *   The fix is to persist a STABLE path that contains only the
 *   attachment id. The server's /api/attachments/{id}/download endpoint
 *   self-resolves the workspace from the attachment row and re-signs (or
 *   proxies) on every request, so the URL is correct forever — until
 *   the attachment row itself is deleted.
 */

const DOWNLOAD_PREFIX = "/api/attachments/";
const DOWNLOAD_SUFFIX = "/download";

/**
 * UUID literal regex (RFC 4122 form). Used to extract an attachment id
 * out of a stable download URL when matching markdown image refs back
 * to their attachment record. Anchored to the segment between
 * `/api/attachments/` and `/download` so we never accept a non-uuid
 * path component as an id.
 */
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * Build the stable per-attachment download URL — the only attachment
 * URL shape that is safe to persist into markdown bodies.
 *
 * Returns a site-relative path (no host) so the same value works in
 * SSR, CSR, Electron, and the mobile webview where the host differs.
 *
 * Callers SHOULD use this when emitting markdown that references an
 * attachment (image src, file-card href, attachment_ids tracker keys).
 * For ad-hoc one-off URLs that don't outlive the current session
 * (e.g. a download click handler that re-signs the URL on every press)
 * the server-returned `download_url` is still appropriate.
 */
export function attachmentDownloadPath(attachmentId: string): string {
  return `${DOWNLOAD_PREFIX}${attachmentId}${DOWNLOAD_SUFFIX}`;
}

/**
 * Extract the attachment id from a `/api/attachments/<uuid>/download`
 * URL (with or without query/host). Returns `undefined` if the URL is
 * not a download URL, or if the captured segment is not a valid UUID
 * literal.
 *
 * The renderer uses this to map a markdown image ref back to its
 * attachment record so it can swap in CloudFront-signed URLs for
 * faster fetches when configured, surface preview metadata, and keep
 * the standalone-attachment dedup logic working when the markdown
 * uses a stable path instead of the storage URL.
 */
export function attachmentIdFromDownloadURL(rawURL: string): string | undefined {
  if (!rawURL) return undefined;

  // Query strings and fragments do not change the stable attachment id.
  let path = rawURL;
  const qi = path.indexOf("?");
  if (qi >= 0) path = path.slice(0, qi);
  const hi = path.indexOf("#");
  if (hi >= 0) path = path.slice(0, hi);

  // Allow absolute URLs (electron, host-bearing CDN URL); pull just the
  // pathname. Skip the protocol-aware parser for site-relative paths so
  // SSR (no document) and webview environments behave identically.
  if (/^https?:\/\//i.test(path)) {
    try {
      path = new URL(path).pathname;
    } catch {
      return undefined;
    }
  }

  if (!path.startsWith(DOWNLOAD_PREFIX)) return undefined;
  if (!path.endsWith(DOWNLOAD_SUFFIX)) return undefined;

  const id = path.slice(DOWNLOAD_PREFIX.length, path.length - DOWNLOAD_SUFFIX.length);
  if (!UUID_RE.test(id)) return undefined;
  return id;
}

/**
 * True when `content` contains the stable markdown reference to
 * `attachment`. The data migration converts persisted raw storage URLs to
 * this path, so every composer and renderer can use the attachment id as the
 * sole matching key.
 */
export function contentReferencesAttachment(
  content: string,
  attachment: { id: string },
): boolean {
  if (!content) return false;
  return content.includes(attachmentDownloadPath(attachment.id));
}
