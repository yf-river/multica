/**
 * AttachmentPreviewModal 的预览类型分发表。
 *
 * 新增可预览类型在这里加。要增加一个类型：
 *   1. 新增一个分支，返回新的 PreviewKind 字面量。
 *   2. 在 attachment-preview-modal.tsx 的分发逻辑里加对应渲染器。
 *   3. 渲染器需要以文本形式读取文件正文的话，还要同步扩展
 *      server/internal/handler/file.go 里的 isTextPreviewable，
 *      让代理接口放行它。
 *   4. 渲染器拉取二进制的话，想清楚用 download_url
 *      （CloudFront，客户端无鉴权）还是一个新的带鉴权代理。
 */

import { isImageAttachment } from "@multica/core/attachments/image-sequence";

/**
 * 附件预览类型：决定弹窗用哪个渲染器展示附件。
 */
export type PreviewKind =
  | "image"
  | "pdf"
  | "video"
  | "audio"
  | "markdown"
  | "html"
  | "text";

/**
 * 扩展名 → hljs 语言标记 的对照表。值给到 lowlight 的 common 语言包。
 */
const EXT_LANGUAGE_MAP: Record<string, string> = {
  // Markdown 文档
  md: "markdown",
  markdown: "markdown",
  // 纯文本——txt 特意不映射到高亮语言，map 里没有对应语言时
  // lowlight 会原样输出正文（不染色）。
  txt: "plaintext",
  log: "plaintext",
  // Web 相关
  html: "xml",
  htm: "xml",
  xml: "xml",
  svg: "xml",
  css: "css",
  scss: "scss",
  sass: "scss",
  less: "less",
  // 配置 / 数据
  json: "json",
  yml: "yaml",
  yaml: "yaml",
  toml: "ini",
  ini: "ini",
  conf: "ini",
  dockerfile: "dockerfile",
  makefile: "makefile",
  gitignore: "plaintext",
  // Shell 脚本
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  // 编程语言
  py: "python",
  rb: "ruby",
  go: "go",
  rs: "rust",
  ts: "typescript",
  tsx: "typescript",
  js: "javascript",
  jsx: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  java: "java",
  kt: "kotlin",
  swift: "swift",
  c: "c",
  cc: "cpp",
  cpp: "cpp",
  h: "c",
  hpp: "cpp",
  cs: "csharp",
  php: "php",
  lua: "lua",
  vim: "vim",
  sql: "sql",
  csv: "plaintext",
  tsv: "plaintext",
};

// 一些常见「没有扩展名」的构建文件，按文件名兜底识别。
const BASENAME_LANGUAGE_MAP: Record<string, string> = {
  dockerfile: "dockerfile",
  makefile: "makefile",
  ".env": "plaintext",
  ".gitignore": "plaintext",
};

// 重要——必须与 server/internal/handler/file.go 的 isTextPreviewable() 保持
// 同步。若扩展名在这里但代理接口拒绝了，用户会在弹窗里看到 415 兜底；
// 反过来代理放行而这里没有，眼睛按钮干脆就不出现。
//
// TODO(后续): 抽成 JSON 单一事实来源 + 生成器（参照 server/internal/
// handler/reserved_slugs.json 的保留字模式）。
const TEXT_EXTENSIONS = new Set<string>([
  "md", "markdown", "txt", "log", "csv", "tsv",
  "html", "htm", "json", "xml",
  "yml", "yaml", "toml", "ini", "conf",
  "dockerfile", "makefile", "gitignore",
  "sh", "bash", "zsh",
  "py", "rb", "go", "rs",
  "ts", "tsx", "js", "jsx", "mjs", "cjs",
  "css", "scss", "sass", "less",
  "sql",
  "java", "kt", "swift",
  "c", "cc", "cpp", "h", "hpp",
  "cs", "php", "lua", "vim",
]);

const TEXT_CONTENT_TYPES = new Set<string>([
  "application/json",
  "application/javascript",
  "application/xml",
  "application/x-yaml",
  "application/yaml",
  "application/toml",
  "application/x-sh",
  "application/x-httpd-php",
]);

const TEXT_BASENAMES = new Set<string>([
  "dockerfile",
  "makefile",
  ".env",
  ".gitignore",
]);

