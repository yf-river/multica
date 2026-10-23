// 工作区仪表盘（Usage / Errors）的数据聚合纯函数：把后端按 (日期, 模型) 与
// (Agent, 模型) 返回的原始行，折叠成图表 / KPI / 排行需要的按日或按周聚合，
// 并负责失败分类与「匿名桶」的归并。所有函数无副作用。

import { hasKnownCost, type Priceable } from "../runtimes/utils";
import type {
  DashboardUsageDaily,
  DashboardUsageByAgent,
  DashboardAgentRunTime,
  DashboardRunTimeDaily,
  DashboardFailureDaily,
  DashboardFailureByAgent,
} from "@multica/core/types";
import {
  FAILURE_CLASSES,
  failureClassOf,
  type FailureClass,
} from "@multica/core/dashboard";
import {
  addDaysIso,
  estimateCost,
  estimateCostBreakdown,
  formatShortDate,
  todayIso,
  weekStartIso,
  type DailyTokenData,
} from "../runtimes/utils";
import type {
  DailyTimeData,
  DailyTasksData,
  WeeklyTimeData,
  WeeklyTasksData,
  DailyErrorsData,
  WeeklyErrorsData,
  FailureBucketTotals,
  FailureClassCounts,
} from "../runtimes/components/charts";

// ---------------------------------------------------------------------------
// 仪表盘数据聚合
//
// 工作区仪表盘返回的 (日期, 模型) 与 (Agent, 模型) 形状和运行时页一致，所以
// 成本计算复用 runtimes utils 的 `estimateCost` / `estimateCostBreakdown`。
// 运行时页用 `aggregateByDate`（面向带 `provider` 字段的 RuntimeUsage）做的
// 事，这里用一个更紧的类型重写——可选字段更少，消费端条件逻辑也更少。
// ---------------------------------------------------------------------------

// 与 runtimes utils 的 `DailyCostStackData` 保持一致（包括它的缓存读段）——
// 仪表盘喂的是同一个 DailyCostChart，这里少一类就意味着图表总额里少一类
// (MUL-6334)。
/** 按日聚合后的成本堆叠数据（输入/输出/缓存读写四段）。 */
export interface DailyCostStack {
  date: string;
  label: string;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  total: number;
}

function formatDateLabel(d: string): string {
  // 锚定到本地零点，让格式化出来的标签与后端（已在工作区时区）选取的桶对齐。
  // 直接把裸日期塞给 `new Date()` 会被当成 UTC，再按用户时区偏移。
  const date = new Date(d + "T00:00:00");
  return `${date.getMonth() + 1}/${date.getDate()}`;
}

/** 把 (日期, 模型) 行 → 每天一行，将成本拆成堆叠柱状图消费的四段。
 *  按日期升序稳定排序，x 轴从左到右就是旧到新。 */
export function aggregateDailyCost(usage: DashboardUsageDaily[]): DailyCostStack[] {
  const map = new Map<
    string,
    { input: number; output: number; cacheRead: number; cacheWrite: number }
  >();
  for (const u of usage) {
    const b = estimateCostBreakdown(u);
    const entry = map.get(u.date) ?? {
      input: 0,
      output: 0,
      cacheRead: 0,
      cacheWrite: 0,
    };
    entry.input += b.input;
    entry.output += b.output;
    entry.cacheRead += b.cacheRead;
    entry.cacheWrite += b.cacheWrite;
    map.set(u.date, entry);
  }
  const round = (n: number) => Math.round(n * 100) / 100;
  return Array.from(map.entries())
    .toSorted(([a], [b]) => a.localeCompare(b))
    .map(([date, s]) => {
      const input = round(s.input);
      const output = round(s.output);
      const cacheRead = round(s.cacheRead);
      const cacheWrite = round(s.cacheWrite);
      return {
        date,
        label: formatDateLabel(date),
        input,
        output,
        cacheRead,
        cacheWrite,
        total: round(input + output + cacheRead + cacheWrite),
      };
    });
}

/** 把 (日期, 模型) 行 → 每天一行，将原始 token 数拆进四个图表段。与定价无关——
 *  未映射价格的模型在这里仍会计入，即使它们不计入成本。镜像 runtimes utils 里
 *  `aggregateByDate(...).dailyTokens`，让 Usage 页的 Tokens 图和运行时详情页用同
 *  一形状。 */
