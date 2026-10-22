// 上游同步的查询 key 与 query options。清单按筛选条件缓存，
// 标记操作统一失效整个 upstream 前缀，避免跨页签状态不一致。
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const upstreamKeys = {
  all: (wsId: string) => ["upstream", wsId] as const,
  snapshot: (wsId: string) => [...upstreamKeys.all(wsId), "snapshot"] as const,
  items: (wsId: string, params: string) => [...upstreamKeys.all(wsId), "items", params] as const,
  runs: (wsId: string) => [...upstreamKeys.all(wsId), "runs"] as const,
};

export function upstreamSnapshotOptions(wsId: string) {
  return queryOptions({
    queryKey: upstreamKeys.snapshot(wsId),
    queryFn: () => api.getUpstreamSnapshot(),
  });
}

export function upstreamItemsOptions(
  wsId: string,
  params: { status?: string; verdict?: string; presence?: string; q?: string },
) {
  const key = JSON.stringify(params);
  return infiniteQueryOptions({
    queryKey: upstreamKeys.items(wsId, key),
    initialPageParam: 0,
    queryFn: ({ pageParam }) => api.listUpstreamItems({ ...params, limit: 100, offset: pageParam }),
    getNextPageParam: (lastPage, pages) => lastPage.items.length === 100 ? pages.length * 100 : undefined,
  });
}

export function upstreamRunsOptions(wsId: string) {
  return queryOptions({
    queryKey: upstreamKeys.runs(wsId),
    queryFn: () => api.listUpstreamRuns(30),
  });
}

export type { UpstreamItem, UpstreamRun, UpstreamSnapshot } from "../api/schemas";
