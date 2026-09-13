// 本文件负责「技能包归档」的组装与校验：限制技能文件/包大小与数量、从目录
// 选择或已有归档里挑出合法成员（应用忽略/二进制/超限规则）、就地打成 ZIP
// （STORE 无压缩），并解析 SKILL.md 的 frontmatter 用于预览信息。
// 规则与 Go 侧技能导入器（server/internal/skill / skill_import_archive.go）
// 保持一致。

import { parseFrontmatter } from "./frontmatter";

/** 单个技能文件最大字节数（镜像 server/internal/handler 的 maxImportFileSize）。 */
export const MAX_SKILL_FILE_BYTES = 1 << 20; // 1 MiB
/** 支撑文件总字节上限（镜像 maxImportTotalSize）。 */
export const MAX_SKILL_BUNDLE_BYTES = 8 << 20; // 8 MiB
/** 支撑文件数量上限（镜像 maxImportFileCount）。 */
export const MAX_SKILL_FILE_COUNT = 256;
/** 归档上传总大小上限（镜像 maxImportArchiveUploadSize）。 */
export const MAX_SKILL_ARCHIVE_BYTES = 16 << 20; // 16 MiB

const SKILL_MD = "skill.md";

/** 一条归档成员：相对路径 + 字节内容。 */
export type SkillArchiveEntry = {
  relativePath: string;
  data: Uint8Array;
};

/** 技能包准备失败的错误种类。 */
export type PreparedSkillArchiveError =
  | "missing_skill_md"
  | "empty"
  | "too_large"
  | "too_many_files";

/** 技能包准备结果：成功带回 File 与预览信息；失败带回错误码。 */
export type PreparedSkillArchive =
  | {
      ok: true;
      file: File;
      preview: {
        displayName: string;
        skillName: string;
        description: string;
        fileCount: number | null;
        source: "folder" | "archive";
      };
    }
  | { ok: false; error: PreparedSkillArchiveError };

/** 文件名是否像技能包归档（.skill 或 .zip）。 */
export function isSkillArchiveFilename(name: string): boolean {
  const lower = name.toLowerCase();
  return lower.endsWith(".skill") || lower.endsWith(".zip");
}

/** 直接复用一份已存在的技能归档：只校验总大小，其余交给服务端解析。 */
export function wrapExistingSkillArchive(file: File): PreparedSkillArchive {
  if (file.size > MAX_SKILL_ARCHIVE_BYTES) {
    return { ok: false, error: "too_large" };
  }
  const base = file.name.replace(/\\/g, "/").split("/").pop() ?? file.name;
  const skillName = stripArchiveExtension(base) || "skill";
  return {
    ok: true,
    file,
    preview: {
      displayName: base,
      skillName,
      description: "",
      fileCount: null,
      source: "archive",
    },
  };
}

/**
 * 从内存里的归档条目准备技能包。
 *
 * @param entries - 原始归档成员列表
 * @returns 组装好的技能包；失败时返回错误码
 */
export function prepareSkillArchiveFromEntries(
  entries: SkillArchiveEntry[],
): PreparedSkillArchive {
  const data = new Map<string, Uint8Array>();
  const members: SkillArchiveMemberMeta[] = [];
  for (const entry of entries) {
    const path = normalizeRelPath(entry.relativePath);
    members.push({ path, size: entry.data.byteLength });
    if (!data.has(path)) data.set(path, entry.data);
  }

  const picked = selectSkillArchiveMembers(members);
  if (!picked.ok) return picked;
  return buildPreparedArchive(picked, (path) => data.get(path) ?? EMPTY_BYTES);
}

const EMPTY_BYTES = new Uint8Array();

/**
 * 两条准备路径共用的收尾：把 `selectSkillArchiveMembers` 已批准的成员打成 zip，
 * 并推导预览信息。`dataFor` 提供选中路径的字节——entries 路径在内存里持有
 * 它们，picker 路径只在选择完成后才读。
 */
