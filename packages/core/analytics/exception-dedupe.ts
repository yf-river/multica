// 本文件对 `$exception` 事件做会话级去重/限流，避免同一错误重复上报。

// 在 posthog-js 的 `before_send` 里、`redactExceptionProperties` 之后运行，
// 因此指纹完全由已脱敏字段构建——原始消息、值或 PII 永远不会写进存储
// （只会存一个哈希和一个小计数器）。
//
// 保险丝逻辑：每个 (标签页会话, 指纹) 组合只保留前 EXCEPTION_SAMPLE_LIMIT 条，
// 其余全部丢弃。一个失控的错误——渲染循环、反复抛错的轮询请求——否则会
// 在单个会话里产生 100+ 条完全相同的 `$exception` 事件（MUL-3331 / MUL-3330）。
// 不同的指纹互不影响。
//
// 安全不变量（关键）：`before_send` 绝不能抛异常——一旦抛出会破坏所有事件投递——
// 而且所有存储失败都必须「故障时放行」。拿不准时我们保留事件：多上报一条重复的
// 代价很小，静默丢掉一条真实首次错误则不然。因此 setItem 失败只会导致「少计数」
// （少丢弃），绝不会「多丢弃」。
//
// 作用域是浏览器标签页会话（`sessionStorage`）：关闭标签页即清除，各标签页相互隔离。
// 这里刻意不用 posthog 的 30 分钟会话——见 MUL-3331 的去重讨论。

const STORAGE_KEY = "mc_exc_fp";
// 每个指纹每会话只保留前 N 条，从第 N+1 条开始丢弃。
const EXCEPTION_SAMPLE_LIMIT = 3;
// 限制每个会话追踪的不同指纹数量，避免「抛很多不同错误」的会话让存储无限膨胀。
// 超过上限后，新指纹不再追踪并故障放行（保留事件）。
const MAX_FINGERPRINTS = 50;

type FingerprintCounts = Record<string, number>;

/**
 * 判断这条已脱敏的 `$exception` 事件是否应作为会话级重复被丢弃。
 * 返回 `true` 表示丢弃，`false` 表示保留。
 *
 * 永不抛异常。缺少指纹信号、存储不可用/损坏或任何意外错误都会返回 `false`（保留），
 * 即故障放行方向。
 *
 * @param properties - 已脱敏的 `$exception` 事件属性
 * @returns 是否应丢弃该事件
 */
export function shouldDropException(
  properties: Record<string, unknown> | undefined,
): boolean {
  const fingerprint = buildFingerprint(properties);
  // 没有稳定的去重依据 → 保留。
  if (fingerprint === null) return false;

  const storage = getSessionStorage();
  if (!storage) return false;

  // 整个「读取-判断-写入」序列都有保护：任何一步抛错（解析、getItem、属性访问）都降级为保留。
  try {
    const counts = readCounts(storage);
    const current = typeof counts[fingerprint] === "number" ? counts[fingerprint] : 0;

    // 该指纹已达上限 → 保险丝熔断，丢弃。
    if (current >= EXCEPTION_SAMPLE_LIMIT) return true;

    // 达到指纹上限后出现全新指纹：不追踪它（避免存储膨胀），并保留事件。
    if (current === 0 && Object.keys(counts).length >= MAX_FINGERPRINTS) {
      return false;
    }

    counts[fingerprint] = current + 1;
    try {
      storage.setItem(STORAGE_KEY, JSON.stringify(counts));
    } catch {
      // 持久化递增失败（配额不足 / 存储被禁用）。下面仍会保留这条事件（返回 false）。
      // 未持久化的递增只意味着下一条相同错误也会被保留——往上限方向「少计数」，
      // 也就是更少丢弃、绝不会更多丢弃。这正是要求的故障方向。
    }
    return false;
  } catch {
    return false;
  }
}

