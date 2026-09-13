// Trace Event Presenter——执行转录的纯展示层。给一条时间线事件，它决定视觉种类、
// 标签和单行摘要，编码了阅读层级：
//
//   1. 智能体的文本是主层，无需点击即可读。
//   2. 错误要醒目，同样无需点击即可读。
//   3. 工具调用保持紧凑——provider 原生名 + 信息量最大的那个参数。
//   4. 工具结果与思考被弱化；正文在步骤检查器里打开，而不是直接在列表里。
//   5. 未知事件类型保留为通用事件，绝不丢弃。
//
// 本模块不碰 React 也不碰数据拉取，因此可以独立单测，且不依赖任何渲染事件的外壳
// 列表。

import { truncateWithEllipsis } from "@multica/core/utils";

/** 一条时间线事件（与 TimelineItem 同形，供本层读取用）。 */
export interface TraceEvent {
  seq?: number;
  type: string;
  tool?: string;
  content?: string;
  input?: Record<string, unknown>;
  output?: string;
  created_at?: string;
}

/** 驱动颜色/强调的视觉种类。`generic` 覆盖任何未知 `type`。 */
export type TraceEventKind =
  | "agent"
  | "thinking"
  | "tool_use"
  | "tool_result"
  | "error"
  | "generic";

/** 把事件的原始 type 映射成视觉种类。 */
export function traceEventKind(event: TraceEvent): TraceEventKind {
  switch (event.type) {
    case "text":
      return "agent";
    case "thinking":
      return "thinking";
    case "tool_use":
      return "tool_use";
    case "tool_result":
      return "tool_result";
    case "error":
      return "error";
    default:
      return "generic";
  }
}

/**
 * 给人看的标签。工具事件原样显示 provider 原生的工具名（exec_command、
 * patch_apply——绝不改名）；未知类型显示它自己的原始 type 字符串，保证证据
 * 不会被错标。
 * @param event 时间线事件。
 * @returns 标签文本。
 */
export function traceEventLabel(event: TraceEvent): string {
  switch (event.type) {
    case "text":
      return "Agent";
    case "thinking":
      return "Thinking";
    case "tool_use":
      return event.tool && event.tool.length > 0 ? event.tool : "Tool";
    case "tool_result":
      return event.tool && event.tool.length > 0 ? event.tool : "Result";
    case "error":
      return "Error";
    default:
      return event.type && event.type.length > 0 ? event.type : "Event";
  }
}

/** 把长路径缩短成 ".../父目录/文件名"，让工具摘要保持在一行内。 */
export function shortenTracePath(p: string): string {
  const parts = p.split("/");
  if (parts.length <= 3) return p;
  return ".../" + parts.slice(-2).join("/");
}

