"use client";

// Agent 活动数据的 hook 与纯函数：把工作区级的近 30 天逐日桶归一化成每个 Agent
// 的固定 30 元素序列（供火花图），并提供"取尾部 N 天做汇总"的入口。
import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import type { Agent, AgentActivityBucket } from "../types";
import { agentListOptions } from "../workspace/queries";
import { agentActivity30dOptions } from "./queries";

const DAYS = 30;
const DAY_MS = 24 * 60 * 60 * 1000;

/** 某一天的完成/失败计数（供火花图）。 */
export interface ActivityBucket {
  total: number;
  failed: number;
}

/** 单个 Agent 的近 30 天活动序列。 */
export interface AgentActivity {
  /**
   * 30 个天桶，旧 → 新。没有活动的天补零。各界面自取尾部多少段渲染：
   * Agents 列表用 7 天，Agent 详情用全部 30 天。读取是调用方的活
   * （标准"尾段切片 + 汇总"见 `summarizeActivityWindow`）。
   */
  buckets: ActivityBucket[];
  /**
   * Agent 已存在的天数，封顶为 30。纯外观用途——供提示文案用（"3 天前创建"）。
   * 年轻 Agent 的火花图形状刻意不变；出生前的天看起来和零活动的天一样。
   */
  daysSinceCreated: number;
}

/**
 * 对 Agent 活动序列做"窗口大小"的汇总。Agents 列表（windowDays=7）和详情
 * 的"近 30 天"面板（windowDays=30）都经由它读取，汇总数字绝不会和它标注的
 * 柱子漂移不一致。
 */
export interface ActivityWindowSummary {
  /** 活动序列的尾部 N 个桶（最新端）。 */
  buckets: ActivityBucket[];
  /** 窗口内 `bucket.total` 之和。 */
  totalRuns: number;
  /** 窗口内 `bucket.failed` 之和。 */
  totalFailed: number;
  /** 输入窗口的回显——渲染器用它拼文案。 */
  windowDays: number;
}

/** 空活动（所有桶补零、按满 30 天处理）。 */
const EMPTY: AgentActivity = {
  buckets: Array.from({ length: DAYS }, () => ({ total: 0, failed: 0 })),
  daysSinceCreated: DAYS,
};

/** 空窗口汇总。 */
const EMPTY_SUMMARY: ActivityWindowSummary = {
  buckets: [],
  totalRuns: 0,
  totalFailed: 0,
  windowDays: 0,
};

/**
 * 以 `agent.id` 为 key 的工作区级活动映射。单趟批量：一次拉取 + 一次推导
 * 就同时支撑列表每一行的火花图和详情面板——加行只花 O(1) HTTP 和 O(N) 计算
 * （而不是 O(N) 次 HTTP）。
 * @param wsId 工作区 id（为空时不查询）。
 * @returns 按 Agent id 索引的活动映射 + 是否仍在加载。
 */
export function useWorkspaceActivityMap(wsId: string | undefined): {
  byAgent: Map<string, AgentActivity>;
  loading: boolean;
} {
  const { data: agents, isPending: agentsPending } = useQuery({
    ...agentListOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const { data: buckets, isPending: bucketsPending } = useQuery({
    ...agentActivity30dOptions(wsId ?? ""),
    enabled: !!wsId,
  });

  const byAgent = useMemo(() => {
    if (!agents || !buckets) return new Map<string, AgentActivity>();
    return buildActivityMap(agents, buckets, Date.now());
  }, [agents, buckets]);

  return { byAgent, loading: agentsPending || bucketsPending };
}

/** 一次遍历工作区 Agent 列表，构建 agentId → 活动序列 的映射。 */
export function buildActivityMap(
  agents: readonly Agent[],
  buckets: readonly AgentActivityBucket[],
  now: number,
): Map<string, AgentActivity> {
  // 桶先按 agent 分好组，让逐 Agent 推导是 O(buckets) 而不是 O(agents × buckets)。
  const bucketsByAgent = new Map<string, AgentActivityBucket[]>();
  for (const b of buckets) {
    const list = bucketsByAgent.get(b.agent_id);
    if (list) list.push(b);
    else bucketsByAgent.set(b.agent_id, [b]);
  }

  const out = new Map<string, AgentActivity>();
  for (const agent of agents) {
    out.set(
      agent.id,
      deriveAgentActivity(
        bucketsByAgent.get(agent.id) ?? [],
        agent.created_at,
        now,
      ),
    );
  }
  return out;
}

/**
 * 纯推导：把工作区级桶过滤成单个 Agent 的，并归一化成以 `now` 结尾的固定
 * 30 元素序列。导出供单元测试，以及已手握工作区级桶的界面直接复用。
 */
export function deriveAgentActivity(
  buckets: readonly AgentActivityBucket[],
  agentCreatedAt: string,
  now: number,
): AgentActivity {
  const series: ActivityBucket[] = Array.from({ length: DAYS }, () => ({
    total: 0,
    failed: 0,
  }));

  // 最新的槽位是本地时间"今天"的开头；往回铺 DAYS 个槽，
  // 于是下标 0 = 最旧、下标 DAYS-1 = 今天。
  const today = startOfDay(now);

  for (const b of buckets) {
    const ts = new Date(b.bucket_at).getTime();
    if (Number.isNaN(ts)) continue;
    const daysAgo = Math.floor((today - startOfDay(ts)) / DAY_MS);
    if (daysAgo < 0 || daysAgo >= DAYS) continue;
    const slot = DAYS - 1 - daysAgo;
    series[slot]!.total += b.task_count;
    series[slot]!.failed += b.failed_count;
  }

  const createdAt = new Date(agentCreatedAt).getTime();
  const ageMs = Number.isFinite(createdAt) ? now - createdAt : Infinity;
  const daysSinceCreated = Math.min(
    DAYS,
    Math.max(0, Math.floor(ageMs / DAY_MS)),
  );

  return {
    buckets: series,
    daysSinceCreated,
  };
}

/**
 * 取活动序列的尾部 N 个桶并对它们汇总。这是两个界面（列表 + 详情）共用的唯一入口，
 * 数字永远不会与它们标注的柱子不一致。
 *
 * `windowDays` 会夹到可用桶数以内，因此传大于 `activity.buckets.length` 的值
 * 返回完整序列而不是越界切片。
 */
export function summarizeActivityWindow(
  activity: AgentActivity | undefined,
  windowDays: number,
): ActivityWindowSummary {
  if (!activity) return { ...EMPTY_SUMMARY, windowDays };
  const safeWindow = Math.min(
    Math.max(0, windowDays),
    activity.buckets.length,
  );
  // `slice(-0)` 会返回整个数组（JS 怪癖：-0 === 0），所以不取窗口时要显式挡住。
  const slice =
    safeWindow === 0 ? [] : activity.buckets.slice(-safeWindow);
  let totalRuns = 0;
  let totalFailed = 0;
  for (const b of slice) {
    totalRuns += b.total;
    totalFailed += b.failed;
  }
  return { buckets: slice, totalRuns, totalFailed, windowDays };
}

function startOfDay(ts: number): number {
  // 本地时间的天界。后端按 UTC 午夜截断，但用户的心智模型是"他正看着的那个时区
  // 里的今天/昨天"；用本地时间与之相符，且即使在桶跨过 UTC 午夜时也让"今天"在
  // 一场工作会话里保持稳定。
  const d = new Date(ts);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}

export const __EMPTY_ACTIVITY = EMPTY;