// 媒体类型没有 contentType 时的扩展名兜底（预览源只有 URL、拿不到
// 服务端元数据的情形）。
const VIDEO_EXTS = new Set<string>([
  "mp4", "m4v", "mov", "webm", "mkv", "avi", "ogv",
]);
const AUDIO_EXTS = new Set<string>([
  "mp3", "wav", "m4a", "ogg", "oga", "flac", "aac", "opus",
]);
// 图片识别放在 @multica/core/attachments/image-sequence——画廊序列构造器
// 需要同一个答案，且它要与移动端共享，而移动端无法 import packages/views。

function extOf(filename: string): string {
  const base = filename.toLowerCase().split(/[\\/]/).pop() ?? "";
  const dot = base.lastIndexOf(".");
  if (dot <= 0) return "";
  return base.slice(dot + 1);
}

function baseOf(filename: string): string {
  return (filename.toLowerCase().split(/[\\/]/).pop() ?? "").trim();
}

function normalizeContentType(contentType: string): string {
  const ct = (contentType ?? "").toLowerCase().trim();
  const semi = ct.indexOf(";");
  return (semi >= 0 ? ct.slice(0, semi) : ct).trim();
}

function isTextLike(contentType: string, filename: string): boolean {
  const ct = normalizeContentType(contentType);
  if (ct.startsWith("text/")) return true;
  if (TEXT_CONTENT_TYPES.has(ct)) return true;
  const ext = extOf(filename);
  if (ext && TEXT_EXTENSIONS.has(ext)) return true;
  return TEXT_BASENAMES.has(baseOf(filename));
}

// 依据 PreviewKind 分发。新类型去 attachment-preview-modal.tsx 加；
// 记住弹窗外框（标题栏、关闭、下载 CTA、ESC 处理）是共用的——子渲染器
// 只负责内容区。
/**
 * 根据 contentType + filename 判定附件属于哪种可预览类型。
 * @returns PreviewKind；无法预览时返回 null。
 */
export function getPreviewKind(
  contentType: string,
  filename: string,
): PreviewKind | null {
  const ct = normalizeContentType(contentType);

  const ext = extOf(filename);

  if (ct === "application/pdf" || ext === "pdf") return "pdf";
  if (ct.startsWith("video/") || (ext && VIDEO_EXTS.has(ext))) return "video";
  if (ct.startsWith("audio/") || (ext && AUDIO_EXTS.has(ext))) return "audio";

  // 图片判断必须放在 html/text 分支之前：svg 是文本类（XML），且 image/*
  // 的 content-type 里有 text/svg 变体，会被 isTextLike 抢先判走。
  if (isImageAttachment(contentType, filename)) return "image";

  // Markdown——既覆盖类型标注正确的情形，也覆盖服务端嗅探器常见的
  // 兜底（.md 被报成 text/plain）。
  if (ct === "text/markdown" || ext === "md" || ext === "markdown") {
    return "markdown";
  }
  if (ct === "text/html" || ext === "html" || ext === "htm") {
    return "html";
  }

  if (isTextLike(contentType, filename)) return "text";
  return null;
}

/**
 * 附件是否可预览（即能否得到一个非 null 的预览类型）。
 */
export function isPreviewable(contentType: string, filename: string): boolean {
  return getPreviewKind(contentType, filename) !== null;
}

// 取文件的 hljs 语言标记。文件没有可识别扩展名时返回 undefined——调用方
// 退回纯 `<pre>` 渲染即可。刻意保持又小又只认扩展名：lowlight 的 `common`
// 包已覆盖人们实际会上传的约 50 种语言，其余类型当纯文本渲染也比导入整个
// 语言包强。
/**
 * 把文件名映射成代码高亮语言标记。
 * @returns hljs/lowlight 语言名；无法识别时返回 undefined。
 */
export function extensionToLanguage(filename: string): string | undefined {
  const ext = extOf(filename);
  if (ext && EXT_LANGUAGE_MAP[ext]) return EXT_LANGUAGE_MAP[ext];
  const base = baseOf(filename);
  if (BASENAME_LANGUAGE_MAP[base]) return BASENAME_LANGUAGE_MAP[base];
  return undefined;
}
