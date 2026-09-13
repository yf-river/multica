// 本文件实现「图片序列」——某个界面上按顺序提供给预览查看器上/下翻页浏览的图片列表。

// 范围刻意收窄：只处理图片。PDF、视频、音频、markdown、HTML 和文本附件永远不会进入
// 这个序列——混合不同类型就意味着要为一颗可能落到 PDF 页或正在播放的视频上的「下一张」
// 定义每种类型的加载/键盘/状态语义。非图片附件继续各自打开自己的单文件预览。
//
// 序列是从数据构建的，而不是从 DOM：issue 时间线和聊天消息列表都是虚拟化的，所以一个
// 记录已挂载 <img> 节点的注册表会静默丢掉所有滚出窗口的图片。喂给它渲染器消费的同一份
// `{ content, attachments }` 块，就能让顺序和读者看到的完全一致，而不依赖当前画面上画了什么。
//
// 纯函数——无 React、无 DOM、无平台 API。移动端共享它（见 apps/mobile/CLAUDE.md：
// 来自 @multica/core 的纯函数可被导入）。

import type { Attachment } from "../types/attachment";
import {
  attachmentIdFromDownloadURL,
  contentReferencesAttachment,
} from "../types/attachment-url";

// 图片测试的扩展名兜底。镜像 packages/views/editor/utils/preview.ts 里的 IMAGE_EXTS，
// 后者现在委托到这里，让「这是图片吗？」的答案不会在预览派发表和序列构建器之间漂移。
const IMAGE_EXTENSIONS = new Set<string>([
  "png",
  "jpg",
  "jpeg",
  "gif",
  "webp",
  "avif",
  "bmp",
  "ico",
  "svg",
]);

