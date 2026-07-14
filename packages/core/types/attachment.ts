export interface Attachment {
  id: string;
  filename: string;
  url: string;
  download_url: string;
  /**
   * Durable URL the client persists into markdown bodies.
   *
   * The server (`buildMarkdownURL` in server/internal/handler/file.go)
   * computes this per deployment policy:
   *   - public CDN path when storage URL is itself absolute and unsigned;
   *   - otherwise `<MULTICA_PUBLIC_URL>/api/attachments/<id>/download`,
   *     which the server self-resigns / proxies on every request.
   *
   * Distinct from `url` (raw storage URL — may be private / site-relative)
   * and `download_url` (this-response click-time URL — may be a short-lived
   * CloudFront / S3 signed URL with a TTL). `markdown_url` is contracted
   * to be safe to embed in markdown bodies that outlive the current
   * session and to load as a native browser resource fetch on every
   * supported client (web / desktop / mobile webview). MUL-3192.
   *
   * Every current upload produces a persisted attachment row and this URL.
   */
  markdown_url: string;
  content_type: string;
  size_bytes: number;
}
