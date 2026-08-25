import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

type DashboardSeries =
  | "daily"
  | "by-agent"
  | "agent-runtime"
  | "runtime-daily";

// 5-min rollup cadence on the server, 60s background refetch on the client.
const STALE_TIME = 60 * 1000;

function dashboardOptions<Result>(
  series: DashboardSeries,
  wsId: string,
  days: number,
  projectId: string | null,
  tz: string,
  queryFn: () => Promise<Result>,
) {
  return queryOptions({
    queryKey: ["dashboard", wsId, series, days, projectId, tz] as const,
    queryFn,
    enabled: !!wsId,
    staleTime: STALE_TIME,
  });
}

// `tz` participates in every dashboard key so a Preferences change
// repoints the cache. All four series — token rollups and the
// atq.completed_at-based run-time series — slice their day boundary in
// the viewer's tz, so the four dashboard tabs always agree.
export function dashboardUsageDailyOptions(
  wsId: string,
  days: number,
  projectId: string | null,
  tz: string,
) {
  return dashboardOptions("daily", wsId, days, projectId, tz, () =>
      api.getDashboardUsageDaily({
        days,
        project_id: projectId ?? undefined,
        tz,
      }),
  );
}

export function dashboardUsageByAgentOptions(
  wsId: string,
  days: number,
  projectId: string | null,
  tz: string,
) {
  return dashboardOptions("by-agent", wsId, days, projectId, tz, () =>
      api.getDashboardUsageByAgent({
        days,
        project_id: projectId ?? undefined,
        tz,
      }),
  );
}

export function dashboardAgentRunTimeOptions(
  wsId: string,
  days: number,
  projectId: string | null,
  tz: string,
) {
  return dashboardOptions("agent-runtime", wsId, days, projectId, tz, () =>
      api.getDashboardAgentRunTime({
        days,
        project_id: projectId ?? undefined,
        tz,
      }),
  );
}

export function dashboardRunTimeDailyOptions(
  wsId: string,
  days: number,
  projectId: string | null,
  tz: string,
) {
  return dashboardOptions("runtime-daily", wsId, days, projectId, tz, () =>
      api.getDashboardRunTimeDaily({
        days,
        project_id: projectId ?? undefined,
        tz,
      }),
  );
}