function buildPreparedArchive(
  selection: SkillArchiveSelection,
  dataFor: (path: string) => Uint8Array,
): PreparedSkillArchive {
  const packed = selection.selected.map((member) => ({
    path: member.path,
    data: dataFor(member.path),
  }));

  const content = new TextDecoder("utf-8").decode(dataFor(selection.skillMdPath));
  const { frontmatter } = parseFrontmatter(content);
  const wrapperName = wrapperNameFromPrefix(selection.prefix);
  const skillName = frontmatter?.name?.trim() || wrapperName || "skill";
  const description = frontmatter?.description?.trim() ?? "";
  const zip = packStoreZip(packed);
  if (zip.byteLength > MAX_SKILL_ARCHIVE_BYTES) {
    return { ok: false, error: "too_large" };
  }

  const filename = `${sanitizeArchiveBasename(skillName)}.skill`;
  const file = new File([uint8ToArrayBuffer(zip)], filename, {
    type: "application/zip",
  });
  return {
    ok: true,
    file,
    preview: {
      displayName: wrapperName || skillName,
      skillName,
      description,
      fileCount: packed.length,
      source: "folder",
    },
  };
}

/** 一条待选成员的元信息（路径已规范化 + 字节数）。 */
export type SkillArchiveMemberMeta = {
  path: string;
  size: number;
};

/** 一次成员选择的结果。 */
export type SkillArchiveSelection = {
  /** 要打包的成员，按输入顺序、带规范化路径。 */
  selected: SkillArchiveMemberMeta[];
  /** 作为技能根的那份 SKILL.md 的规范化路径。 */
  skillMdPath: string;
  /** 技能根所在目录前缀；SKILL.md 就在根上时为 ""。 */
  prefix: string;
};

/**
 * 忽略规则与大小/数量限制唯一生效的地方。仅凭路径 + 大小做成员判定——不做
 * I/O——所以 picker 路径可以在读目录之前就拒绝它，而两条准备路径通过「共用
 * 这一处」保持同步，而不是各自维护一份规则副本。镜像服务端导入器。
 */
export function selectSkillArchiveMembers(
  files: SkillArchiveMemberMeta[],
):
  | ({ ok: true } & SkillArchiveSelection)
  | { ok: false; error: PreparedSkillArchiveError } {
  if (files.length === 0) return { ok: false, error: "empty" };

  const normalized: SkillArchiveMemberMeta[] = [];
  for (const file of files) {
    const path = normalizeRelPath(file.path);
    if (!path || isIgnoredArchiveEntry(path)) continue;
    normalized.push({ path, size: file.size });
  }
  if (normalized.length === 0) return { ok: false, error: "empty" };

  let skillMd: (SkillArchiveMemberMeta & { prefix: string }) | null = null;
  for (const file of normalized) {
    if (!isSkillMdPath(file.path)) continue;
    const prefix = archiveEntryPrefix(file.path);
    if (!skillMd || prefix.length < skillMd.prefix.length) {
      skillMd = { ...file, prefix };
    }
  }
  if (!skillMd) return { ok: false, error: "missing_skill_md" };
  if (skillMd.size > MAX_SKILL_FILE_BYTES) {
    return { ok: false, error: "too_large" };
  }

  const selected: SkillArchiveMemberMeta[] = [];
  let supportingCount = 0;
  let supportingBytes = 0;
  for (const file of normalized) {
    if (skillMd.prefix && !file.path.startsWith(skillMd.prefix)) continue;
    const rel = skillMd.prefix ? file.path.slice(skillMd.prefix.length) : file.path;
    if (!rel || rel.endsWith("/")) continue;
    if (isSkillMdPath(rel)) {
      selected.push(file);
      continue;
    }
    if (isLikelyBinaryFilePath(rel)) continue;
    if (file.size > MAX_SKILL_FILE_BYTES) continue;
    supportingCount += 1;
    if (supportingCount > MAX_SKILL_FILE_COUNT) {
      return { ok: false, error: "too_many_files" };
    }
    supportingBytes += file.size;
    if (supportingBytes > MAX_SKILL_BUNDLE_BYTES) {
      return { ok: false, error: "too_large" };
    }
    selected.push(file);
  }
  return {
    ok: true,
    selected,
    skillMdPath: skillMd.path,
    prefix: skillMd.prefix,
  };
}

/** 取 picker 文件的相对路径（webkitRelativePath 不可用时退回文件名）。 */
function pickerRelativePath(file: File): string {
  const rel =
    typeof file.webkitRelativePath === "string" && file.webkitRelativePath
      ? file.webkitRelativePath
      : file.name;
  return normalizeRelPath(rel);
}

/**
 * 打包一次「目录选择器」的结果。被忽略的、二进制的、超限的和出根的文件，会
 * 在任何 `arrayBuffer()` 调用前就按「路径 + File.size」被丢弃。
 */
