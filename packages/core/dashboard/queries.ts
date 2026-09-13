// 本文件封装仪表盘各报表的 React Query 查询。服务端每 5 分钟把这些统计表
// 物化一次，前端按同样节奏轮询；所有查询都以「工作区 + 天数 + 项目 + 时区」
// 作为 scope。

import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** 仪表盘各类报表查询的 query key 命名空间。 */
export const dashboardKeys = {
  all: (wsId: string) => ["dashboard", wsId] as const,
  daily: (
    wsId: string,
    days: number,
    projectId: string | null,
    tz: string,
  ) => [...dashboardKeys.all(wsId), "daily", days, projectId, tz] as const,
  byAgent: (
    wsId: string,
    days: number,
    projectId: string | null,
    tz: string,
  ) => [...dashboardKeys.all(wsId), "by-agent", days, projectId, tz] as const,
  agentRuntime: (
    wsId: string,
    days: number,
    projectId: string | null,
    tz: string,
  ) => [...dashboardKeys.all(wsId), "agent-runtime", days, projectId, tz] as const,
  runTimeDaily: (
    wsId: string,
    days: number,
    projectId: string | null,
    tz: string,
  ) => [...dashboardKeys.all(wsId), "runtime-daily", days, projectId, tz] as const,
  failuresDaily: (
    wsId: string,
    days: number,
    projectId: string | null,
    tz: string,
  ) => [...dashboardKeys.all(wsId), "failures-daily", days, projectId, tz] as const,
  failuresByAgent: (
    wsId: string,
    days: number,
    projectId: string | null,
    tz: string,
  ) =>
    [...dashboardKeys.all(wsId), "failures-by-agent", days, projectId, tz] as const,
};

// 服务端按 5 分钟节奏物化这些统计，所以挂载中的仪表盘也按同样节奏轮询——轮询
// 更快只会反复读到没变化的统计。较短的 staleTime 让「重新进入页面」保持诚实：
// 超过一分钟的数据在挂载时直接重新拉取，而不是干等轮询周期。查询未挂载或页面
// 在后台时都不会触发请求。
const STALE_TIME = 60 * 1000;
const REFETCH_INTERVAL = 5 * 60 * 1000;

// 时间范围（第 3 段）变化时应保留上一份结果继续展示，让 KPI 卡片和图表原地
// 过渡，而不是退回整页骨架屏。scope 变化（工作区/项目/报表类型/时区）则刻意
// 排除：跨这些维度携带旧数据会短暂展示错误数据。
function isSameDashboardScope(
  previousKey: readonly unknown[] | undefined,
  nextKey: readonly unknown[],
): boolean {
  if (!previousKey || previousKey.length !== nextKey.length) return false;
  return previousKey.every(
    (part, index) => index === 3 || Object.is(part, nextKey[index]),
  );
}

// `tz` 参与所有仪表盘 key，所以切换偏好时缓存会跟着换。每一条序列（token 统计、
// 基于 atq.completed_at 的耗时/失败序列）都按查看者的时区切日界，保证仪表盘各
// 标签页口径始终一致。
/** 查询「每日用量」KPI 曲线。 */
export function dashboardUsageDailyOptions(
  wsId: string,
  days: number,
  projectId: string | null,
  tz: string,
) {
  const queryKey = dashboardKeys.daily(wsId, days, projectId, tz);
  return queryOptions({
    queryKey,
    queryFn: () =>
      api.getDashboardUsageDaily({
        days,
        project_id: projectId ?? undefined,
        tz,
      }),
    enabled: !!wsId,
    staleTime: STALE_TIME,
    refetchInterval: REFETCH_INTERVAL,
    placeholderData: (previousData, previousQuery) =>
      isSameDashboardScope(previousQuery?.queryKey, queryKey)
        ? keepPreviousData(previousData)
        : undefined,
  });
}

/** 查询「按 agent 拆分的用量」。 */
export function dashboardUsageByAgentOptions(
  wsId: string,
  days: number,
  projectId: string | null,
  tz: string,
) {
  const queryKey = dashboardKeys.byAgent(wsId, days, projectId, tz);
  return queryOptions({
    queryKey,
    queryFn: () =>
      api.getDashboardUsageByAgent({
        days,
        project_id: projectId ?? undefined,
        tz,
      }),
    enabled: !!wsId,
    staleTime: STALE_TIME,
    refetchInterval: REFETCH_INTERVAL,
    placeholderData: (previousData, previousQuery) =>
      isSameDashboardScope(previousQuery?.queryKey, queryKey)
        ? keepPreviousData(previousData)
        : undefined,
  });
}

/** 查询「agent 运行总耗时」。 */
export function dashboardAgentRunTimeOptions(
  wsId: string,
  days: number,
  projectId: string | null,
  tz: string,
) {
  const queryKey = dashboardKeys.agentRuntime(wsId, days, projectId, tz);
  return queryOptions({
    queryKey,
    queryFn: () =>
      api.getDashboardAgentRunTime({
        days,
        project_id: projectId ?? undefined,
        tz,
      }),
    enabled: !!wsId,
    staleTime: STALE_TIME,
    refetchInterval: REFETCH_INTERVAL,
    placeholderData: (previousData, previousQuery) =>
      isSameDashboardScope(previousQuery?.queryKey, queryKey)
        ? keepPreviousData(previousData)
        : undefined,
  });
}

/** 查询「每日运行耗时分布」。 */
export function dashboardRunTimeDailyOptions(
  wsId: string,
  days: number,
  projectId: string | null,
  tz: string,
) {
  const queryKey = dashboardKeys.runTimeDaily(wsId, days, projectId, tz);
  return queryOptions({
    queryKey,
    queryFn: () =>
      api.getDashboardRunTimeDaily({
        days,
        project_id: projectId ?? undefined,
        tz,
      }),
    enabled: !!wsId,
    staleTime: STALE_TIME,
    refetchInterval: REFETCH_INTERVAL,
    placeholderData: (previousData, previousQuery) =>
      isSameDashboardScope(previousQuery?.queryKey, queryKey)
        ? keepPreviousData(previousData)
        : undefined,
  });
}

/** 查询「每日失败次数」。 */
export function dashboardFailuresDailyOptions(
  wsId: string,
  days: number,
  projectId: string | null,
  tz: string,
) {
  const queryKey = dashboardKeys.failuresDaily(wsId, days, projectId, tz);
  return queryOptions({
    queryKey,
    queryFn: () =>
      api.getDashboardFailuresDaily({
        days,
        project_id: projectId ?? undefined,
        tz,
      }),
    enabled: !!wsId,
    staleTime: STALE_TIME,
    refetchInterval: REFETCH_INTERVAL,
    placeholderData: (previousData, previousQuery) =>
      isSameDashboardScope(previousQuery?.queryKey, queryKey)
        ? keepPreviousData(previousData)
        : undefined,
  });
}

/** 查询「按 agent 拆分的失败次数」。 */
export function dashboardFailuresByAgentOptions(
  wsId: string,
  days: number,
  projectId: string | null,
  tz: string,
) {
  const queryKey = dashboardKeys.failuresByAgent(wsId, days, projectId, tz);
  return queryOptions({
    queryKey,
    queryFn: () =>
      api.getDashboardFailuresByAgent({
        days,
        project_id: projectId ?? undefined,
        tz,
      }),
    enabled: !!wsId,
    staleTime: STALE_TIME,
    refetchInterval: REFETCH_INTERVAL,
    placeholderData: (previousData, previousQuery) =>
      isSameDashboardScope(previousQuery?.queryKey, queryKey)
        ? keepPreviousData(previousData)
        : undefined,
  });
}