// Provider 常把真正的命令包在登录 shell 调用里；在单行摘要里这个包装层是纯噪音
// （完整原样保留在展开后的参数里）。匹配 `<shell> -lc '<cmd>'` / `-c "<cmd>"` 形式。
const SHELL_WRAPPER_PATTERN =
  /^(?:\/[\w./-]*\/)?(?:zsh|bash|sh|fish)\s+(?:-[a-z]+\s+)*(['"])([\s\S]+)\1$/;

/** 剥掉 shell 包装，返回被执行的真正命令。 */
export function stripShellWrapper(command: string): string {
  const match = SHELL_WRAPPER_PATTERN.exec(command.trim());
  return match?.[2] ?? command;
}

// 按"内容字符预算"截断，交给共享的助手保证码点安全且用标准单字符省略号。传
// max + 1 是因为助手把省略号计入自己的预算。
function clip(value: string, max: number): string {
  return truncateWithEllipsis(value, max + 1);
}

/**
 * 本模块自己无法产出的、需要本地化的措辞。本模块保持不依赖 React 与 i18n 才能
 * 独立单测，所以由调用方注入措辞。省略时回退到英文——让兜底安全而不是空白。
 */
export interface TraceSummaryLabels {
  /**
   * 多文件补丁的说法，如 `src/a.go +2 more`。
   *
   * `extraCount` 是"命名文件之外的额外文件数"，不是总数。翻译必须说"还有 N 个"，
   * 不能说"共 N 个文件"——两者在英文里几乎同义，到了别的语言就会分叉。
   */
  morePaths?: (path: string, extraCount: number) => string;
}

/**
 * 工具调用里信息量最大的那个参数，压成一行。优先级顺序贴近评审者最先扫的内容，
 * 最后回退到首个短字符串值。
 * @param input 工具调用入参。
 * @param labels 可选的本地化措辞。
 * @returns 单行摘要文本。
 */
export function traceToolArgSummary(
  input: Record<string, unknown> | undefined,
  labels?: TraceSummaryLabels,
): string {
  if (!input) return "";
  const str = (v: unknown): string => (typeof v === "string" ? v : "");
  if (str(input.query)) return str(input.query);
  // 多文件补丁没有单一 path 字段；少了这步，行摘要会落到通用扫描并空手而归。
  const patch = readPatchSummary(input, labels);
  if (patch) return patch;
  if (str(input.file_path)) return shortenTracePath(str(input.file_path));
  if (str(input.path)) return shortenTracePath(str(input.path));
  if (str(input.pattern)) return str(input.pattern);
  if (str(input.description)) return str(input.description);
  if (str(input.command)) return clip(stripShellWrapper(str(input.command)), 120);
  if (str(input.prompt)) return clip(str(input.prompt), 120);
  if (str(input.skill)) return str(input.skill);
  for (const v of Object.values(input)) {
    if (typeof v === "string" && v.length > 0 && v.length < 120) return v;
  }
  return "";
}

// 取第一行非空白文本。
function firstLine(value: string | undefined): string {
  return value?.split("\n").find((l) => l.trim().length > 0) ?? "";
}

/**
 * 把所有连续空白折叠成单个空格。与 firstLine 不同，它保留跨行的内容，所以美化
 * 打印的 JSON 结果预览成 `[ { "id": ... } ]` 而不是孤零零一个左括号。
 */
function collapseWhitespace(value: string | undefined): string {
  return (value ?? "").replace(/\s+/g, " ").trim();
}

/** 折叠行的单行摘要——绝不包含换行。 */
export function traceEventSummary(event: TraceEvent, labels?: TraceSummaryLabels): string {
  switch (traceEventKind(event)) {
    case "thinking":
      return clip(firstLine(event.content), 200);
    case "tool_use":
      return traceToolArgSummary(event.input, labels);
    case "tool_result":
      // 先解包：折叠行是人不点击就直读的那一行，绝不能显示传输层的转义。
      return clip(collapseWhitespace(unwrapToolOutput(event.output ?? "")), 200);
    default:
      return firstLine(event.content ?? event.output);
  }
}

/**
 * "全量复制"用的完整、未截断文本——完整正文，不是单行摘要。工具调用复制完整入参
 * JSON；结果与叙述复制完整内容。事件有合法 `created_at` 时，行首加 RFC 3339
 * 时间戳前缀（#5873）。调用方负责对结果做敏感信息脱敏。
 */
export function traceEventCopyText(event: TraceEvent): string {
  const label = traceEventLabel(event);
  let body: string;
  switch (traceEventKind(event)) {
    case "tool_use":
      body = event.input ? JSON.stringify(event.input, null, 2) : "";
      break;
    case "tool_result":
      // 与行上显示的内容保持一致：复制出来的证据读起来像终端输出，而不是传输编码。
      body = unwrapToolOutput(event.output ?? "");
      break;
    default:
      body = event.content ?? "";
  }
  const date = event.created_at ? new Date(event.created_at) : null;
  const timestamp = date && !Number.isNaN(date.getTime()) ? `[${date.toISOString()}] ` : "";
  return body ? `${timestamp}[${label}] ${body}` : `${timestamp}[${label}]`;
}

/**
 * 工具输出以 JSON 编码持久化，所以结果抵达时是一个带引号、换行被转义的字符串。
 * 恰好解一层，让它读起来就像原本的终端输出。任何不是"包着字符串"的东西——裸的
 * JSON 文档、普通文本、被截断的正文——都原样返回。
 * @param raw 原始输出。
 * @returns 解包后的文本。
 */
export function unwrapToolOutput(raw: string): string {
  const trimmed = raw.trim();
  if (trimmed.length < 2 || !trimmed.startsWith('"') || !trimmed.endsWith('"')) return raw;
  try {
    const decoded: unknown = JSON.parse(trimmed);
    return typeof decoded === "string" ? decoded : raw;
  } catch {
    return raw;
  }
}

export type TraceDiffLineKind = "add" | "remove" | "context" | "gap";

export interface TraceDiffLine {
  kind: TraceDiffLineKind;
  text: string;
  /** `gap` 代表被折叠掉的上下文行数；其他类型上没有此字段。 */
  hidden?: number;
}

/**
 * 展开行的正文。替换类操作读成 diff；整文件写入读成纯内容——因为没跟任何东西
 * 比较过，全标 `+` 只会增加噪音而非信息。一次 patch 按文件携带条目，因为单次
 * Codex `patch_apply` 常常碰多个文件。其余一律当文本。
 */
export type TraceEventDetail =
  | { kind: "diff"; path: string; lines: TraceDiffLine[] }
  | { kind: "file"; path: string; text: string; lineCount: number }
  | { kind: "patch"; files: TracePatchFile[]; truncated: boolean }
  | { kind: "text"; text: string };

/** 多文件补丁中单个文件的正文。 */
export type TracePatchBody =
  | { kind: "diff"; lines: TraceDiffLine[] }
  | { kind: "file"; text: string; lineCount: number }
  /** 路径与类型已知，但正文被大小预算裁掉了。 */
  | { kind: "none" };

export interface TracePatchFile {
  path: string;
  /** `add` | `delete` | `update`，provider 上报时原样保留。 */
  changeKind?: string;
  /** 重命名目标；当这次变更把文件移动了时存在。 */
  movePath?: string;
  /** 为适配载荷预算，该文件的正文被裁剪过则为 true。 */
  truncated?: boolean;
  body: TracePatchBody;
}

/** 空正文是零行，不是一空行——纯删除不会产生任何 `+`。 */
function toLines(value: string): string[] {
  return value.length === 0 ? [] : value.split("\n");
}

// LCS 表格超过这个规模，算账的成本就不值当可读性收益了——变更退化成一块朴素的
// 替换展示，而不是最小 diff。
const MAX_DIFF_CELLS = 250_000;

/** 最小行级 diff。同一变更块内，删除排在新增之前。 */
export function diffTraceLines(before: string[], after: string[]): TraceDiffLine[] {
  const n = before.length;
  const m = after.length;
  const out: TraceDiffLine[] = [];

  if (n * m > MAX_DIFF_CELLS) {
    for (const text of before) out.push({ kind: "remove", text });
    for (const text of after) out.push({ kind: "add", text });
    return out;
  }

  // 平铺的 (n+1) x (m+1) 表：lcs[i][j] 是 before[i:] 与 after[j:] 的最长公共子
  // 序列长度。typed-array 单元格在 noUncheckedIndexedAccess 下保持 number 类型，
  // 且一次分配胜过 n+1 次。
  const width = m + 1;
  const lcs = new Int32Array((n + 1) * width);
  const at = (i: number, j: number): number => lcs[i * width + j] ?? 0;
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i * width + j] =
        before[i] === after[j] ? at(i + 1, j + 1) + 1 : Math.max(at(i + 1, j), at(i, j + 1));
    }
  }

  // 回溯 LCS 表：相同时记 context，否则往 LCS 更长的方向走，走"前"是删除、
  // 走"后"是新增。
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    const beforeLine = before[i] ?? "";
    const afterLine = after[j] ?? "";
    if (beforeLine === afterLine) {
      out.push({ kind: "context", text: beforeLine });
      i++;
      j++;
    } else if (at(i + 1, j) >= at(i, j + 1)) {
      out.push({ kind: "remove", text: beforeLine });
      i++;
    } else {
      out.push({ kind: "add", text: afterLine });
      j++;
    }
  }
  while (i < n) out.push({ kind: "remove", text: before[i++] ?? "" });
  while (j < m) out.push({ kind: "add", text: after[j++] ?? "" });
  return out;
}