export function aggregateDailyTokens(usage: DashboardUsageDaily[]): DailyTokenData[] {
  const map = new Map<
    string,
    { input: number; output: number; cacheRead: number; cacheWrite: number }
  >();
  for (const u of usage) {
    const entry = map.get(u.date) ?? {
      input: 0,
      output: 0,
      cacheRead: 0,
      cacheWrite: 0,
    };
    entry.input += u.input_tokens;
    entry.output += u.output_tokens;
    entry.cacheRead += u.cache_read_tokens;
    entry.cacheWrite += u.cache_write_tokens;
    map.set(u.date, entry);
  }
  return Array.from(map.entries())
    .toSorted(([a], [b]) => a.localeCompare(b))
    .map(([date, t]) => ({
      date,
      label: formatDateLabel(date),
      input: t.input,
      output: t.output,
      cacheRead: t.cacheRead,
      cacheWrite: t.cacheWrite,
    }));
}

/** 整个时间窗口的合计指标（KPI 磁贴用）。 */
export interface DashboardTokenTotals {
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  cost: number;
  taskCount: number;
}

/** 整个时间窗的总计，给 KPI 磁贴。taskCount 是把每行的去重任务数相加——服务端
 *  已经按 (日期, 模型) 折叠过，所以同一任务若跨越两天会重复计；对 KPI（「大概的
 *  量」）可接受，精确值由 per-agent 运行时间卡给出。 */
export function computeDailyTotals(usage: DashboardUsageDaily[]): DashboardTokenTotals {
  return usage.reduce<DashboardTokenTotals>(
    (acc, u) => ({
      input: acc.input + u.input_tokens,
      output: acc.output + u.output_tokens,
      cacheRead: acc.cacheRead + u.cache_read_tokens,
      cacheWrite: acc.cacheWrite + u.cache_write_tokens,
      cost: acc.cost + estimateCost(u),
      taskCount: acc.taskCount + u.task_count,
    }),
    { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, cost: 0, taskCount: 0 },
  );
}

/** 单个 Agent 的聚合成本/token 行（由按 (Agent, 模型) 的原始行折叠而来）。 */
export interface AgentCostRow {
  agentId: string;
  tokens: number;
  cost: number;
  taskCount: number;
}

/** 把按 (Agent, 模型) 的行折叠成每个 Agent 一行：成本是该 Agent 各模型花销之和，
 *  这才是用户真正关心的数字；按成本倒序排，让花钱最多的排在最前。 */
export function aggregateAgentTokens(rows: DashboardUsageByAgent[]): AgentCostRow[] {
  const map = new Map<string, AgentCostRow>();
  for (const r of rows) {
    const entry = map.get(r.agent_id) ?? {
      agentId: r.agent_id,
      tokens: 0,
      cost: 0,
      taskCount: 0,
    };
    entry.tokens +=
      r.input_tokens + r.output_tokens + r.cache_read_tokens + r.cache_write_tokens;
    entry.cost += estimateCost(r);
    entry.taskCount += r.task_count;
    map.set(r.agent_id, entry);
  }
  return Array.from(map.values()).toSorted((a, b) => b.cost - a.cost);
}

/** 合并后的单 Agent 仪表盘行：token/成本 + 运行秒数 + 任务数。 */
export interface AgentDashboardRow {
  agentId: string;
  tokens: number;
  cost: number;
  seconds: number;
  taskCount: number;
}

/**
 * 把「每 Agent 的 token 合计」与「每 Agent 的运行时长合计」合并成每 Agent 一行。
 *
 * taskCount 优先取 runTimeRows 里的值——那份汇总才是 SQL 里按 (agent,
 * terminal-task) 做 `COUNT(*)` 得到的真·去重计数；token 汇总按 (agent, model)
 * 统计，任务跨多个模型时会被重复计数，所以只在「该 Agent 还没有终止运行」时
 * 兜底回退到它的 taskCount（进行中的任务已上报 token 但尚未跑完）。
 * 排序：先按成本倒序，再按运行时长倒序。
 */
