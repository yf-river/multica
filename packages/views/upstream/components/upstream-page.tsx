// 上游同步页（"/upstream"）：顶部状态区 + 两个横向页签。
// 页面只读展示服务端台账；所有 git 操作都在 daemon 侧 Agent 里完成。
"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, GitMerge, Settings } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { upstreamSnapshotOptions } from "@multica/core/upstream/queries";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@multica/ui/components/ui/tabs";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import { PendingTab } from "./pending-tab";
import { RunsTab } from "./runs-tab";
import { UpstreamSettingsSheet } from "./settings-sheet";
import { Button } from "@multica/ui/components/ui/button";

// 仓库只展示「域名/路径」，完整 URL 放到 title 里：整串 URL 在窄列里会折成两行，
// 而用户真正要看的是哪个仓库、哪个分支。
function repoLabel(url: string): string {
  return url.replace(/^https?:\/\//, "").replace(/\.git$/, "").replace(/\/$/, "");
}

// 顶部信息条只放得下一个时间，用「10/22 20:24」这种紧凑写法；
// 跨年时才补年份，避免每年都重复印一次。
function formatCompact(value: string | null | undefined): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  const day = `${pad(date.getMonth() + 1)}/${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
  return date.getFullYear() === new Date().getFullYear() ? day : `${date.getFullYear()}/${day}`;
}

export function UpstreamPage() {
  const { t } = useT("upstream");
  const wsId = useWorkspaceId();
  const [tab, setTab] = useState("pending");
  const [showSettings, setShowSettings] = useState(false);
  const { data, isLoading, error } = useQuery(upstreamSnapshotOptions(wsId));

  const config = data?.config;
  const running = !!data?.active_run_id;
  const lastRun = data?.last_run;
  const paused = config?.sync_state === "paused";
  const unconfigured = !config?.agent_id || data?.executor_state === "none";
  const catchingUp = config?.sync_state === "bootstrap" || !!lastRun?.has_more;

  const stateLabel = (() => {
    if (unconfigured) return t(($) => $.page.status.state_unconfigured);
    if (paused) return t(($) => $.page.status.state_paused);
    if (running) return t(($) => $.page.status.state_running);
    if (catchingUp) return t(($) => $.page.status.state_catching_up);
    return t(($) => $.page.status.state_current);
  })();

  // 2. 执行体失去运行时、或绑定的运行时离线，之前只在设置面板里提示；页面自己也要说，
  //    否则列表照常显示"已追平"，用户不知道其实下一批不会被执行。
  //    徽标只放得下几个字，具体后果放进 title，点开就是设置面板里的修复入口。
  const executorWarning = unconfigured
    ? null
    : data?.executor_state === "unbound"
        ? { badge: t(($) => $.page.status.executor_unbound_badge), detail: t(($) => $.page.status.executor_unbound) }
        : data?.runtime_online === false
          ? { badge: t(($) => $.page.status.runtime_offline_badge), detail: t(($) => $.page.status.runtime_offline) }
          : null;

  // 运行中显示品牌色、暂停与未配置用中性色、追平用成功色：状态先说清楚，
  // 异常原因再单独用警示色标出，不和状态抢同一个色位。
  const stateTone = unconfigured || paused
    ? "border-surface-border bg-muted/50 text-muted-foreground"
    : running || catchingUp
      ? "border-brand/30 bg-brand/10 text-brand"
      : "border-success/30 bg-success/10 text-success";
  const dotTone = unconfigured || paused ? "bg-faint-foreground" : running || catchingUp ? "bg-brand" : "bg-success";

  const lastRunAt = lastRun ? formatCompact(lastRun.completed_at ?? lastRun.started_at ?? lastRun.created_at) : "";
  const lastRunFailed = lastRun?.status === "failed" || lastRun?.status === "interrupted";

  // 信息带按「范围 → 上次 → 下次」排，失败的那一段标红，其余保持同样的灰。
  // 基线只是同步范围的技术细节，窄屏先让位给上次/下次这两条节奏信息。
  const meta: { key: string; text: string; tone?: string; className?: string }[] = [];
  if (config?.local_ref) {
    meta.push({
      key: "baseline",
      text: t(($) => $.page.status.baseline, { ref: config.local_ref }),
      className: "hidden sm:inline-flex",
    });
  }
  if (lastRunAt) {
    meta.push({
      key: "last",
      text: lastRunFailed
        ? t(($) => $.page.status.last_run, {
            time: lastRunAt,
            status: lastRun?.status === "failed" ? t(($) => $.page.runs.status_failed) : t(($) => $.page.runs.status_interrupted),
          })
        : t(($) => $.page.status.last_run_short, { time: lastRunAt }),
      tone: lastRunFailed ? "text-destructive" : undefined,
    });
  }
  if (data?.next_run_at && !paused) {
    meta.push({ key: "next", text: t(($) => $.page.status.next_run, { time: formatCompact(data.next_run_at) }) });
  }

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {error ? <p role="alert" className="px-4 py-2 text-body text-destructive">{error.message}</p> : null}
      {/* 修改 1. 原来把仓库、基线、游标、状态、上次/下次同步六行右对齐文字全塞进页头，
          读起来是一堵字墙。现在页头只留标题 + 状态 + 操作，配置与节奏降为下面一条
          左对齐的信息带，异常单独成行，视觉层级从「六件事并列」变成「页面身份 / 状态 / 细节」。 */}
      <header className="shrink-0 border-b border-surface-border">
        <div className="flex h-12 items-center gap-2 px-4 sm:px-6">
          <GitMerge aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
          <h1 className="shrink-0 text-body font-medium">{t(($) => $.page.title)}</h1>
          <p className="ml-1 hidden min-w-0 truncate text-caption text-muted-foreground md:block">
            {t(($) => $.page.tagline)}
          </p>
          <div className="ml-auto flex shrink-0 items-center gap-1.5">
            {isLoading ? (
              <Skeleton className="h-6 w-20 rounded-full" />
            ) : (
              <span
                className={cn("inline-flex h-6 items-center gap-1.5 rounded-full border px-2.5 text-caption font-medium", stateTone)}
              >
                <span aria-hidden="true" className={cn("size-1.5 rounded-full", dotTone)} />
                {stateLabel}
              </span>
            )}
            {executorWarning ? (
              <button
                type="button"
                title={executorWarning.detail}
                aria-label={executorWarning.detail}
                onClick={() => setShowSettings(true)}
                className="inline-flex h-6 items-center gap-1 rounded-full border border-warning/40 bg-warning/10 px-2.5 text-caption font-medium text-warning hover:bg-warning/15"
              >
                <AlertTriangle aria-hidden="true" className="size-3" />
                {executorWarning.badge}
              </button>
            ) : null}
            <Button size="sm" variant="ghost" aria-expanded={showSettings} onClick={() => setShowSettings(true)}>
              <Settings aria-hidden="true" className="mr-1 size-3.5" />
              {t(($) => $.page.setup.title)}
            </Button>
          </div>
        </div>
        {isLoading ? null : (
          <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 px-4 pb-3 text-caption text-muted-foreground sm:px-6">
            <span className="inline-flex min-w-0 max-w-full items-center gap-1">
              <span className="truncate font-mono text-faint-foreground" title={config?.upstream_url}>
                {repoLabel(config?.upstream_url ?? "")}
              </span>
              {config?.upstream_branch ? <span className="shrink-0">@ {config.upstream_branch}</span> : null}
            </span>
            {meta.map((item, index) => (
              <span key={item.key} className={cn("inline-flex items-center whitespace-nowrap", item.tone, item.className)}>
                {index > 0 ? <span aria-hidden="true" className="mr-1.5 hidden text-faint-foreground sm:inline">·</span> : null}
                {item.text}
              </span>
            ))}
          </div>
        )}
        {config?.error ? (
          <details className="px-4 pb-3 text-caption sm:px-6">
            <summary className="cursor-pointer text-destructive">{t(($) => $.page.status.error)}</summary>
            <pre className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted/40 p-2 font-mono text-caption">
              {config.error}
            </pre>
          </details>
        ) : null}
      </header>

      <Tabs value={tab} onValueChange={setTab} className="flex min-h-0 flex-1 flex-col">
        <div className="border-b border-surface-border px-4 sm:px-6">
          {/* 修改 2. 页签原来被撑成两等分、文字居中，和其他页面的左对齐页签不一致。 */}
          <TabsList variant="line" className="justify-start gap-1">
            <TabsTrigger value="pending">
              {t(($) => $.page.tabs.pending)}
              {/* 修改 3. 这个数字只数「待定」条目，切到全部/已采纳/已忽略后会和列表条数不同；
                  写成「N 条待定」，避免被当成列表漏了数据。 */}
              {data?.pending ? <span className="text-faint-foreground">{t(($) => $.page.tabs.pending_count, { count: data.pending })}</span> : null}
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
