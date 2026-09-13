// 运行步骤建模：把扁平事件流整理成"一次运行的步骤"。事件流里一次工具调用及其
// 结果存成两行独立的记录（先 tool_use 后 tool_result），这正是转录看起来有实际
// 工作两倍长的原因：75 次调用的运行渲染成 150 行，而结果行正是 raw base64 和
// 8KB 命令转储的落点。这里的"步骤"把这对记录重新折叠，让一次调用读成一行、
// 它的结果是这一行的详情。
//
// 配对靠位置而非 ID：事件流不携带调用 ID。`agent.Message` 到 daemon 一路都有
// CallID，但 `TaskMessageData` 在进入报表前把它丢了，`task_message` 表也没有这
// 列——在那之前，一个结果只能属于"同工具名最老的一条未闭合调用"。

import type { TimelineItem } from "./build-timeline";

/** 一次工具调用。两侧都可能缺失：还在运行中的调用没有结果；流中途重连可能只
 *  收到一个结果而其调用从未被记录。 */
export interface TraceCallStep {
  kind: "call";
  /** 排序键：调用的 seq，调用缺失时用结果的 seq。 */
  seq: number;
  tool: string;
  /** 调用侧的原始时间线条目。 */
  call?: TimelineItem;
  /** 结果侧的原始时间线条目。 */
  result?: TimelineItem;
  startedAt?: string;
  endedAt?: string;
  /** 调用到结果之间的墙钟毫秒数。任一侧缺时间戳则为 undefined——绝不靠猜。 */
  durationMs?: number;
}

/** 智能体的叙述、模型思考或错误：单条消息，没有可配对的对象。 */
export interface TraceMessageStep {
  kind: "text" | "thinking" | "error";
  seq: number;
  item: TimelineItem;
  startedAt?: string;
}

export type TraceStep = TraceCallStep | TraceMessageStep;

/** 连续的同工具调用折叠成的一组。可再展开回它的成员。 */
export interface TraceGroupRow {
  kind: "group";
  seq: number;
  tool: string;
  steps: TraceCallStep[];
  startedAt?: string;
  endedAt?: string;
  durationMs?: number;
}

export type TraceRow = TraceStep | TraceGroupRow;

/**
 * 连续同工具调用累计到多少次才折成一行。
 *
 * 选 3 而不是 2：两次文件读取仍是值得逐行读的两行；五次才是一次"动作"。
 */
export const MIN_GROUP_SIZE = 3;

/**
 * shell 调用无论连续多少次都绝不折叠。
 *
 * 读一次运行，"pnpm typecheck" 和 "pnpm exec playwright test" 是两件不同的事；
 * "Bash · 5 次调用"恰好藏起了读者真正想看的东西，而且把两次命令之间空档一起算
 * 进一组的时长，会读成一条十分钟的命令。判定键是"入参带 `command` 字符串"而非
 * 工具名白名单，因此对所有后端都成立。
 */
function isShellCall(step: TraceCallStep): boolean {
  return typeof step.call?.input?.command === "string";
}

// 把 ISO 时间串解析成毫秒；空值或非法值返回 undefined（绝不抛异常）。
function timeMs(iso: string | undefined): number | undefined {
  if (!iso) return undefined;
  const ms = new Date(iso).getTime();
  return Number.isFinite(ms) ? ms : undefined;
}

// 计算两个时间点之间的毫秒间隔；任一端无效返回 undefined。负值夹成 0 而不是丢弃：
// daemon 按 500ms 批次刷盘，一次很快的调用其结果可能落在同一 tick 甚至早一 tick。
function durationBetween(start?: string, end?: string): number | undefined {
  const a = timeMs(start);
  const b = timeMs(end);
  if (a === undefined || b === undefined) return undefined;
  return Math.max(0, b - a);
}