/** 变更两侧各保留多少行上下文，超过后折叠。 */
const DIFF_CONTEXT_LINES = 3;

/**
 * 把长段未变化的内容折叠成单个 `gap` 行。一次替换可能给单行变更带上一大段
 * `old_string`；不折叠的话，变更会被从未动过的上下文淹没。折叠省不出一行的短段
 * 保持原样。
 */
export function collapseDiffContext(
  lines: readonly TraceDiffLine[],
  contextLines: number = DIFF_CONTEXT_LINES,
): TraceDiffLine[] {
  const out: TraceDiffLine[] = [];
  let index = 0;
  while (index < lines.length) {
    const line = lines[index];
    if (line === undefined) break;
    if (line.kind !== "context") {
      out.push(line);
      index++;
      continue;
    }

    // 收集一段连续的 context 行。
    let end = index;
    while (end < lines.length && lines[end]?.kind === "context") end++;
    const run = lines.slice(index, end);
    // 顶到最前/最后的一段只在朝向变更的一侧需要保留上下文。
    const head = index === 0 ? 0 : contextLines;
    const tail = end === lines.length ? 0 : contextLines;

    // 短到折叠省不下一行就整段保留，否则留头尾上下文、中间换成 gap。
    if (run.length <= head + tail + 1) {
      out.push(...run);
    } else {
      out.push(...run.slice(0, head));
      out.push({ kind: "gap", text: "", hidden: run.length - head - tail });
      out.push(...run.slice(run.length - tail));
    }
    index = end;
  }
  return out;
}