export async function prepareSkillArchiveFromPickerFiles(
  files: File[],
): Promise<PreparedSkillArchive> {
  const sources = new Map<string, File>();
  const members: SkillArchiveMemberMeta[] = [];
  for (const file of files) {
    const path = pickerRelativePath(file);
    members.push({ path, size: file.size });
    if (!sources.has(path)) sources.set(path, file);
  }

  const picked = selectSkillArchiveMembers(members);
  if (!picked.ok) return picked;

  const data = new Map<string, Uint8Array>();
  for (const member of picked.selected) {
    if (data.has(member.path)) continue;
    const source = sources.get(member.path);
    data.set(
      member.path,
      source ? new Uint8Array(await source.arrayBuffer()) : EMPTY_BYTES,
    );
  }
  return buildPreparedArchive(picked, (path) => data.get(path) ?? EMPTY_BYTES);
}

// --- zip（STORE / 无压缩）-------------------------------------------------

const CRC32_TABLE = (() => {
  const table = new Uint32Array(256);
  for (let i = 0; i < 256; i++) {
    let c = i;
    for (let j = 0; j < 8; j++) {
      c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    }
    table[i] = c >>> 0;
  }
  return table;
})();

/** 计算一段数据的 CRC-32 校验值。 */
function crc32(data: Uint8Array): number {
  let c = 0xffffffff;
  for (let i = 0; i < data.length; i++) {
    c = CRC32_TABLE[(c ^ data[i]!) & 0xff]! ^ (c >>> 8);
  }
  return (c ^ 0xffffffff) >>> 0;
}

/** 字符串按 UTF-8 编码。 */
function utf8(s: string): Uint8Array {
  return new TextEncoder().encode(s);
}

/** Uint8Array → ArrayBuffer（拷贝，避免共享底层缓冲）。 */
function uint8ToArrayBuffer(data: Uint8Array): ArrayBuffer {
  const copy = new Uint8Array(data.byteLength);
  copy.set(data);
  return copy.buffer;
}

/**
 * 打一个无压缩 zip。服务端解析器（`parseSkillArchive`）接受 STORE 归档；
 * 跳过压缩让这个辅助函数保持零依赖。
 */
export function packStoreZip(entries: { path: string; data: Uint8Array }[]): Uint8Array {
  const files = entries.map((e) => ({
    name: utf8(e.path.replace(/\\/g, "/")),
    data: e.data,
    crc: crc32(e.data),
  }));

  let localSize = 0;
  let centralSize = 0;
  for (const f of files) {
    localSize += 30 + f.name.length + f.data.length;
    centralSize += 46 + f.name.length;
  }
  const out = new Uint8Array(localSize + centralSize + 22);
  const view = new DataView(out.buffer);
  let offset = 0;
  const localOffsets: number[] = [];

  for (const f of files) {
    localOffsets.push(offset);
    view.setUint32(offset, 0x04034b50, true);
    view.setUint16(offset + 4, 20, true);
    view.setUint16(offset + 6, 0x0800, true); // UTF-8 文件名
    view.setUint16(offset + 8, 0, true); // STORE（无压缩）
    view.setUint16(offset + 10, 0, true);
    view.setUint16(offset + 12, 0, true);
    view.setUint32(offset + 14, f.crc, true);
    view.setUint32(offset + 18, f.data.length, true);
    view.setUint32(offset + 22, f.data.length, true);
    view.setUint16(offset + 26, f.name.length, true);
    view.setUint16(offset + 28, 0, true);
    out.set(f.name, offset + 30);
    out.set(f.data, offset + 30 + f.name.length);
    offset += 30 + f.name.length + f.data.length;
  }

  const centralStart = offset;
  for (let i = 0; i < files.length; i++) {
    const f = files[i]!;
    view.setUint32(offset, 0x02014b50, true);
    view.setUint16(offset + 4, 20, true);
    view.setUint16(offset + 6, 20, true);
    view.setUint16(offset + 8, 0x0800, true);
    view.setUint16(offset + 10, 0, true);
    view.setUint16(offset + 12, 0, true);
    view.setUint16(offset + 14, 0, true);
    view.setUint32(offset + 16, f.crc, true);
    view.setUint32(offset + 20, f.data.length, true);
    view.setUint32(offset + 24, f.data.length, true);
    view.setUint16(offset + 28, f.name.length, true);
    view.setUint16(offset + 30, 0, true);
    view.setUint16(offset + 32, 0, true);
    view.setUint16(offset + 34, 0, true);
    view.setUint16(offset + 36, 0, true);
    view.setUint32(offset + 38, 0, true);
    view.setUint32(offset + 42, localOffsets[i]!, true);
    out.set(f.name, offset + 46);
    offset += 46 + f.name.length;
  }

  view.setUint32(offset, 0x06054b50, true);
  view.setUint16(offset + 4, 0, true);
  view.setUint16(offset + 6, 0, true);
  view.setUint16(offset + 8, files.length, true);
  view.setUint16(offset + 10, files.length, true);
  view.setUint32(offset + 12, offset - centralStart, true);
  view.setUint32(offset + 16, centralStart, true);
  view.setUint16(offset + 20, 0, true);
  return out;
}