/** 把 `tool_use` / `tool_result` 对折叠成单个步骤，保持流顺序。 */
export function buildSteps(items: TimelineItem[]): TraceStep[] {
  const steps: TraceStep[] = [];
  // 每种工具名下"未闭合的调用"队列，旧的在前。用 FIFO 而非"最近邻"：provider
  // 并行跑同一工具的两个调用时，返回顺序更接近调用顺序而非倒序。
  const open = new Map<string, TraceCallStep[]>();

  for (const item of items) {
    if (item.type === "tool_use") {
      const tool = item.tool ?? "";
      const step: TraceCallStep = {
        kind: "call",
        seq: item.seq,
        tool,
        call: item,
        startedAt: item.created_at,
      };
      steps.push(step);
      const queue = open.get(tool);
      if (queue) queue.push(step);
      else open.set(tool, [step]);
      continue;
    }

    if (item.type === "tool_result") {
      const tool = item.tool ?? "";
      const pending = open.get(tool)?.shift();
      if (pending) {
        pending.result = item;
        pending.endedAt = item.created_at;
        pending.durationMs = durationBetween(pending.startedAt, item.created_at);
        continue;
      }
      // 孤儿结果：让它独立成一个步骤，任何输出都不丢弃。
      steps.push({
        kind: "call",
        seq: item.seq,
        tool,
        result: item,
        startedAt: item.created_at,
        endedAt: item.created_at,
      });
      continue;
    }

    steps.push({
      kind: item.type,
      seq: item.seq,
      item,
      startedAt: item.created_at,
    });
  }

  return steps;
}

/** 把 `MIN_GROUP_SIZE` 及以上连续的同工具调用折叠成组行。 */
export function groupSteps(steps: TraceStep[]): TraceRow[] {
  const rows: TraceRow[] = [];
  let run: TraceCallStep[] = [];

  const flush = () => {
    if (run.length === 0) return;
    if (run.length < MIN_GROUP_SIZE) {
      rows.push(...run);
    } else {
      const first = run[0]!;
      const last = run[run.length - 1]!;
      rows.push({
        kind: "group",
        seq: first.seq,
        tool: first.tool,
        steps: run,
        startedAt: first.startedAt,
        endedAt: last.endedAt,
        durationMs: durationBetween(first.startedAt, last.endedAt),
      });
    }
    run = [];
  };

  for (const step of steps) {
    if (
      step.kind === "call" &&
      !isShellCall(step) &&
      (run.length === 0 || run[0]!.tool === step.tool)
    ) {
      run.push(step);
      continue;
    }
    flush();
    if (step.kind === "call") run.push(step);
    else rows.push(step);
  }
  flush();

  return rows;
}

// `TraceMessageStep` 在同一个接口上携带三种 kind，所以光靠 `kind` 检查就能收窄
// 属性而不丢组成。下面几个谓词让调用方能对一行做 switch 并拿到可用的收窄类型。
export function isGroupRow(row: TraceRow): row is TraceGroupRow {
  return row.kind === "group";
}

export function isCallStep(row: TraceRow): row is TraceCallStep {
  return row.kind === "call";
}

export function isMessageStep(row: TraceRow): row is TraceMessageStep {
  return row.kind === "text" || row.kind === "thinking" || row.kind === "error";
}

/** 一行里的全部调用——组和单个调用读起来一样，调用方无需分别处理。 */
export function rowCalls(row: TraceRow): TraceCallStep[] {
  if (row.kind === "group") return row.steps;
  return row.kind === "call" ? [row] : [];
}

// ─── 泳道 ─────────────────────────────────────────────────────────────────────

export type LaneSegmentKind = "tool" | "think" | "report" | "error";

export interface LaneSegment {
  /** 距运行起点的毫秒数。 */
  startMs: number;
  durationMs: number;
  kind: LaneSegmentKind;
}

export interface TraceLanes {
  /** 模型回合：工具泳道的补集。 */
  model: LaneSegment[];
  tool: LaneSegment[];
  modelMs: number;
  toolMs: number;
  totalMs: number;
}

interface Interval {
  start: number;
  end: number;
}

// 区间合并：按起点排序后，把重叠/相邻的区间并成一个。工具调用有重叠时（并行），
// 泳道上画的是它们的外包络。
function mergeIntervals(intervals: Interval[]): Interval[] {
  const sorted = [...intervals].sort((a, b) => a.start - b.start);
  const out: Interval[] = [];
  for (const interval of sorted) {
    const prev = out[out.length - 1];
    if (prev && interval.start <= prev.end) {
      prev.end = Math.max(prev.end, interval.end);
      continue;
    }
    out.push({ ...interval });
  }
  return out;
}