/**
 * 文件变更靠入参的*形状*识别，绝不看工具名：provider 管它叫 Edit、patch_apply、
 * str_replace、write_file……而本层契约就是原样保留 provider 原生名。
 */
type FileMutation =
  | { mode: "replace"; path: string; before: string[]; after: string[] }
  | { mode: "write"; path: string; content: string };

/**
 * 把现成的 unified diff 解析成 diff 行。
 *
 * Codex 把一个更新过的文件上报成 unified diff 而不是 before/after 对，所以没有
 * 可比对的东西——重新算 diff 就得先从 diff 本身重构出两侧。hunk 头变成 `gap`
 * 行，它代表的正是"被跳过的、未变化的内容"。
 */
export function parseUnifiedDiff(diff: string): TraceDiffLine[] {
  const raw = diff.split("\n");
  // 对结尾换行做 split 会产出一个幻影末元素；真正为空的结尾上下文行应该是 " "，
  // 不可能是 ""。
  if (raw.length > 0 && raw[raw.length - 1] === "") raw.pop();

  const out: TraceDiffLine[] = [];
  // 文件头只可能出现在第一个 hunk 之前。越过那之后每一行都属于文件内容，这时
  // 的 "---"/"+++" 只是恰好以横线或加号开头的普通变更行——Markdown 分隔线、
  // 内嵌补丁、注释横幅都可能长这样。在任意位置把它们当头处理会从 diff 里删掉
  // 真实内容。
  let inHunk = false;
  for (const line of raw) {
    if (line.startsWith("@@")) {
      inHunk = true;
      out.push({ kind: "gap", text: line });
      continue;
    }
    if (!inHunk) {
      if (
        line.startsWith("diff --git") ||
        line.startsWith("index ") ||
        line.startsWith("--- ") ||
        line.startsWith("+++ ") ||
        line === "---" ||
        line === "+++"
      ) {
        continue;
      }
    }
    // "\ No newline at end of file" 是元数据而非文件的一行：真正以反斜杠开头的行
    // 前面会先带 + / - / 空格前缀。
    if (line.startsWith("\\")) continue;
    if (line.startsWith("+")) {
      out.push({ kind: "add", text: line.slice(1) });
      continue;
    }
    if (line.startsWith("-")) {
      out.push({ kind: "remove", text: line.slice(1) });
      continue;
    }
    if (line.startsWith(" ")) {
      out.push({ kind: "context", text: line.slice(1) });
      continue;
    }
    // 容忍丢了前导空格的上下文行——总比把内容扔地上强。
    out.push({ kind: "context", text: line });
  }
  return out;
}