function extensionOf(filename: string): string {
  const base = (filename ?? "").toLowerCase().split(/[\\/]/).pop() ?? "";
  const withoutQuery = base.split(/[?#]/, 1)[0] ?? "";
  const dot = withoutQuery.lastIndexOf(".");
  if (dot <= 0) return "";
  return withoutQuery.slice(dot + 1);
}

function normalizeContentType(contentType: string): string {
  const ct = (contentType ?? "").toLowerCase().trim();
  const semi = ct.indexOf(";");
  return (semi >= 0 ? ct.slice(0, semi) : ct).trim();
}

/**
 * 判断文件是否以图片形式渲染。content-type 优先；对于不带服务端元数据的纯 URL 来源，
 * 扩展名作为兜底。
 *
 * 给实现完整类型派发的调用方的提示：这只是图片分支。`application/pdf`、`video/*` 和
 * `audio/*` 必须先于本判断测试（`.svg` 是文本类 XML，名为 `chart.pdf.png` 的 `.pdf`
 * 不是真实场景）——见 `getPreviewKind`。
 *
 * @param contentType - 文件的 content-type
 * @param filename - 文件名
 * @returns 是否为图片附件
 */
export function isImageAttachment(
  contentType: string,
  filename: string,
): boolean {
  if (normalizeContentType(contentType).startsWith("image/")) return true;
  const ext = extensionOf(filename);
  return ext !== "" && IMAGE_EXTENSIONS.has(ext);
}

// ---------------------------------------------------------------------------
// URL → 附件匹配
// ---------------------------------------------------------------------------

function stripQueryAndFragment(url: string): string {
  return url.split(/[?#]/, 1)[0] ?? "";
}

function matchesAttachmentURL(
  embeddedURL: string,
  attachmentURL?: string,
): boolean {
  if (!embeddedURL || !attachmentURL) return false;
  if (embeddedURL === attachmentURL) return true;
  const embeddedStable = stripQueryAndFragment(embeddedURL);
  const attachmentStable = stripQueryAndFragment(attachmentURL);
  return embeddedStable !== "" && embeddedStable === attachmentStable;
}

/**
 * 把 markdown 正文里嵌入的 URL 解析回对应的附件记录。
 *
 * 两个兜底按顺序——和 `AttachmentDownloadProvider` 用的是同一对（现在与本模块共享，
 * 这样同一个 URL 绝不会出现「下载点击解析到一条记录、序列解析到另一条」）：
 *
 *   1. 从稳定的 `/api/attachments/<id>/download` 形状里提取 id——所有 MUL-3130 之后的
 *      正文都持久化成这种形状。换 host、以及任何附带的 query / fragment 都不影响。
 *   2. 与 `url` / `download_url` / `markdown_url` 做完整 URL 相等比较，用于旧正文和
 *      从未拿到稳定形状的 CDN markdown。
 *
 * @param url - 正文中嵌入的 URL
 * @param attachments - 附件列表（可为空）
 * @returns 匹配到的附件记录，未匹配则返回 undefined
 */
export function matchAttachmentByURL(
  url: string,
  attachments: ReadonlyArray<Attachment> | null | undefined,
): Attachment | undefined {
  if (!url || !attachments?.length) return undefined;
  const idFromUrl = attachmentIdFromDownloadURL(url);
  if (idFromUrl) {
    const byId = attachments.find((a) => a.id === idFromUrl);
    if (byId) return byId;
  }
  return attachments.find(
    (a) =>
      matchesAttachmentURL(url, a.url) ||
      matchesAttachmentURL(url, a.download_url) ||
      matchesAttachmentURL(url, a.markdown_url),
  );
}

/**
 * 返回界面在正文下方以卡片形式渲染的附件——即 markdown 没有在行内引用的那些。
 *
 * 同时丢弃「同一文件已在行内、但以另一条附件行重新上传」的情况（同名 + 同类型 + 同大小）：
 * 读者只看到一张图，所以序列也必须只算一张。
 *
 * @param content - markdown 正文内容（可为空）
 * @param attachments - 附件列表（可为空）
 * @returns 正文未引用的独立附件列表
 */
export function selectStandaloneAttachments(
  content: string | null | undefined,
  attachments: ReadonlyArray<Attachment> | null | undefined,
): Attachment[] {
  if (!attachments?.length) return [];
  if (!content) return [...attachments];
  return attachments.filter((a) => {
    if (contentReferencesAttachment(content, a)) return false;
    const hasSiblingInContent = attachments.some(
      (other) =>
        other.id !== a.id &&
        other.filename === a.filename &&
        other.content_type === a.content_type &&
        other.size_bytes === a.size_bytes &&
        contentReferencesAttachment(content, other),
    );
    return !hasSiblingInContent;
  });
}

// ---------------------------------------------------------------------------
// 行内图片引用
// ---------------------------------------------------------------------------

function blankOut(text: string): string {
  // 换行符保留，这样下面按行锚定的模式能保住行号。
  return text.replace(/[^\n]/g, " ");
}

/**
 * 把每个行内代码和围栏代码块都置空，同时保留偏移量和换行。
 *
 * 不这样做的话，一个在围栏代码块里作为示例展示的 `![shot](url)`——在 agent 解释
 * markdown 的回复里很常见——会被当成真实图片计数，读者就会翻到从未被渲染的内容上。
 *
 * 用逐行扫描而非一个多行正则：在 agent 输出上反向引用的 `(`{3,})[\s\S]*?\1` 是
 * ReDoS 形状，而且这段代码每次 issue/chat 渲染都要跑。
 */
function maskCode(content: string): string {
  const lines = content.split("\n");
  let fence: string | null = null;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i]!;
    const fenceMatch = /^[ \t]{0,3}(`{3,}|~{3,})/.exec(line);
    if (fence !== null) {
      lines[i] = blankOut(line);
      const closes =
        fenceMatch !== null &&
        fenceMatch[1]![0] === fence[0] &&
        fenceMatch[1]!.length >= fence.length;
      if (closes) fence = null;
      continue;
    }
    if (fenceMatch !== null) {
      fence = fenceMatch[1]!;
      lines[i] = blankOut(line);
      continue;
    }
    lines[i] = line.replace(/(`+)[^`\n]*?\1/g, blankOut);
  }
  return lines.join("\n");
}

interface InlineImageRef {
  index: number;
  url: string;
  /** 引用点处可用的最佳文件名提示。 */
  filename: string;
  /**
   * 该引用是否仍需通过图片测试。markdown 的 `![]()` 和 `<img>` 从构造上就是图片
   * （渲染器传 `forceKind: "image"`）；而 `!file[name](url)` 卡片只有在其名称/类型
   * 表明是图片时才以图片渲染。
   */
  requiresImageCheck: boolean;
}

// `![alt](url "title")`。URL 字符类在空白和 `)` 处停止，这样 title 才不会漏进去；
// 可选的 <> 包裹是 CommonMark 为含空格的 URL 提供的转义写法。
const MARKDOWN_IMAGE_RE =
  /!\[([^\]\n]*)\]\(\s*<([^>\n]*)>[^)]*\)|!\[([^\]\n]*)\]\(\s*([^)\s]+)[^)]*\)/g;

// 原始 `<img src=...>`——可达，因为只读渲染器运行 rehypeRaw + rehypeSanitize，
// 而 sanitize schema 保留了 img。
const HTML_IMAGE_RE =
  /<img\b[^>]*?\ssrc\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s">']+))[^>]*>/gi;

// 独占一行的 `!file[name](url)`——`preprocessFileCards` 会把它变成 fileCard div，
// 渲染器再交给 <Attachment>；那里的图片文件名会渲染成行内图片，而不是卡片。
const FILE_CARD_LINE_RE = /^[ \t]*!file\[((?:\\.|[^\]\\\n])*)\]\(([^)\s]+)\)[ \t]*$/gm;

function unescapeLabel(label: string): string {
  return label.replace(/\\([[\]\\()])/g, "$1");
}

function extractInlineImageRefs(rawContent: string): InlineImageRef[] {
  if (!rawContent) return [];
  const content = maskCode(rawContent);
  const refs: InlineImageRef[] = [];

  for (const m of content.matchAll(MARKDOWN_IMAGE_RE)) {
    // 两个分支：`<...>` 包裹的 URL（组 1/2）或裸 URL（组 3/4）。
    const alt = m[1] ?? m[3] ?? "";
    const url = (m[2] ?? m[4] ?? "").trim();
    if (!url) continue;
    refs.push({
      index: m.index ?? 0,
      url,
      filename: unescapeLabel(alt),
      requiresImageCheck: false,
    });
  }

  for (const m of content.matchAll(HTML_IMAGE_RE)) {
    const url = (m[1] ?? m[2] ?? m[3] ?? "").trim();
    if (!url) continue;
    refs.push({
      index: m.index ?? 0,
      url,
      filename: "",
      requiresImageCheck: false,
    });
  }

  for (const m of content.matchAll(FILE_CARD_LINE_RE)) {
    const url = (m[2] ?? "").trim();
    if (!url) continue;
    refs.push({
      index: m.index ?? 0,
      url,
      filename: unescapeLabel(m[1] ?? ""),
      requiresImageCheck: true,
    });
  }

  return refs.sort((a, b) => a.index - b.index);
}

// ---------------------------------------------------------------------------
// 序列
// ---------------------------------------------------------------------------

/** 查看器可以翻到的一张图片。 */
export interface ImageSequenceItem {
  /**
   * 用于在点击的图片处打开序列的标识。当引用能解析到记录时为附件 id，
   * 否则为正文中写出的 URL——与渲染器在点击时刻所知的一致。
   */
  key: string;
  /** 已知的最佳 URL。持有 `attachment` 的调用方应优先重新解析。 */
  url: string;
  filename: string;
  /** 当引用解析到工作区附件记录时存在。 */
  attachment?: Attachment;
}

/** 一个可渲染单元：一条 issue 描述、一条评论、一条聊天消息。 */
export interface ImageSequenceBlock {
  content?: string | null;
  attachments?: ReadonlyArray<Attachment> | null;
}

/**
 * 把若干块展平成查看器按序遍历的有序图片序列。
 *
 * 顺序即渲染顺序：对每个块，先是正文中按文本顺序的行内图片，再是它下面渲染的
 * 独立附件卡片。同一张图片的重复引用坍缩到第一次出现的位置，这样计数匹配的是
 * 「不同图片的数量」而非「引用的数量」。
 *
 * @param blocks - 有序的渲染块列表（元素可为空）
 * @returns 有序图片序列
 */
export function collectImageSequence(
  blocks: ReadonlyArray<ImageSequenceBlock | null | undefined>,
): ImageSequenceItem[] {
  const items: ImageSequenceItem[] = [];
  const seen = new Set<string>();

  const push = (item: ImageSequenceItem) => {
    if (!item.key || seen.has(item.key)) return;
    seen.add(item.key);
    items.push(item);
  };

  for (const block of blocks) {
    if (!block) continue;
    const content = block.content ?? "";
    const attachments = block.attachments ?? [];

    for (const ref of extractInlineImageRefs(content)) {
      const attachment = matchAttachmentByURL(ref.url, attachments);
      if (ref.requiresImageCheck) {
        const isImage = attachment
          ? isImageAttachment(attachment.content_type, attachment.filename)
          : isImageAttachment("", ref.filename || ref.url);
        if (!isImage) continue;
      }
      push({
        key: attachment?.id ?? ref.url,
        url:
          attachment?.download_url ||
          attachment?.markdown_url ||
          attachment?.url ||
          ref.url,
        filename: attachment?.filename || ref.filename,
        attachment,
      });
    }

    for (const attachment of selectStandaloneAttachments(content, attachments)) {
      if (!isImageAttachment(attachment.content_type, attachment.filename)) {
        continue;
      }
      push({
        key: attachment.id,
        url:
          attachment.download_url || attachment.markdown_url || attachment.url,
        filename: attachment.filename,
        attachment,
      });
    }
  }

  return items;
}

/**
 * 返回 `key` 在序列中的下标，不在序列中时返回 -1。
 *
 * @param items - 图片序列
 * @param key - 要查找的图片标识
 * @returns 下标或 -1
 */
export function indexOfImageKey(
  items: ReadonlyArray<ImageSequenceItem>,
  key: string,
): number {
  if (!key) return -1;
  return items.findIndex((item) => item.key === key);
}