/**
 * 两条泳道：工具在干什么、模型在干什么。
 *
 * 模型泳道是工具泳道的补集，而不是取自模型自己的事件——因为那是我们手里唯一
 * 诚实的数据源：一条 `text`/`thinking` 行只带一个到达时间戳，不是一段区间。
 * 工具调用之间的空档就是模型的时间，"28 分钟去哪了"这类问题正是因此才可答。
 *
 * 事件没带可用时间戳时返回 null；调用方会整条时间线都不画，而不是画一条没有
 * 意义的时间轴。
 * @param steps 运行步骤。
 * @param runStart 运行开始时间（ISO）。
 * @param runEnd 运行结束时间（ISO）。
 * @returns 泳道数据；无法确定时间范围时 null。
 */
export function buildLanes(
  steps: TraceStep[],
  runStart: string | undefined,
  runEnd: string | undefined,
): TraceLanes | null {
  const stamps = steps
    .flatMap((step) => [timeMs(step.startedAt), timeMs(step.kind === "call" ? step.endedAt : undefined)])
    .filter((ms): ms is number => ms !== undefined);
  if (stamps.length === 0) return null;

  // 时间轴范围 = 所有时间戳与显式 run 边界的并集，保证头尾都有内容。
  const startMs = Math.min(timeMs(runStart) ?? Infinity, ...stamps);
  const endMs = Math.max(timeMs(runEnd) ?? -Infinity, ...stamps);
  const totalMs = endMs - startMs;
  if (!Number.isFinite(totalMs) || totalMs <= 0) return null;

  // 收集所有可用的工具调用区间，并裁到时间轴范围内。
  const calls: Interval[] = [];
  for (const step of steps) {
    if (step.kind !== "call") continue;
    const start = timeMs(step.startedAt);
    const end = timeMs(step.endedAt);
    if (start === undefined || end === undefined || end <= start) continue;
    calls.push({
      start: Math.max(startMs, start),
      end: Math.min(endMs, end),
    });
  }

  const merged = mergeIntervals(calls.filter((c) => c.end > c.start));
  const tool: LaneSegment[] = merged.map((c) => ({
    startMs: c.start - startMs,
    durationMs: c.end - c.start,
    kind: "tool",
  }));

  // 模型段填满工具留下的每一处空档，含首尾。
  const model: LaneSegment[] = [];
  let cursor = startMs;
  const pushModel = (from: number, to: number) => {
    if (to <= from) return;
    model.push({ startMs: from - startMs, durationMs: to - from, kind: kindForGap(steps, from, to) });
  };
  for (const interval of merged) {
    pushModel(cursor, interval.start);
    cursor = Math.max(cursor, interval.end);
  }
  pushModel(cursor, endMs);

  return {
    model,
    tool,
    modelMs: model.reduce((sum, s) => sum + s.durationMs, 0),
    toolMs: tool.reduce((sum, s) => sum + s.durationMs, 0),
    totalMs,
  };
}

/**
 * 模型在某个空档里做了什么：由落在这个空档内的消息判断。错误压过汇报，
 * 汇报压过纯思考。颜色因此表状态（正常/已交付/失败），而非分类——token 色板
 * 没有可供五种事件类型消耗的分类渐变。
 */
function kindForGap(steps: TraceStep[], from: number, to: number): LaneSegmentKind {
  let kind: LaneSegmentKind = "think";
  for (const step of steps) {
    if (step.kind === "call") continue;
    const at = timeMs(step.startedAt);
    if (at === undefined || at < from || at > to) continue;
    if (step.kind === "error") return "error";
    if (step.kind === "text") kind = "report";
  }
  return kind;
}

/**
 * 按"整"间隔产出的时间轴刻度。
 *
 * `maxTicks` 随缩放放大：时间线被拉到容器 4 倍宽时有四个屏幕要标，就需要 4 倍
 * 的刻度来维持同样的屏上密度。
 * @param totalMs 时间轴总长（毫秒）。
 * @param maxTicks 允许的最大刻度数（随缩放放大）。
 * @returns 刻度偏移数组（毫秒，0 起点，末尾含 totalMs）。
 */