/** 读取并校验计数存储。损坏或意外的载荷按空处理（故障放行——保留此事件并重新播种存储）。 */
function readCounts(storage: Storage): FingerprintCounts {
  const raw = storage.getItem(STORAGE_KEY);
  if (!raw) return {};
  try {
    const parsed: unknown = JSON.parse(raw);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      return parsed as FingerprintCounts;
    }
  } catch {
    // JSON 损坏 → 从空开始。
  }
  return {};
}

/**
 * 从脱敏后的异常属性构建稳定指纹。使用异常类型、脱敏后的消息/值和单个确定的
 * 栈帧。当没有任何稳定可用的键时返回 `null`（保留事件）。
 *
 * 每个栈帧字段（`function` / `lineno` / `colno`）都按可选处理、缺失时降级为空字符串——
 * 压缩过或不完整的栈绝不能抛异常，也不能因为 undefined 访问把所有错误坍缩到同一个桶里。
 */
function buildFingerprint(properties: Record<string, unknown> | undefined): string | null {
  if (!properties || typeof properties !== "object") return null;

  const list = properties.$exception_list;
  const entry =
    Array.isArray(list) && list.length > 0 && list[0] && typeof list[0] === "object"
      ? (list[0] as Record<string, unknown>)
      : undefined;

  const type = readString(entry?.type) ?? readString(properties.$exception_type) ?? "";
  const value =
    readString(entry?.value) ?? readString(properties.$exception_message) ?? "";
  const frame = topFrame(entry);

  // 完全没有任何信号 → 不去重。
  if (type === "" && value === "" && !frame) return null;

  const parts = [type, value];
  if (frame) {
    // colno 被保留下来（关键）：压缩后的 bundle 会把很多语句挤到同一行，
    // 只靠行号无法区分不同的错误。
    parts.push(frame.filename, frame.fn, frame.lineno, frame.colno);
  }
  return hash(parts.join(""));
}

interface TopFrame {
  filename: string;
  fn: string;
  lineno: string;
  colno: string;
}

/**
 * 提取单个确定的栈帧用于指纹计算。我们总是取数组的最后一个帧——一个固定端点，
 * 不做任何引擎/顺序探测。同一会话内的同一错误会得到相同的 frames 数组，因此也
 * 会选中相同的帧，这正是指纹所需要的；我们不关心哪一端在语义上「最靠上」。
 * 缺失的字段降级为空字符串。
 */
function topFrame(entry: Record<string, unknown> | undefined): TopFrame | null {
  if (!entry) return null;
  const stacktrace = entry.stacktrace;
  const frames =
    stacktrace && typeof stacktrace === "object"
      ? (stacktrace as Record<string, unknown>).frames
      : undefined;
  if (!Array.isArray(frames) || frames.length === 0) return null;

  const f = frames[frames.length - 1];
  if (!f || typeof f !== "object") return null;
  const frame = f as Record<string, unknown>;

  return {
    filename: readString(frame.filename) ?? "",
    fn: readString(frame.function) ?? "",
    lineno: readNumberAsString(frame.lineno) ?? "",
    colno: readNumberAsString(frame.colno) ?? "",
  };
}

function readString(v: unknown): string | undefined {
  return typeof v === "string" && v.length > 0 ? v : undefined;
}

function readNumberAsString(v: unknown): string | undefined {
  return typeof v === "number" && Number.isFinite(v) ? String(v) : undefined;
}

/** djb2 —— 一个极小且稳定的字符串哈希。只用于限制存储键长度；
 *  在单个标签页会话的异常集合里，碰撞风险可忽略不计。 */
function hash(input: string): string {
  let h = 5381;
  for (let i = 0; i < input.length; i++) {
    h = ((h << 5) + h) ^ input.charCodeAt(i);
  }
  return (h >>> 0).toString(36);
}

/** 解析 `sessionStorage`，若不存在（SSR）或访问时抛错（沙箱 iframe、存储被禁用）则返回 `null`。 */
function getSessionStorage(): Storage | null {
  try {
    if (typeof sessionStorage === "undefined") return null;
    return sessionStorage;
  } catch {
    return null;
  }
}
