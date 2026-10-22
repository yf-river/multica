// 上游同步的查询 key 与 query options。清单按筛选条件缓存，
// 标记操作统一失效整个 upstream 前缀，避免跨页签状态不一致。
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const upstreamKeys = {
  all: (wsId: string) => ["upstream", wsId] as const,
  snapshot: (wsId: string) => [...upstreamKeys.all(wsId), "snapshot"] as const,
  instructions: (wsId: string) => [...upstreamKeys.all(wsId), "instructions"] as const,
  items: (wsId: string, params: string) => [...upstreamKeys.all(wsId), "items", params] as const,
  runs: (wsId: string) => [...upstreamKeys.all(wsId), "runs"] as const,
};

const IDLE_POLL_MS = 30_000;
const ACTIVE_POLL_MS = 5_000;

export function upstreamSnapshotOptions(wsId: string) {
  return queryOptions({
    queryKey: upstreamKeys.snapshot(wsId),
    // 只有批次在跑时才需要贴近实时；空闲时把轮询放慢，别让页面常驻请求。
    refetchInterval: (query) => (query.state.data?.active_run_id ? ACTIVE_POLL_MS : IDLE_POLL_MS),
    queryFn: () => api.getUpstreamSnapshot(),
  });
}

export function upstreamInstructionsOptions(wsId: string) {
  return queryOptions({
    queryKey: upstreamKeys.instructions(wsId),
    queryFn: () => api.getUpstreamInstructions(),
    staleTime: Infinity,
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
    // 服务端返回筛选后的总数，页尾判断不再靠"这页是否满 100 条"。
    getNextPageParam: (lastPage, pages) => {
      const loaded = pages.reduce((sum, page) => sum + page.items.length, 0);
      return loaded < lastPage.total ? loaded : undefined;
    },
  });
}

export function upstreamRunsOptions(wsId: string) {
  return infiniteQueryOptions({
    queryKey: upstreamKeys.runs(wsId),
    initialPageParam: 0,
    queryFn: ({ pageParam }) => api.listUpstreamRuns(30, pageParam),
    getNextPageParam: (lastPage, pages) => lastPage.runs.length === 30 ? pages.length * 30 : undefined,
    refetchInterval: (query) => query.state.data?.pages[0]?.runs.some((run) => run.status === "preparing" || run.status === "running") ? 5000 : false,
  });
}

export type { UpstreamItem, UpstreamRun, UpstreamSnapshot } from "../api/schemas";