// --- 路径辅助（镜像 skill_import_archive.go）------------------------------

/** 规范化相对路径：反斜杠变正斜杠、去掉前导斜杠。 */
function normalizeRelPath(p: string): string {
  return p.replace(/\\/g, "/").replace(/^\/+/, "");
}

/** 路径基底（去掉目录后的文件名）是否为 skill.md（大小写不敏感）。 */
function isSkillMdPath(path: string): boolean {
  const base = path.split("/").pop() ?? "";
  return base.toLowerCase() === SKILL_MD;
}

/** 取路径所在目录前缀（含尾部斜杠）；没有目录时返回空串。 */
function archiveEntryPrefix(cleanName: string): string {
  const i = cleanName.lastIndexOf("/");
  if (i <= 0) return "";
  return cleanName.slice(0, i + 1);
}

/** 从前缀里推导外层目录名（用作展示名）；无目录或名字为 . / .. 时返回空串。 */
function wrapperNameFromPrefix(prefix: string): string {
  const trimmed = prefix.replace(/\/+$/, "");
  if (!trimmed) return "";
  const base = trimmed.split("/").pop() ?? "";
  if (base === "." || base === "..") return "";
  return base;
}

/** 该相对路径是否该被忽略：空段、__MACOSX 目录、任何以点开头的段，或名为
 * license / license.md / license.txt 的文件。 */
export function isIgnoredArchiveEntry(rel: string): boolean {
  const segments = rel.split("/");
  for (const seg of segments) {
    if (seg === "" || seg === "__MACOSX" || seg.startsWith(".")) return true;
  }
  const base = (segments[segments.length - 1] ?? "").toLowerCase();
  return base === "license" || base === "license.md" || base === "license.txt";
}

/** 扩展名是否大概率是二进制文件（图片/字体/压缩包/Office 文档/音视频等）。 */
export function isLikelyBinaryFilePath(path: string): boolean {
  const ext = extensionOf(path);
  switch (ext) {
    case ".png":
    case ".jpg":
    case ".jpeg":
    case ".gif":
    case ".webp":
    case ".bmp":
    case ".tiff":
    case ".ico":
    case ".heic":
    case ".ttf":
    case ".otf":
    case ".woff":
    case ".woff2":
    case ".eot":
    case ".zip":
    case ".gz":
    case ".tar":
    case ".bz2":
    case ".7z":
    case ".rar":
    case ".pdf":
    case ".docx":
    case ".xlsx":
    case ".pptx":
    case ".doc":
    case ".xls":
    case ".ppt":
    case ".mp3":
    case ".mp4":
    case ".wav":
    case ".avi":
    case ".mov":
    case ".webm":
    case ".m4a":
    case ".flac":
    case ".exe":
    case ".dll":
    case ".so":
    case ".dylib":
    case ".class":
    case ".jar":
    case ".wasm":
    case ".db":
    case ".sqlite":
    case ".sqlite3":
    case ".pyc":
      return true;
    default:
      return false;
  }
}

/** 取路径的小写扩展名（含点）；无扩展名返回空串。 */
function extensionOf(path: string): string {
  const base = path.split("/").pop() ?? path;
  const i = base.lastIndexOf(".");
  if (i <= 0) return "";
  return base.slice(i).toLowerCase();
}

/** 去掉 .skill / .zip 后缀（大小写不敏感）。 */
function stripArchiveExtension(name: string): string {
  return name.replace(/\.(skill|zip)$/i, "");
}

/** 把名字清洗成可安全用作文件名的基底；清空后回退为 "skill"。 */
function sanitizeArchiveBasename(name: string): string {
  const cleaned = name.replace(/[/\\?%*:|"<>]/g, "-").trim();
  return cleaned || "skill";
}