export function mergeAgentDashboardRows(
  tokenRows: AgentCostRow[],
  runTimeRows: DashboardAgentRunTime[],
): AgentDashboardRow[] {
  const runTimeByAgent = new Map(
    runTimeRows.map((r) => [r.agent_id, r] as const),
  );
  const merged = new Map<string, AgentDashboardRow>();
  for (const r of tokenRows) {
    const rt = runTimeByAgent.get(r.agentId);
    merged.set(r.agentId, {
      agentId: r.agentId,
      tokens: r.tokens,
      cost: r.cost,
      seconds: rt?.total_seconds ?? 0,
      taskCount: rt ? rt.task_count : r.taskCount,
    });
  }
  // 有运行时长行但没有 token 的 Agent 仍该留在列表里（例如一个在产出用量前就
  // 报错的任务）。它们的 token 列保持 0。
  for (const r of runTimeRows) {
    if (merged.has(r.agent_id)) continue;
    merged.set(r.agent_id, {
      agentId: r.agent_id,
      tokens: 0,
      cost: 0,
      seconds: r.total_seconds,
      taskCount: r.task_count,
    });
  }
  return Array.from(merged.values()).toSorted((a, b) => {
    if (b.cost !== a.cost) return b.cost - a.cost;
    return b.seconds - a.seconds;
  });
}

/** 汇聚「所有被硬删除的 Agent」这一聚合行的合成 agentId。哨兵值（不是真实
 *  UUID），组件可据此识别它并渲染占位名，而不是去 Agent 列表里查这个 id。 */
export const DELETED_AGENTS_ROW_ID = "__deleted_agents__";

/** 服务端对「它拒绝具名的所有 Agent」的聚合桶所用的合成 agentId（MUL-5409）：
 *  包括查看者看不到的 Agent，以及 agent-builder 会话背后那些隐藏的系统载体——
 *  任何客户端都无法给它们解析出名字。与 server/internal/handler/dashboard.go 里
 *  的 `restrictedAgentsRowID` 互为镜像，两个字符串必须保持同步。
 *
 *  刻意区别于 DELETED_AGENTS_ROW_ID：那些 Agent 已消失，这些还活着、仍在跑。
 *  把它们标注成「Deleted agents」对用户是假话，因此该桶以中性的「Other agents」
 *  渲染。 */
export const RESTRICTED_AGENTS_ROW_ID = "__restricted_agents__";

/**
 * 把「工作区里已不存在的 Agent」的用量行收进一个合成的「Deleted agents」聚合
 * 行，而不是丢弃它们。Agent 列表以 `include_archived: true` 拉取，所以归档的
 * Agent 会保留名字、继续以自己的身份留在排行榜上；只有被硬删除的 Agent 才会
 * 掉出 `knownAgentIds`、收进这个桶。
 *
 * MUL-3771（PR #4637）最初是*丢弃*这些行、让它们不再渲染成一串裸 UUID——但
 * 顶行 Cost/Tokens KPI 仍然把它们算进花费（那些汇总聚合 `task_usage_hourly`
 * 时不 join `agent`），于是分 Agent 明细与总计对不上了（MUL-3776, #4640）。
 * 改成聚合而不是丢弃，既保住「sum(可见行) == KPI 总计」，又始终不暴露 UUID。
 * 该桶只携带 tokens + cost；seconds 与 taskCount 保持 0，因为运行时长汇总
 * 内连接 `agent`，已删除的 Agent 本来就不给 Time/Tasks KPI 贡献任何东西——
 * 组件对这一行把这两列渲染成「—」。
 *
 * Agent 列表还在加载时 `knownAgentIds` 为 `null`；调用方此时传 `null`，让行
 * 原样通过，而不是慢拉取下整个排行榜塌缩成一个桶。
 *
 * 服务端的 restricted 桶同样不在 `knownAgentIds` 里（它不是 agent），所以显式
 * 放行、不把它扫进 deleted 桶。它也保留自己的 seconds / taskCount：与硬删除的
 * Agent 不同，它真的运行过，运行时长汇总把那些数字折进了它。
 */
export function bucketUnknownAgentRows(
  rows: AgentDashboardRow[],
  knownAgentIds: ReadonlySet<string> | null,
): AgentDashboardRow[] {
  if (!knownAgentIds) return rows;
  const known: AgentDashboardRow[] = [];
  const bucket: AgentDashboardRow = {
    agentId: DELETED_AGENTS_ROW_ID,
    tokens: 0,
    cost: 0,
    seconds: 0,
    taskCount: 0,
  };
  let hasDeleted = false;
  for (const r of rows) {
    if (knownAgentIds.has(r.agentId) || r.agentId === RESTRICTED_AGENTS_ROW_ID) {
      known.push(r);
      continue;
    }
    hasDeleted = true;
    bucket.tokens += r.tokens;
    bucket.cost += r.cost;
  }
  return hasDeleted ? [...known, bucket] : known;
}

