// 同步记录页签：每次批次的模式、范围、状态与产出数量。
"use client";

import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { autopilotKeys, autopilotRunsOptions } from "@multica/core/autopilots/queries";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { Button } from "@multica/ui/components/ui/button";
import { upstreamRunsOptions, type UpstreamRun, type UpstreamSnapshot } from "@multica/core/upstream/queries";
import { Badge } from "@multica/ui/components/ui/badge";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useT } from "../../i18n";

function formatTime(value: string | null): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  const day = `${pad(date.getMonth() + 1)}/${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
  return date.getFullYear() === new Date().getFullYear() ? day : `${date.getFullYear()}/${day}`;
}

function duration(startedAt: string | null, completedAt: string | null): string {
  if (!startedAt || !completedAt) return "";
  const start = new Date(startedAt).getTime();
  const end = new Date(completedAt).getTime();
  if (Number.isNaN(start) || Number.isNaN(end) || end <= start) return "";
  const seconds = Math.round((end - start) / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

export function RunsTab({
  wsId,
  projectId = "multica",
  snapshot,
  onOpenSettings,
}: {
  wsId: string;
  projectId?: string;
  snapshot?: UpstreamSnapshot;
  onOpenSettings: () => void;
}) {
  const { t } = useT("upstream");
  const { data, isLoading, error, hasNextPage, fetchNextPage, isFetchingNextPage } = useInfiniteQuery(upstreamRunsOptions(wsId, projectId));
  const runs = data?.pages.flatMap((page) => page.runs) ?? [];
  const boundAgent = !!snapshot?.config.agent_id;
  const canWrite = snapshot?.can_write ?? false;
  const autopilotId = snapshot?.config.autopilot_id ?? "";
  const qc = useQueryClient();
  const runtimes = useQuery(runtimeListOptions(wsId));
  const canExecute = (runtimes.data ?? []).some((runtime) => runtime.id === snapshot?.config.runtime_id);
  const execution = useQuery({ ...autopilotRunsOptions(wsId, autopilotId), enabled: canExecute && !!autopilotId, refetchInterval: 5000 });
  const latestExecution = execution.data?.[0];
  const dispatchRunning = latestExecution?.status === "running";
  const dispatchFailure = latestExecution && (latestExecution.status === "failed" || latestExecution.status === "skipped");
  const trigger = useMutation({
    mutationFn: () => api.triggerAutopilot(autopilotId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["upstream", wsId] });
      qc.invalidateQueries({ queryKey: autopilotKeys.runs(wsId, autopilotId) });
    },
  });

  const modeLabel = (mode: string) =>
    mode === "bootstrap" ? t(($) => $.page.runs.mode_bootstrap) : t(($) => $.page.runs.mode_incremental);

  const statusLabel = (status: string) => {
    switch (status) {
      case "completed":
        return t(($) => $.page.runs.status_completed);
      case "failed":
        return t(($) => $.page.runs.status_failed);
      case "interrupted":
        return t(($) => $.page.runs.status_interrupted);
      default:
        return t(($) => $.page.runs.status_running);
    }
  };

  return (
    <div className="space-y-3">
      {!boundAgent ? (
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-dashed border-surface-border px-4 py-3 text-caption text-muted-foreground">
          <span>{t(($) => $.page.setup.hint)}</span>
          {canWrite ? <Button size="sm" variant="secondary" onClick={onOpenSettings}>{t(($) => $.page.setup.title)}</Button> : null}
        </div>
      ) : canWrite ? (
        <div className="flex items-center gap-2">
          <Button size="sm" variant="secondary" disabled={!canExecute || dispatchRunning || trigger.isPending || !!snapshot?.active_run_id || snapshot?.config.sync_state === "paused"} onClick={() => trigger.mutate()}>
            {t(($) => $.page.runs.run_now)}
          </Button>
          {trigger.isError ? <p role="alert" className="text-caption text-destructive">{trigger.error.message}</p> : null}
          {trigger.isSuccess ? (
            <span className="text-caption text-muted-foreground">{t(($) => $.page.runs.triggered)}</span>
          ) : null}
        </div>
      ) : null}

      {boundAgent && !canExecute && !runtimes.isLoading ? <p className="text-caption text-muted-foreground">{t(($) => $.page.runs.scope_hint)}</p> : null}
      {boundAgent && !snapshot?.runtime_online ? <p className="text-caption text-warning">{t(($) => $.page.runs.runtime_offline)}</p> : null}
      {dispatchRunning && !snapshot?.active_run_id ? <p aria-live="polite" className="text-caption text-muted-foreground">{t(($) => $.page.status.running)}</p> : null}
      {dispatchFailure ? <p role="alert" className="text-caption text-destructive">{t(($) => $.page.runs.dispatch_failed, { reason: latestExecution.failure_reason ?? "" })}</p> : null}
      {execution.error ? <p role="alert" className="text-caption text-destructive">{execution.error.message}</p> : null}
      {error ? <p role="alert" className="text-caption text-destructive">{error.message}</p> : null}
      {isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
        </div>
      ) : runs.length === 0 ? (
        <div className="rounded-lg border border-dashed border-surface-border px-4 py-10 text-center text-caption text-muted-foreground">
          {t(($) => $.page.runs.empty)}
        </div>
      ) : (
        <ul className="divide-y divide-surface-border rounded-lg border border-surface-border">
          {runs.map((run) => (
            <RunRow
              key={run.id}
              run={run}
              modeLabel={modeLabel(run.mode)}
              statusLabel={statusLabel(run.status)}
              scannedLabel={t(($) => $.page.runs.scanned, { count: run.scanned_commits })}
              absorbedLabel={t(($) => $.page.runs.absorbed, { count: run.absorbed_commits })}
              itemsLabel={t(($) => $.page.runs.items, { count: run.item_count })}
              hasMoreLabel={t(($) => $.page.runs.has_more)}
              durationLabel={duration(run.started_at, run.completed_at)}
              errorLabel={t(($) => $.page.runs.error_detail)}
              triggerLabel={
                run.trigger_source === "schedule"
                  ? t(($) => $.page.runs.trigger_schedule)
                  : t(($) => $.page.runs.trigger_manual)
              }
            />
          ))}
        </ul>
      )}
      {hasNextPage ? <Button size="sm" variant="ghost" disabled={isFetchingNextPage} onClick={() => void fetchNextPage()}>{t(($) => $.page.pending.load_more)}</Button> : null}
    </div>
  );
}

function RunRow({
  run,
  modeLabel,
  statusLabel,
  scannedLabel,
  absorbedLabel,
  itemsLabel,
  hasMoreLabel,
  durationLabel,
  errorLabel,
  triggerLabel,
}: {
  run: UpstreamRun;
  modeLabel: string;
  statusLabel: string;
  scannedLabel: string;
  absorbedLabel: string;
  itemsLabel: string;
  hasMoreLabel: string;
  durationLabel: string;
  errorLabel: string;
  triggerLabel: string;
}) {
  const tone =
    run.status === "failed" || run.status === "interrupted"
      ? "text-destructive"
      : run.status === "running" || run.status === "preparing"
        ? "text-brand"
        : "text-muted-foreground";
  // 修改 1. 原来第二行是「扫描 37 已吸收 2 条目 3 2m 0s 定时」——数字和词并排、没有分隔，
  //    读起来是一串散装字符；改成带单位的短语并用 · 分开。
  const facts = [scannedLabel, absorbedLabel, itemsLabel, durationLabel, triggerLabel].filter(Boolean);
  return (
    <li className="flex items-start justify-between gap-3 px-4 py-3">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <Badge variant="outline">{modeLabel}</Badge>
          <span className={`text-caption font-medium ${tone}`}>{statusLabel}</span>
          <span className="font-mono text-caption text-faint-foreground">
            {run.base_sha ? run.base_sha.slice(0, 8) : ""}
            {run.cursor_sha ? ` → ${run.cursor_sha.slice(0, 8)}` : ""}
          </span>
          {/* 1. 失败或中断的批次没有推进游标，"还有剩余"只对跑完的批次成立。 */}
          {run.has_more && run.status === "completed" ? <span className="text-caption text-warning">{hasMoreLabel}</span> : null}
        </div>
        <div className="mt-1 text-caption text-muted-foreground">{facts.join(" · ")}</div>
        {run.error ? (
          <details className="mt-1 text-caption">
            <summary className="cursor-pointer text-destructive">{errorLabel}</summary>
            <pre className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-words rounded bg-muted/40 p-2 font-mono">
              {run.error}
            </pre>
          </details>
        ) : null}
      </div>
      <span className="shrink-0 text-caption tabular-nums text-faint-foreground">
        {formatTime(run.completed_at ?? run.started_at ?? run.created_at)}
      </span>
    </li>
  );
}
