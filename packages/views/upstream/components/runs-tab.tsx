// 同步记录页签：每次批次的模式、范围、状态与产出数量。
"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { Button } from "@multica/ui/components/ui/button";
import { upstreamRunsOptions, upstreamSnapshotOptions, type UpstreamRun } from "@multica/core/upstream/queries";
import { Badge } from "@multica/ui/components/ui/badge";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useT } from "../../i18n";

function formatTime(value: string | null): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString();
}

export function RunsTab({ wsId }: { wsId: string }) {
  const { t } = useT("upstream");
  const { data, isLoading, error } = useQuery(upstreamRunsOptions(wsId));
  const snapshot = useQuery(upstreamSnapshotOptions(wsId));
  const runs = data?.runs ?? [];
  const boundAgent = !!snapshot.data?.config.agent_id;
  const autopilotId = snapshot.data?.config.autopilot_id ?? "";
  const qc = useQueryClient();
  const trigger = useMutation({
    mutationFn: () => api.triggerAutopilot(autopilotId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["upstream", wsId] });
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
        <div className="rounded-lg border border-dashed border-surface-border px-4 py-3 text-caption text-muted-foreground">
          {t(($) => $.page.setup.hint)}
        </div>
      ) : (
        <div className="flex items-center gap-2">
          <Button size="sm" variant="secondary" disabled={trigger.isPending || !!snapshot.data?.active_run_id || snapshot.data?.config.sync_state==="paused"} onClick={() => trigger.mutate()}>
            {t(($) => $.page.runs.run_now)}
          </Button>
          {trigger.isError ? <p role="alert" className="text-caption text-danger">{trigger.error.message}</p>:null}
          {trigger.isSuccess ? (
            <span className="text-caption text-muted-foreground">{t(($) => $.page.runs.triggered)}</span>
          ) : null}
        </div>
      )}

      {error ? <p role="alert" className="text-caption text-danger">{error.message}</p> : null}
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
              itemsLabel={t(($) => $.page.runs.items, { count: run.item_count })}
              hasMoreLabel={t(($) => $.page.runs.has_more)}
              triggerLabel={
                run.trigger_source === "schedule"
                  ? t(($) => $.page.runs.trigger_schedule)
                  : t(($) => $.page.runs.trigger_manual)
              }
            />
          ))}
        </ul>
      )}
    </div>
  );
}

function RunRow({
  run,
  modeLabel,
  statusLabel,
  scannedLabel,
  itemsLabel,
  hasMoreLabel,
  triggerLabel,
}: {
  run: UpstreamRun;
  modeLabel: string;
  statusLabel: string;
  scannedLabel: string;
  itemsLabel: string;
  hasMoreLabel: string;
  triggerLabel: string;
}) {
  return (
    <li className="flex flex-wrap items-start justify-between gap-3 px-3 py-2.5">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge variant="outline">{modeLabel}</Badge>
          <span className="text-caption text-muted-foreground">{statusLabel}</span>
          <span className="text-caption text-faint-foreground">
            {run.base_sha ? run.base_sha.slice(0, 8) : ""}
            {run.cursor_sha ? ` → ${run.cursor_sha.slice(0, 8)}` : ""}
          </span>
          {run.has_more ? <span className="text-caption text-warning">{hasMoreLabel}</span> : null}
        </div>
        <div className="mt-1 flex flex-wrap items-center gap-3 text-caption text-muted-foreground">
          <span>{scannedLabel}</span>
          <span>{itemsLabel}</span>
          <span>{triggerLabel}</span>
          {run.error ? <span className="text-danger">{run.error}</span> : null}
        </div>
      </div>
      <span className="shrink-0 text-caption text-faint-foreground">
        {formatTime(run.completed_at ?? run.started_at ?? run.created_at)}
      </span>
    </li>
  );
}