/**
 * 按 agent 分组判断"这批用量有没有价格来源"，键是 agentId；被折进
 * "已删除的智能体" 桶的 agent 合并成一条，用桶自己的用量判断。
 *
 * 为什么不能整窗一个标志：窗口里只要有一个可定价模型，其余完全不可定价的 agent
 * 就会跟着显示 $0.00，等于断言"这个 agent 免费"。桶也一样——它是一组真实用量，
 * 不是"未知"。
 */
export function costKnownByAgent(
  rows: readonly (Priceable & { agent_id: string })[],
  knownAgentIds: ReadonlySet<string> | null,
): Map<string, boolean> {
  const grouped = new Map<string, (Priceable & { agent_id: string })[]>();
  for (const row of rows) {
    const list = grouped.get(row.agent_id);
    if (list) list.push(row);
    else grouped.set(row.agent_id, [row]);
  }
  const deleted: (Priceable & { agent_id: string })[] = [];
  const result = new Map<string, boolean>();
  for (const [agentId, own] of grouped) {
    if (
      knownAgentIds &&
      !knownAgentIds.has(agentId) &&
      agentId !== RESTRICTED_AGENTS_ROW_ID
    ) {
      deleted.push(...own);
      continue;
    }
    result.set(agentId, hasKnownCost(own));
  }
  if (deleted.length > 0) result.set(DELETED_AGENTS_ROW_ID, hasKnownCost(deleted));
  return result;
}

/** 判断某 agentId 是否为一个合成的桶行（渲染成聚合桶而不是单个 Agent）。
 *  `deletedAgentCount` 和 caption 里的 agent 数都必须排除这些，否则卡片宣称的
 *  Agent 数（或删除数）会多于它实际展示的。 */
export function isSyntheticAgentRow(agentId: string): boolean {
  return agentId === DELETED_AGENTS_ROW_ID || agentId === RESTRICTED_AGENTS_ROW_ID;
}

// ---------------------------------------------------------------------------
// 运行时长 + 任务数的「按周折叠」。镜像 `runtimes/utils.ts` 里已覆盖成本 /
// token 的 `aggregateByWeek`：同样的日历周语义（周一到周日、锚定「今天所在
// 时区」）、同样的预置零桶、同样的部分周元信息。行为只在时区来源上不同——
// 工作区仪表盘用这里用户选的时区，运行时页用运行时的 IANA 时区。
// ---------------------------------------------------------------------------

interface WeekShell {
  weekStart: string;
  weekEnd: string;
  label: string;
  rangeLabel: string;
  partial: boolean;
  daysCovered: number;
}

// 以「今天所在时区」为锚点构建 N 个末尾日历周的「周外壳」。每个外壳携带图表
// 组件消费的标签与部分周元信息；下游聚合器把自己的每周值折到匹配的外壳上。
function buildWeekShells(tz: string, weekCount: number): WeekShell[] {
  const count = Math.max(1, Math.floor(weekCount));
  const today = todayIso(tz);
  const currentWeekStart = weekStartIso(today);
  const firstWeekStart = addDaysIso(currentWeekStart, -(count - 1) * 7);
  const shells: WeekShell[] = [];
  for (let i = 0; i < count; i++) {
    const weekStart = addDaysIso(firstWeekStart, i * 7);
    const weekEnd = addDaysIso(weekStart, 6);
    const partial = today < weekEnd;
    // 这一周实际上已经历了多少天的「含端点」计数。已过去的完整周为 7；当前周
    // 报 1..6。
    const clampedToday =
      today < weekStart ? weekStart : today < weekEnd ? today : weekEnd;
    const elapsed = Math.min(7, Math.max(1, diffDaysIso(weekStart, clampedToday) + 1));
    shells.push({
      weekStart,
      weekEnd,
      label: formatShortDate(weekStart),
      rangeLabel: `${formatShortDate(weekStart)} – ${formatShortDate(weekEnd)}`,
      partial,
      daysCovered: partial ? elapsed : 7,
    });
  }
  return shells;
}

