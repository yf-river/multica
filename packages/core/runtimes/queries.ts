// Runtime 查询：query key 工厂与各 queryOptions（列表、用量报表等）。
import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** Runtime 全部查询 key 的工厂。 */
export const runtimeKeys = {
  all: (wsId: string) => ["runtimes", wsId] as const,
  list: (wsId: string) => [...runtimeKeys.all(wsId), "list"] as const,
  listMine: (wsId: string) => [...runtimeKeys.all(wsId), "list", "mine"] as const,
  usage: (rid: string, days: number, tz: string) =>
    ["runtimes", "usage", rid, days, tz] as const,
  usageByAgent: (rid: string, days: number, tz: string) =>
    ["runtimes", "usage", "by-agent", rid, days, tz] as const,
  // 按小时报表现在跟随查看者时区，与其它报表一致。
  usageByHour: (rid: string, days: number, tz: string) =>
    ["runtimes", "usage", "by-hour", rid, days, tz] as const,
};

// `tz` 是查看者的 IANA 时区名——所有报表都跟随查看者时区。
/** 单台 runtime 的用量汇总查询配置。 */
export function runtimeUsageOptions(
  runtimeId: string,
  days: number,
  tz: string,
) {
  return queryOptions({
    queryKey: runtimeKeys.usage(runtimeId, days, tz),
    queryFn: () => api.getRuntimeUsage(runtimeId, { days, tz }),
    staleTime: 60 * 1000,
  });
}

/** 按 Agent 拆分的 runtime 用量查询配置。 */
export function runtimeUsageByAgentOptions(
  runtimeId: string,
  days: number,
  tz: string,
) {
  return queryOptions({
    queryKey: runtimeKeys.usageByAgent(runtimeId, days, tz),
    queryFn: () => api.getRuntimeUsageByAgent(runtimeId, { days, tz }),
    staleTime: 60 * 1000,
  });
}

/** 按小时拆分的 runtime 用量查询配置。 */
export function runtimeUsageByHourOptions(runtimeId: string, days: number, tz: string) {
  return queryOptions({
    queryKey: runtimeKeys.usageByHour(runtimeId, days, tz),
    queryFn: () => api.getRuntimeUsageByHour(runtimeId, { days, tz }),
    staleTime: 60 * 1000,
  });
}

/**
 * `wsSlug` 的目标是活跃工作区之外的另一工作区。服务端先按 slug 头解析工作区、
 * 之后才看 `workspace_id` 参数，所以光靠参数够不到"应用尚未导航过去"的工作区——
 * 那正是创建工作区流程的场景。
 * @param wsId 工作区 id。
 * @param owner 传 "me" 只列当前用户自己的 runtime。
 * @param wsSlug 目标工作区 slug（非当前工作区时用）。
 * @returns runtime 列表查询配置。
 */
export function runtimeListOptions(wsId: string, owner?: "me", wsSlug?: string) {
  return queryOptions({
    queryKey: owner === "me" ? runtimeKeys.listMine(wsId) : runtimeKeys.list(wsId),
    queryFn: () => api.listRuntimes({ workspace_id: wsId, owner }, wsSlug),
  });
}
