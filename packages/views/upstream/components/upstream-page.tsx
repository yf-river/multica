// 上游同步页（"/upstream"）：顶部状态区 + 两个横向页签。
// 页面只读展示服务端台账；所有 git 操作都在 daemon 侧 Agent 里完成。
"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { GitMerge, Settings } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { upstreamSnapshotOptions } from "@multica/core/upstream/queries";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useT } from "../../i18n";
import { PendingTab } from "./pending-tab";
import { RunsTab } from "./runs-tab";
import { UpstreamSettingsSheet } from "./settings-sheet";
import { Button } from "@multica/ui/components/ui/button";

function shortSha(sha: string): string {
  return sha ? sha.slice(0, 8) : "";
}

function formatTime(value: string | null | undefined): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleString();
}

export function UpstreamPage() {
  const { t } = useT("upstream");
  const wsId = useWorkspaceId();
  const [tab, setTab] = useState("pending");
  const [showSettings, setShowSettings] = useState(false);
  const { data, isLoading, error } = useQuery(upstreamSnapshotOptions(wsId));

  const config = data?.config;
  const head = config?.head_sha ?? "";
  const cursor = config?.cursor_sha ?? "";
  const running = !!data?.active_run_id;
  const lastRun = data?.last_run;
  const paused = config?.sync_state === "paused";
  const unconfigured = !config?.agent_id || data?.executor_state === "none";

  const runStatusLabel = (status: string) => {
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

  const stateLabel = (() => {
    if (unconfigured) return t(($) => $.page.status.state_unconfigured);
    if (paused) return t(($) => $.page.status.state_paused);
    if (running) return t(($) => $.page.status.state_catching_up);
    if (config?.sync_state === "bootstrap" || lastRun?.has_more) return t(($) => $.page.status.state_catching_up);
    return t(($) => $.page.status.state_current);
  })();

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {error ? <p role="alert" className="px-4 py-2 text-body text-danger">{error.message}</p> : null}
      <header className="flex flex-wrap items-start justify-between gap-4 border-b border-surface-border px-4 py-4 sm:px-6">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <GitMerge aria-hidden="true" className="h-4 w-4 text-muted-foreground" />
            <h1 className="text-title font-semibold">{t(($) => $.page.title)}</h1>
          </div>
          <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.page.tagline)}</p>
        </div>
        <div className="flex shrink-0 items-start gap-3">
          <Button size="sm" variant="ghost" aria-expanded={showSettings} onClick={() => setShowSettings(true)}>
            <Settings aria-hidden="true" className="mr-1 h-3.5 w-3.5" />
            {t(($) => $.page.setup.title)}
          </Button>
          {isLoading ? (
            <Skeleton className="h-12 w-56" />
          ) : (
            <div className="max-w-sm space-y-1 text-right text-caption text-muted-foreground">
              <div className="break-all font-mono text-caption">
                {config?.upstream_url ?? ""}
                {config?.upstream_branch ? ` @ ${config.upstream_branch}` : ""}
              </div>
              <div>
                {t(($) => $.page.status.baseline, { ref: config?.local_ref ?? "" })}
                {" · "}
                {cursor ? `${shortSha(cursor)} → ${head ? shortSha(head) : "?"}` : t(($) => $.page.status.never)}
              </div>
              <div className={paused ? "text-warning" : running ? "text-brand" : ""}>{stateLabel}</div>
              {lastRun ? (
                <div>
                  {t(($) => $.page.status.last_run, {
                    time: formatTime(lastRun.completed_at ?? lastRun.started_at ?? lastRun.created_at),
                    status: runStatusLabel(lastRun.status),
                  })}
                  {" · "}
                  {t(($) => $.page.status.last_run_result, { scanned: lastRun.scanned_commits, items: lastRun.item_count })}
                </div>
              ) : null}
              {data?.next_run_at && !paused ? (
                <div>{t(($) => $.page.status.next_run, { time: formatTime(data.next_run_at) })}</div>
              ) : null}
              {config?.error ? (
                <details className="text-left">
                  <summary className="cursor-pointer text-danger">{t(($) => $.page.status.error)}</summary>
                  <pre className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-words rounded bg-muted/40 p-2 text-left font-mono text-caption">
                    {config.error}
                  </pre>
                </details>
              ) : null}
            </div>
          )}
        </div>
      </header>

      <Tabs value={tab} onValueChange={setTab} className="flex min-h-0 flex-1 flex-col">
        <div className="border-b border-surface-border px-4 sm:px-6">
          <TabsList variant="line" className="w-full justify-start">
            <TabsTrigger value="pending">
              {t(($) => $.page.tabs.pending)}
              {data?.pending ? <span className="ml-1.5 text-faint-foreground">{data.pending}</span> : null}
            </TabsTrigger>
            <TabsTrigger value="runs">{t(($) => $.page.tabs.runs)}</TabsTrigger>
          </TabsList>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-4 py-4 sm:px-6">
          <TabsContent value="pending" className="mt-0">
            <PendingTab wsId={wsId} snapshot={data} onOpenSettings={() => setShowSettings(true)} />
          </TabsContent>
          <TabsContent value="runs" className="mt-0">
            <RunsTab wsId={wsId} snapshot={data} onOpenSettings={() => setShowSettings(true)} />
          </TabsContent>
        </div>
      </Tabs>

      <UpstreamSettingsSheet open={showSettings} onClose={() => setShowSettings(false)} snapshot={data} />
    </div>
  );
}