/**
 * 读取 Codex adapter 记录下来的规范化多文件补丁载荷：
 * `{ changes: [{ path, kind, diff?, content?, move_path? }], truncated? }`。
 *
 * 形状不符返回 null，让未识别载荷回退成美化 JSON，而不是渲染成空补丁。
 */
function readPatchChanges(input: Record<string, unknown>): TracePatchFile[] | null {
  if (!Array.isArray(input.changes)) return null;

  const files: TracePatchFile[] = [];
  for (const entry of input.changes) {
    if (typeof entry !== "object" || entry === null) continue;
    const rec = entry as Record<string, unknown>;
    const path = typeof rec.path === "string" ? rec.path : "";
    if (path.length === 0) continue;

    const file: TracePatchFile = { path, body: { kind: "none" } };
    if (typeof rec.kind === "string" && rec.kind.length > 0) file.changeKind = rec.kind;
    if (typeof rec.move_path === "string" && rec.move_path.length > 0) {
      file.movePath = rec.move_path;
    }
    if (rec.truncated === true) file.truncated = true;

    if (typeof rec.diff === "string" && rec.diff.length > 0) {
      file.body = { kind: "diff", lines: parseUnifiedDiff(rec.diff) };
    } else if (typeof rec.content === "string") {
      // 新增文件合法地可以为空，所以这里以"字段存在"为键，而不是"内容非空"。
      if (file.changeKind === "delete") {
        // 旧协议把删除上报成整个"发出去的文件"。全标删除才陈述了事实；一个
        // 绿色的 "+N" 会说反。
        file.body = {
          kind: "diff",
          lines: toLines(rec.content).map((text) => ({ kind: "remove" as const, text })),
        };
      } else {
        file.body = {
          kind: "file",
          text: rec.content,
          lineCount: toLines(rec.content).length,
        };
      }
    }
    files.push(file);
  }

  return files.length > 0 ? files : null;
}

/** 折叠后单行摘要用：第一个路径 + 其余文件数。 */
function readPatchSummary(
  input: Record<string, unknown> | undefined,
  labels?: TraceSummaryLabels,
): string {
  if (!input) return "";
  const files = readPatchChanges(input);
  if (files === null) return "";
  const first = files[0];
  if (first === undefined) return "";
  const head = shortenTracePath(first.path);
  if (files.length === 1) return head;
  const extraCount = files.length - 1;
  return labels?.morePaths?.(head, extraCount) ?? `${head} +${extraCount} more`;
}

function readFileMutation(input: Record<string, unknown>): FileMutation | null {
  const str = (v: unknown): string | null => (typeof v === "string" ? v : null);
  const path = str(input.file_path) ?? str(input.path);
  if (path === null) return null;

  const oldString = str(input.old_string);
  const newString = str(input.new_string);
  if (oldString !== null && newString !== null) {
    return { mode: "replace", path, before: toLines(oldString), after: toLines(newString) };
  }

  // 以 `content` 字段为键，而不是"before 侧为空"：old_string 为空的编辑是对现有
  // 文件的插入，仍然最该读成 diff。
  const content = str(input.content);
  if (content !== null) return { mode: "write", path, content };

  return null;
}