function diffDaysIso(from: string, to: string): number {
  const [y1, m1, d1] = from.split("-").map(Number);
  const [y2, m2, d2] = to.split("-").map(Number);
  const a = Date.UTC(y1 ?? 1970, (m1 ?? 1) - 1, d1 ?? 1);
  const b = Date.UTC(y2 ?? 1970, (m2 ?? 1) - 1, d2 ?? 1);
  return Math.round((b - a) / 86_400_000);
}

/** 把 (日期, 运行时长) 行折叠成每周一行，值为该周落在各外壳上的总运行秒数。 */
export function aggregateWeeklyTime(
  rows: DashboardRunTimeDaily[],
  tz: string,
  weekCount: number,
): WeeklyTimeData[] {
  const shells = buildWeekShells(tz, weekCount);
  const totals = new Map<string, number>();
  for (const shell of shells) totals.set(shell.weekStart, 0);
  for (const r of rows) {
    const wkStart = weekStartIso(r.date);
    if (!totals.has(wkStart)) continue;
    totals.set(wkStart, (totals.get(wkStart) ?? 0) + r.total_seconds);
  }
  return shells.map((s) => ({ ...s, totalSeconds: totals.get(s.weekStart) ?? 0 }));
}

/** 把 (日期, 运行时长) 行折叠成每周一行的完成/失败/取消计数。 */
export function aggregateWeeklyTasks(
  rows: DashboardRunTimeDaily[],
  tz: string,
  weekCount: number,
): WeeklyTasksData[] {
  const shells = buildWeekShells(tz, weekCount);
  const buckets = new Map<
    string,
    { completed: number; failed: number; cancelled: number }
  >();
  for (const shell of shells)
    buckets.set(shell.weekStart, { completed: 0, failed: 0, cancelled: 0 });
  for (const r of rows) {
    const wkStart = weekStartIso(r.date);
    const bucket = buckets.get(wkStart);
    if (!bucket) continue;
    const failed = r.failed_count;
    const cancelled = r.cancelled_count;
    const completed = Math.max(0, r.task_count - failed - cancelled);
    bucket.completed += completed;
    bucket.failed += failed;
    bucket.cancelled += cancelled;
  }
  return shells.map((s) => {
    const b =
      buckets.get(s.weekStart) ?? { completed: 0, failed: 0, cancelled: 0 };
    return {
      ...s,
      completed: b.completed,
      failed: b.failed,
      cancelled: b.cancelled,
    };
  });
}

/** 把按日期的运行时长行 → 每日一行、带 `totalSeconds`，供 DailyTimeChart 使用。
 *  升序排序，让 x 轴从左到右就是旧到新，与成本/token 聚合器保持一致。 */
export function aggregateDailyTime(rows: DashboardRunTimeDaily[]): DailyTimeData[] {
  return rows.toSorted((a, b) => a.date.localeCompare(b.date))
    .map((r) => ({
      date: r.date,
      label: formatDateLabel(r.date),
      totalSeconds: r.total_seconds,
    }));
}

/** 把按日期的运行时长行 → 每日一行、带 completed/failed/cancelled 计数，供
 *  DailyTasksChart 的堆叠柱使用。failed_count 与 cancelled_count 是 task_count
 *  不相交的子集，所以成功数就是余数。减掉 cancelled 很关键：不减的话，一条
 *  用户主动停掉的运行会渲染进绿色的「已完成」段。 */
export function aggregateDailyTasks(rows: DashboardRunTimeDaily[]): DailyTasksData[] {
  return rows.toSorted((a, b) => a.date.localeCompare(b.date))
    .map((r) => {
      const failed = r.failed_count;
      const cancelled = r.cancelled_count;
      const completed = Math.max(0, r.task_count - failed - cancelled);
      return {
        date: r.date,
        label: formatDateLabel(r.date),
        completed,
        failed,
        cancelled,
      };
    });
}

/** 紧凑的人类可读时长："1h 23m" / "12m 30s" / "45s" / "<1m"。用于仪表盘运行
 *  时长 KPI 与每 Agent 的运行时长列。最多保留两段——三段只添视觉噪音，仪表盘
 *  并不需要那种精度。 */