export function timelineTicks(totalMs: number, maxTicks = 6): number[] {
  if (!Number.isFinite(totalMs) || totalMs <= 0) return [];
  const steps = [
    1_000, 5_000, 15_000, 30_000, 60_000, 2 * 60_000, 5 * 60_000, 10 * 60_000,
    15 * 60_000, 30 * 60_000, 60 * 60_000, 2 * 60 * 60_000, 6 * 60 * 60_000,
  ];
  // 挑最小满足"刻度数 ≤ maxTicks"的间隔（值越大越稀疏，从头试）。
  const interval = steps.find((step) => totalMs / step <= maxTicks) ?? steps[steps.length - 1]!;
  const ticks: number[] = [];
  for (let at = 0; at < totalMs; at += interval) ticks.push(at);
  const lastRoundTick = ticks[ticks.length - 1];
  // 精确的运行终点比相近的整刻度更有用；两者都保留会让 "2m" 和 "2m 2s" 这类
  // 标签挤在同一个右边缘像素上，所以当整刻度距终点不足半格时去掉整刻度。
  if (
    lastRoundTick !== undefined &&
    lastRoundTick > 0 &&
    totalMs - lastRoundTick < interval / 2
  ) {
    ticks.pop();
  }
  ticks.push(totalMs);
  return ticks;
}

/** 比这更细的条会看不见；再快的调用也得保持可点击。 */
const SEGMENT_MIN_PX = 3;

/**
 * 一个段落在泳道上的位置（CSS 百分比）。
 *
 * 宽度下限让这个函数不只是两个百分比：40 分钟运行里的一次 40ms 调用四舍五入后
 * 是零点几个像素，所以每条块都被赋予一个可见的最小宽度，缩放是让宽度回归真实
 * 的手段。这个下限也是 `left` 要夹紧的原因——从 99.99% 开始的细条会把它的 3px
 * 画到时间轴右端之外，而那段可横向滚动的 3px 溢出会让轨道白得一条横向滚动条
 * （#6278）。
 * @param segment 时间线段。
 * @param totalMs 时间轴总长（毫秒）。
 * @returns CSS left/width 百分比字符串。
 */
export function laneSegmentPosition(
  segment: LaneSegment,
  totalMs: number,
): { left: string; width: string } {
  const width = `max(${SEGMENT_MIN_PX}px, ${(segment.durationMs / totalMs) * 100}%)`;
  return { left: `min(${(segment.startMs / totalMs) * 100}%, 100% - ${width})`, width };
}

/**
 * 工具泳道时间按"这些调用在做什么"的细分。
 *
 * 泳道本身保持整体：真实运行里某一种工具占绝对多数（命令跑几分钟、读取和编辑
 * 几毫秒完事），按工具拆泳道会用三条近乎空白的轨道去表达一行文字就能说清的事。
 * 这个类型就是那行文字。
 */
export interface ToolKindTotals {
  /** 命令执行类调用累计毫秒。 */
  command: number;
  /** 写文件/编辑类调用累计毫秒。 */
  write: number;
  /** 读文件/查询类调用累计毫秒。 */
  read: number;
  /** 其余类型累计毫秒。 */
  other: number;
}

/**
 * 汇总工具泳道的时间去向。按入参形状猜行为类别：带 `command` 是命令；
 * 带编辑相关字段（old_string/content/changes）是写入；带文件/查询相关字段
 * （file_path/path/pattern/query）是读取；其余归入其他。
 * @param steps 运行步骤。
 * @returns 各类别累计毫秒。
 */
export function toolKindTotals(steps: TraceStep[]): ToolKindTotals {
  const totals: ToolKindTotals = { command: 0, write: 0, read: 0, other: 0 };
  for (const step of steps) {
    if (step.kind !== "call" || step.durationMs === undefined) continue;
    const input = step.call?.input;
    const kind: keyof ToolKindTotals =
      typeof input?.command === "string"
        ? "command"
        : typeof input?.old_string === "string" ||
            typeof input?.content === "string" ||
            input?.changes !== undefined
          ? "write"
          : typeof input?.file_path === "string" ||
              typeof input?.path === "string" ||
              typeof input?.pattern === "string" ||
              typeof input?.query === "string"
            ? "read"
            : "other";
    totals[kind] += step.durationMs;
  }
  return totals;
}

/** 少于这些门槛，时间线就只是装饰：画出来的几根条，信息量不如每行上已经写着的
 *  时长。 */
export const TIMELINE_MIN_STEPS = 8;
export const TIMELINE_MIN_MS = 60_000;

/**
 * 是否值得显示时间线：事件不带可用时间（无泳道数据）肯定不显示；步骤够多且
 * 总时长够长才值得画。
 */
export function shouldShowTimeline(steps: TraceStep[], lanes: TraceLanes | null): boolean {
  if (!lanes) return false;
  return steps.length >= TIMELINE_MIN_STEPS && lanes.totalMs >= TIMELINE_MIN_MS;
}