/**
 * 展开行的结构化正文。编辑变成 diff，让评审者看到改了什么而不是两个被转义的字符
 * 串字面量；结果被解包；其余工具调用回退成美化 JSON。
 */
export function traceEventDetail(event: TraceEvent): TraceEventDetail {
  switch (traceEventKind(event)) {
    case "tool_use": {
      if (!event.input) return { kind: "text", text: "" };
      const patch = readPatchChanges(event.input);
      if (patch !== null) {
        return { kind: "patch", files: patch, truncated: event.input.truncated === true };
      }
      const mutation = readFileMutation(event.input);
      if (mutation?.mode === "replace") {
        return {
          kind: "diff",
          path: mutation.path,
          lines: collapseDiffContext(diffTraceLines(mutation.before, mutation.after)),
        };
      }
      if (mutation?.mode === "write") {
        return {
          kind: "file",
          path: mutation.path,
          text: mutation.content,
          lineCount: toLines(mutation.content).length,
        };
      }
      return { kind: "text", text: JSON.stringify(event.input, null, 2) };
    }
    case "tool_result":
      return { kind: "text", text: unwrapToolOutput(event.output ?? "") };
    default:
      return { kind: "text", text: event.content ?? "" };
  }
}

/** 工具返回的图片，可直接渲染。 */
export interface TraceImageResult {
  mediaType: string;
  base64: string;
}

/**
 * 截图工具会以 base64 图片块作答，转录过去把整墙 base64 打印出来——旧界面上最糟
 * 的东西。识别它，让读者看到图片而不是乱码。
 *
 * 只解析已经像是图片块的 JSON：一段 8KB 的命令输出不该为每次渲染付一次
 * `JSON.parse`。
 * @param output 工具输出。
 * @returns 识别出的图片结果，不是图片块时 null。
 */
export function readImageResult(output: string | undefined): TraceImageResult | null {
  if (!output) return null;
  const head = output.slice(0, 200);
  if (!head.includes('"image"') || !head.includes('"base64"')) return null;

  let parsed: unknown;
  try {
    parsed = JSON.parse(output);
  } catch {
    return null;
  }

  // 遍历内容块找 type === "image" 的那个。
  const blocks = Array.isArray(parsed) ? parsed : [parsed];
  for (const block of blocks) {
    if (typeof block !== "object" || block === null) continue;
    const record = block as Record<string, unknown>;
    if (record.type !== "image") continue;
    const source = record.source;
    if (typeof source !== "object" || source === null) continue;
    const sourceRecord = source as Record<string, unknown>;
    const data = sourceRecord.data;
    if (typeof data !== "string" || data.length === 0) continue;
    const mediaType =
      typeof sourceRecord.media_type === "string" ? sourceRecord.media_type : "image/png";
    return { mediaType, base64: data };
  }

  return null;
}

/** base64 载荷的粗略解码后字节数，供人读的标签使用。 */
export function base64ByteLength(base64: string): number {
  const padding = base64.endsWith("==") ? 2 : base64.endsWith("=") ? 1 : 0;
  return Math.max(0, Math.floor((base64.length * 3) / 4) - padding);
}

/** 该事件是否有值得展开的正文。 */
export function traceEventHasDetail(event: TraceEvent): boolean {
  switch (traceEventKind(event)) {
    case "tool_use":
      return !!event.input && Object.keys(event.input).length > 0;
    case "tool_result":
      return !!event.output && event.output.length > 0;
    default:
      return !!event.content && event.content.length > 0;
  }
}

/** 折叠后的单行摘要是否该用等宽字体（命令/输出）。 */
export function traceEventSummaryIsMono(kind: TraceEventKind): boolean {
  return kind === "tool_use" || kind === "tool_result";
}