export function formatDuration(seconds: number, lessThanMinuteLabel: string): string {
  if (seconds < 0 || !Number.isFinite(seconds)) return lessThanMinuteLabel;
  if (seconds < 60) {
    if (seconds < 1) return lessThanMinuteLabel;
    return `${Math.round(seconds)}s`;
  }
  const totalMinutes = Math.floor(seconds / 60);
  const hours = Math.floor(totalMinutes / 60);
  const mins = totalMinutes % 60;
  if (hours === 0) {
    const secs = Math.floor(seconds) % 60;
    return secs > 0 ? `${mins}m ${secs}s` : `${mins}m`;
  }
  if (hours >= 24) {
    const days = Math.floor(hours / 24);
    const h = hours % 24;
    return h > 0 ? `${days}d ${h}h` : `${days}d`;
  }
  return mins > 0 ? `${hours}h ${mins}m` : `${hours}h`;
}

// ---------------------------------------------------------------------------
// 失败聚合
//
// 两个失败汇总接口会下发每一条终止任务，用 `failure_reason: ""` 标记成功桶。
// 成功项留在同一载荷里，正是这些辅助函数能算「错误率」的原因：分子分母来自
// 相同的过滤条件——运行时汇总不能当分母，因为它们要求 `started_at IS NOT
// NULL`，而一条在队列里就过期、从未启动的任务不算数。
//
// 这里把所有原始 reason 折叠进 `@multica/core/dashboard` 的七个展示类；原始
// reason 只在 `aggregateFailureReasons` 里存活，它负责类别汇总下面的明细行。
// ---------------------------------------------------------------------------

function emptyClassCounts(): FailureClassCounts {
  return Object.fromEntries(
    FAILURE_CLASSES.map((c) => [c, 0]),
  ) as FailureClassCounts;
}

// 把一行汇总折进可变的累加器。`failure_reason: ""` 是成功桶：它只推进 `total`，
// 从不推进 `failed` 或某个类别。
function foldFailureRow(
  acc: FailureClassCounts & FailureBucketTotals,
  reason: string,
  count: number,
): void {
  acc.total += count;
  if (reason === "") return;
  acc.failed += count;
  acc[failureClassOf(reason)] += count;
}

/** 把 (日期, reason) 行 → 每日一行，带各类别失败计数和该日 failed/total 合计。
 *  日期升序排列，与其他每日聚合器保持一致。 */
export function aggregateDailyErrors(
  rows: DashboardFailureDaily[],
): DailyErrorsData[] {
  const map = new Map<string, FailureClassCounts & FailureBucketTotals>();
  for (const r of rows) {
    let entry = map.get(r.date);
    if (!entry) {
      entry = { ...emptyClassCounts(), failed: 0, total: 0 };
      map.set(r.date, entry);
    }
    foldFailureRow(entry, r.failure_reason, r.task_count);
  }
  return Array.from(map.entries())
    .toSorted(([a], [b]) => a.localeCompare(b))
    .map(([date, counts]) => ({
      ...counts,
      date,
      label: formatDateLabel(date),
    }));
}

/** 周版对应物。桶用与 time/tasks 周聚合器相同的周外壳预置零，所以没有终止任务
 *  的一周渲染成空柱而不是让 x 轴塌缩。 */
export function aggregateWeeklyErrors(
  rows: DashboardFailureDaily[],
  tz: string,
  weekCount: number,
): WeeklyErrorsData[] {
  const shells = buildWeekShells(tz, weekCount);
  const buckets = new Map<string, FailureClassCounts & FailureBucketTotals>();
  for (const shell of shells) {
    buckets.set(shell.weekStart, { ...emptyClassCounts(), failed: 0, total: 0 });
  }
  for (const r of rows) {
    const bucket = buckets.get(weekStartIso(r.date));
    if (!bucket) continue;
    foldFailureRow(bucket, r.failure_reason, r.task_count);
  }
  return shells.map((s) => ({
    ...(buckets.get(s.weekStart) ?? { ...emptyClassCounts(), failed: 0, total: 0 }),
    ...s,
  }));
}

/** 全窗口失败合计，给 Errors KPI 提示和 breakdown 头部用。`rate` 是 [0, 1]
 *  的比率；窗口内完全没有终止任务时为 0。 */
export interface FailureTotals {
  failed: number;
  total: number;
  rate: number;
}

