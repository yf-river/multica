// 本文件定义「附件（Attachment）」类型：文件元数据及其多种下载/内嵌 URL。

export interface Attachment {
  id: string;
  workspace_id: string;
  issue_id: string | null;
  comment_id: string | null;
  chat_session_id: string | null;
  chat_message_id: string | null;
  uploader_type: string;
  uploader_id: string;
  filename: string;
  url: string;
  download_url: string;
  /**
   * 「强制下载」按钮 URL：无需凭证，并且与 `download_url`（加载意图，媒体
   * 以内联预览提供）不同，它在所有存储模式下都带 Content-Disposition:
   * attachment。短时效——绝不要持久化它。可选字段：早于该字段的服务器省略
   * 它，下载调用方回退到 `download_url` / 稳定端点。
   */
  attachment_download_url?: string;
  /**
   * 客户端持久化进 markdown 正文用的耐用 URL。
   *
   * 服务器（server/internal/handler/file.go 的 buildMarkdownURL）按每次部署
   * 的策略计算它：
   *   - 存储 URL 本身是绝对且未签名时，返回公开 CDN 路径；
   *   - 否则返回 `<MULTICA_PUBLIC_URL>/api/attachments/<id>/download`，
   *     服务器对每次请求自行重签名 / 代理。
   *
   * 与 `url`（原始存储 URL——可能私有 / 站点相对）和 `download_url`
   *（本次响应的点击时 URL——可能是带 TTL 的短时 CloudFront / S3 签名 URL）
   * 都不同。`markdown_url` 契约保证：可安全嵌入到比当前会话存活更久的
   * markdown 正文，并且能在所有支持的客户端（web / desktop / 移动 webview）
   * 上作为原生浏览器资源加载。MUL-3192。
   *
   * 当响应来自早于该字段的服务器，或来自未产生持久附件行的上传路径
   *（如无 workspace 的头像分支）时为空。前端需要把 URL 嵌入 markdown 时应
   * 使用 useFileUpload 里的辅助函数而不是直接读此字段，让旧回退
   *（下载路径 / `att.url`）集中在一处。
   */
  markdown_url: string;
  content_type: string;
  size_bytes: number;
  created_at: string;
}
