// 本文件提供「附件 URL」的稳定引用辅助函数：生成 / 解析持久化到 markdown
// 正文（议题描述、评论正文、聊天消息）里的附件路径。
//
// 背景 —— MUL-3130：
//
//   最初的传文件流程把 `att.url` 作为要嵌入 markdown 的链接。PR #3903
//  （MUL-3132）之后，LocalStorage 后端的 `att.url` 变成了 30 分钟有效的
//   HMAC 签名 URL `/uploads/<key>?exp&sig`。该 URL 天生短命——它是让原生
//   浏览器 <img>/<video> 无需 Authorization 头也能加载资源的内存态 token。
//   把它存进评论正文意味着签名一过期（默认 30 分钟）评论里的图就裂了。
//   CloudFront 模式下的 `download_url` 也有同样的问题，它们也是签名跳转。
//
//   修复方式是持久化一条只含附件 id 的「稳定路径」。服务器的
//   /api/attachments/{id}/download 端点会从附件行自行解析 workspace 并在每次
//   请求时重新签名（或代理），所以该 URL 一直有效——直到附件行本身被删除。

const DOWNLOAD_PREFIX = "/api/attachments/";
const DOWNLOAD_SUFFIX = "/download";
const CHANNEL_MEDIA_MARKER_RE =
  /<!-- multica:channel-media:[0-9a-fA-F-]{36} -->/g;

/** 从可编辑 / 已渲染的 Markdown 里移除服务端归属的「频道媒体」合并元数据，
 * 同时保留可见的附件链接不动。 */
export function stripChannelMediaMarkers(content: string): string {
  return content.replace(CHANNEL_MEDIA_MARKER_RE, "");
}

/**
 * UUID 字面量正则（RFC 4122 形式）。用于把 markdown 图片引用反向匹配回其
 * 附件记录时，从稳定下载 URL 里抽取附件 id。锚定在 `/api/attachments/` 与
 * `/download` 之间的片段上，因此绝不会把非 uuid 路径段当成 id。
 */
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * 构造每个附件稳定的下载 URL——唯一「可以安全持久化进 markdown 正文」的
 * 附件 URL 形状。
 *
 * 返回站内相对路径（不带主机），让同一值在 SSR、CSR 与主机各异的移动
 * webview 里都可用。
 *
 * 当 markdown 引用附件（图片 src、文件卡片 href、附件 id 跟踪键）时应使用
 * 本函数。对不跨会话存活的临时 URL（如每次点击都会重新签名的下载处理器），
 * 服务器返回的 `download_url` 仍然适用。
 */
export function attachmentDownloadPath(attachmentId: string): string {
  return `${DOWNLOAD_PREFIX}${attachmentId}${DOWNLOAD_SUFFIX}`;
}

/**
 * 从 `/api/attachments/<uuid>/download` URL（可带查询串或主机）里抽取附件 id。
 * 若该 URL 不是下载 URL、或中间段不是合法 UUID 字面量，返回 `undefined`。
 *
 * 渲染器用它在 markdown 图片引用与附件记录之间建立映射：以便在配置了
 * CloudFront 签名 URL 时换成更快的抓取、展示预览元数据，并在 markdown 使用
 * 稳定路径而非存储 URL 时保持独立附件的去重逻辑工作。
 */
export function attachmentIdFromDownloadURL(rawURL: string): string | undefined {
  if (!rawURL) return undefined;

  // 先剥掉查询串与片段，避免 `?exp=...`（旧客户端误嵌入的过期签名 URL）
  // 污染匹配。
  let path = rawURL;
  const qi = path.indexOf("?");
  if (qi >= 0) path = path.slice(0, qi);
  const hi = path.indexOf("#");
  if (hi >= 0) path = path.slice(0, hi);

  // 允许绝对 URL（带主机的 API 或 CDN URL），只取 pathname。对站内相对路径
  // 跳过协议解析，使 SSR（无 document）与 webview 环境行为一致。
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

/** 去掉 URL 的查询串与片段，只留主体部分。 */
function stripQueryAndFragment(url: string): string {
  return url.split(/[?#]/, 1)[0] ?? "";
}

/** `content` 里是否出现过 `url`（或其去查询串的主体形态）。 */
function contentReferencesURL(content: string, url?: string): boolean {
  if (!url) return false;
  if (content.includes(url)) return true;
  const stable = stripQueryAndFragment(url);
  return stable !== "" && content.includes(stable);
}

/**
 * `content` 是否包含对 `attachment` 的 markdown 引用——无论是新版稳定的
 * `/api/attachments/<id>/download` 形态，还是旧的 `att.url` 存储路径。
 * 评论编辑器与独立附件列表用它判断某个附件是否「已在正文里」（此时应把它
 * 当作评论附件跟踪，但不再在下方渲染成卡片）。
 *
 * 两种检查必须都做，因为：
 *   - MUL-3130 后新客户端上传会写稳定 URL
 *   - 编辑一条 MUL-3130 前的旧评论时，`entry.content` 里可能仍是旧 URL
 *   - 混合内容（一张图在修复前上传、另一张在修复后上传）在滚动发布窗口内
 *     是可能的
 */
export function contentReferencesAttachment(
  content: string,
  attachment: {
    id: string;
    url: string;
    download_url?: string;
    markdown_url?: string;
  },
): boolean {
  if (!content) return false;
  if (content.includes(attachmentDownloadPath(attachment.id))) return true;
  if (contentReferencesURL(content, attachment.url)) return true;
  if (contentReferencesURL(content, attachment.download_url)) return true;
  if (contentReferencesURL(content, attachment.markdown_url)) return true;
  return false;
}