/** 计算整个窗口的失败合计：失败数、总数与失败率（总数为 0 时率为 0）。 */
export function computeFailureTotals(
  rows: { failure_reason: string; task_count: number }[],
): FailureTotals {
  let failed = 0;
  let total = 0;
  for (const r of rows) {
    total += r.task_count;
    if (r.failure_reason !== "") failed += r.task_count;
  }
  return { failed, total, rate: total > 0 ? failed / total : 0 };
}

/** 单个失败类别行（类别 + 计数）。 */
export interface FailureClassRow {
  failureClass: FailureClass;
  count: number;
}

/** 全窗口按类别统计，最重的在前，零计数类别丢弃。计数相同时按 FAILURE_CLASSES
 *  的顺序打破平局，这样两个类别同计数时列表不会在渲染之间重排。 */
export function aggregateFailureClasses(
  rows: { failure_reason: string; task_count: number }[],
): FailureClassRow[] {
  const counts = emptyClassCounts();
  for (const r of rows) {
    if (r.failure_reason === "") continue;
    counts[failureClassOf(r.failure_reason)] += r.task_count;
  }
  return FAILURE_CLASSES.map((failureClass) => ({
    failureClass,
    count: counts[failureClass],
  }))
    .filter((r) => r.count > 0)
    .toSorted((a, b) => b.count - a.count);
}

/** 单条原始 reason 行（在更粗的类别汇总下面回答「具体是哪个错误」）。 */
export interface FailureReasonRow {
  reason: string;
  failureClass: FailureClass;
  count: number;
}

/** 全窗口按原始 reason 统计，最重的在前。这是回答「哪个具体错误」的行集，
 *  位于更粗的类别汇总之下。 */
export function aggregateFailureReasons(
  rows: { failure_reason: string; task_count: number }[],
): FailureReasonRow[] {
  const counts = new Map<string, number>();
  for (const r of rows) {
    if (r.failure_reason === "") continue;
    counts.set(r.failure_reason, (counts.get(r.failure_reason) ?? 0) + r.task_count);
  }
  return Array.from(counts.entries())
    .map(([reason, count]) => ({
      reason,
      failureClass: failureClassOf(reason),
      count,
    }))
    .toSorted((a, b) => b.count - a.count || a.reason.localeCompare(b.reason));
}

/** 汇总「查看者无法解析出名字的所有 Agent」这一聚合行的合成 agentId。刻意
 *  区别于 DELETED_AGENTS_ROW_ID，因为这个桶一次覆盖两类人群：硬删除的 Agent，
 *  以及服务端已匿名化的 restricted 桶。按「删除」来命名对第二类是说谎，所以
 *  Errors 卡片中性标注它为「Other agents」，对两类都诚实。 */
export const UNRESOLVED_AGENTS_ROW_ID = "__unresolved_agents__";

/** 单 Agent 的失败合计行。 */
export interface AgentFailureRow {
  agentId: string;
  failed: number;
  total: number;
  rate: number;
  // 该 Agent 失败的完整分类拆分，offender 行按它画堆叠柱。带完整构成而不是只
  // 带最重的一类：「只有一种失败方式」和「有五种失败方式」是不同的问题，用单一
  // 主导类别标签会把它们压成同一行。每个类别都在（未用为 0），这样堆叠柱无需
  // 存在性判断就能搭出来。
  classes: FailureClassCounts;
}

/** 每 Agent 的失败合计，最差的在前。默认顺序是绝对失败数：1/1 失败的 Agent
 *  是 100% 失败率，但很少是操作者该先看的（相比失败 40 次的 Agent）。rate
 *  随行携带，`sortAgentFailures` 可按它重新排名。
 *
 *  零失败的 Agent 会被丢掉——这个列表是排查辅助，不是人口普查；它上方的
 *  leaderboard 已展示每个 Agent。 */
export function aggregateAgentFailures(
  rows: DashboardFailureByAgent[],
): AgentFailureRow[] {
  const map = new Map<
    string,
    { failed: number; total: number; classes: FailureClassCounts }
  >();
  for (const r of rows) {
    let entry = map.get(r.agent_id);
    if (!entry) {
      entry = { failed: 0, total: 0, classes: emptyClassCounts() };
      map.set(r.agent_id, entry);
    }
    entry.total += r.task_count;
    if (r.failure_reason === "") continue;
    entry.failed += r.task_count;
    entry.classes[failureClassOf(r.failure_reason)] += r.task_count;
  }
  return sortAgentFailures(
    Array.from(map.entries())
      .filter(([, v]) => v.failed > 0)
      .map(([agentId, v]) => ({
        agentId,
        failed: v.failed,
        total: v.total,
        rate: v.total > 0 ? v.failed / v.total : 0,
        classes: v.classes,
      })),
    "failed",
  );
}

/** offender 列表按哪个指标排序，因此也决定条形的长度。这两者以前曾不一致：列表
 *  按绝对失败数排名，而行上最显眼的数字却是失败率，于是条形看起来在丈量一个它
 *  无关的百分比。镜像 LeaderboardSort 的约定——排序指标、条形长度与强调列一起
 *  联动。 */
export type OffenderSort = "failed" | "rate";

export const OFFENDER_METRIC: Record<
  OffenderSort,
  (r: AgentFailureRow) => number
> = {
  failed: (r) => r.failed,
  rate: (r) => r.rate,
};

/** 在 Agent 的失败率可以参与 Rate 排名之前，其所需的最小终止运行次数。一次
 *  运行就失败是 100% 失败率，没有下限的话那一行会直接登顶、埋没所有值得看
 *  的 Agent。
 *
 *  小样本行被降级而不是隐藏：这个列表必须继续与上方的工作区失败总数对得上账，
 *  而且一个「仅有的两次运行都失败」的 Agent 仍是操作者可能想看的事实。 */
export const MIN_RATE_SAMPLE = 10;

/** 该 Agent 是否已达到失败率排名所需的最小样本量。 */
export function hasRateSample(row: AgentFailureRow): boolean {
  return row.total >= MIN_RATE_SAMPLE;
}

/** 按所选指标给 offender 行重新排名。同值时用另一指标打破平局，使等值的桶保持
 *  稳定、有意义的顺序，而不是每次渲染都重排。 */
export function sortAgentFailures(
  rows: AgentFailureRow[],
  sortBy: OffenderSort,
): AgentFailureRow[] {
  if (sortBy === "failed") {
    return rows.toSorted((a, b) => b.failed - a.failed || b.rate - a.rate);
  }
  const sample = (r: AgentFailureRow) => (hasRateSample(r) ? 0 : 1);
  return rows.toSorted(
    (a, b) => sample(a) - sample(b) || b.rate - a.rate || b.failed - a.failed,
  );
}

/**
 * 把「查看者无法解析其身份」的 Agent 的行折叠成一个聚合桶，让 Errors 列表永远
 * 不渲染裸的 Agent UUID。
 *
 * 隐私边界本身如今住在服务端：GetDashboardFailuresByAgent 在序列化前把调用者
 * 不可见的 Agent 折到 RESTRICTED_AGENTS_ROW_ID（MUL-5409），因为客户端侧过滤
 * 只是装饰——一条 curl 就能绕过。本函数仍是显示侧的兜底：它继续处理硬删除的
 * Agent（服务端没理由藏它们的 id，但它们解析不出名字），并把服务端的桶重新匿名
 * 到同一个中性行，让卡片只有一种「不是你可以打开的 Agent」情形。
 *
 * Agent 列表还在加载时 `knownAgentIds` 为 null。与 `bucketUnknownAgentRows` 在
 * 那个窗口期让行原样通过不同，这里选择匿名化：一瞬闪现的 UUID 正是本函数要防的
 * 泄露，几百毫秒合并成一行是代价更小的失败。
 *
 * 本函数改写的是 RAW 的按 (agent, reason) 行，而不是去合并已聚合的行——这样等
 * `aggregateAgentFailures` 跑起来时，桶只是又一个 agent_id：它的 totals、rate、
 * 类别拆分和排名全来自与其它行相同的代码路径。合并聚合行意味着要在合并处手写
 * 重推 `total` 与 `rate`——对一份已存在一次的算术做第二个易失真的拷贝。
 */
export function anonymizeUnresolvedAgentRows(
  rows: DashboardFailureByAgent[],
  knownAgentIds: ReadonlySet<string> | null,
): DashboardFailureByAgent[] {
  if (!rows.some((r) => !knownAgentIds?.has(r.agent_id))) return rows;
  return rows.map((r) =>
    knownAgentIds?.has(r.agent_id)
      ? r
      : { ...r, agent_id: UNRESOLVED_AGENTS_ROW_ID },
  );
}
