"use client";

// 单次运行的上下文遥测：DSH 的 context / compaction 帧。
//
// 数据来自 GET /api/issues/:id/tasks/:taskId/context。事件按 kind 区分：
// context 是一次模型请求的占用/构成/耗时，compaction 是压缩前后 token。
// 这里只做只读汇总，不参与运行控制。

import { useMemo } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { taskContextOptions } from "@multica/core/issues/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { summarizeTaskContext } from "@multica/core/issues/runtime-context";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";

interface RunContextDialogProps {
  issueId?: string;
  taskId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

function compact(value: number | undefined): string {
  if (value === undefined || !Number.isFinite(value)) return "—";
  return new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

function duration(ms: number | undefined): string {
  if (ms === undefined || !Number.isFinite(ms)) return "—";
  if (ms <= 0) return "0s";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const sec = ms / 1000;
  if (sec < 60) return `${sec.toFixed(1)}s`;
  return `${Math.floor(sec / 60)}m ${Math.round(sec % 60)}s`;
}

export function RunContextDialog({ issueId = "", taskId, open, onOpenChange }: RunContextDialogProps) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const query = useInfiniteQuery({ ...taskContextOptions(wsId, issueId, taskId), enabled: open && Boolean(wsId && taskId) });
  const events = useMemo(() => query.data?.pages.flatMap((page) => page.events) ?? [], [query.data]);
  const summary = useMemo(() => summarizeTaskContext(events), [events]);
  const { contexts, compactions } = summary;

  const recent = contexts.slice(-8).reverse();

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85dvh] overflow-y-auto overscroll-contain sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t(($) => $.execution_log.context_title)}</DialogTitle>
        </DialogHeader>
        {query.isLoading ? (
          <Skeleton className="h-40 w-full" />
        ) : query.isError && events.length === 0 ? (
          <div role="alert" className="space-y-2 py-4">
            <p className="text-body">{t(($) => $.execution_log.context_load_error)}</p>
            <Button variant="outline" onClick={() => void query.refetch()}>{t(($) => $.execution_log.context_retry)}</Button>
          </div>
        ) : events.length === 0 && !query.hasNextPage ? (
          <p className="py-6 text-body text-muted-foreground">{t(($) => $.execution_log.context_empty)}</p>
        ) : (
          <div className="space-y-4">
            {summary.hasUnobservedUsage && <p role="note" className="text-caption text-muted-foreground">{t(($) => $.execution_log.context_usage_partial)}</p>}
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
              <Metric label={t(($) => $.execution_log.context_requests)} value={String(contexts.length)} />
              <Metric label={t(($) => $.execution_log.context_peak)} value={compact(summary.peak)} />
              <Metric label={t(($) => $.execution_log.context_window)} value={compact(summary.window)} />
              <Metric label={t(($) => $.execution_log.context_model_time)} value={duration(summary.modelMs)} />
              <Metric label={t(($) => $.execution_log.context_cache_hit)} value={summary.rate === undefined ? "—" : `${Math.round(summary.rate * 100)}%`} />
              <Metric label={t(($) => $.execution_log.context_tool_time)} value={duration(summary.toolMs)} />
              <Metric label={t(($) => $.execution_log.context_waiting_time)} value={duration(summary.waitingMs)} />
              <Metric label={t(($) => $.execution_log.context_child_calls)} value={String(summary.childCalls)} />
              <Metric label={t(($) => $.execution_log.context_compactions)} value={String(compactions.filter((event) => event.phase === "end").length)} />
            </div>
            <div className="rounded-md border p-3 text-body">
              <div className="text-caption text-muted-foreground">{t(($) => $.execution_log.context_breakdown)}</div>
              <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1 tabular-nums">
                <span>{t(($) => $.execution_log.context_system)} {compact(summary.system)}</span>
                <span>{t(($) => $.execution_log.context_tools)} {compact(summary.tools)}</span>
                <span>{t(($) => $.execution_log.context_messages)} {compact(summary.messages)}</span>
                {summary.mcp !== undefined && <span>{t(($) => $.execution_log.context_mcp)} {compact(summary.mcp)}</span>}
                {summary.skills !== undefined && <span>{t(($) => $.execution_log.context_skills)} {compact(summary.skills)}</span>}
                <span className="text-muted-foreground">
                  {t(($) => $.execution_log.context_tokens)}: {compact(summary.input)} / {compact(summary.output)} / {compact(summary.read)}
                </span>
              </div>
            </div>
            {compactions.length > 0 && (
              <div className="space-y-1">
                {compactions.slice(-4).map((event, index) => (
                  <div key={`${event.at ?? index}-${event.phase ?? ""}`} className="text-caption text-muted-foreground">
                    {event.phase === "end"
                      ? `${compact(event.before_tokens)} → ${compact(event.after_tokens)} ${event.reason ?? ""}`
                      : `${t(($) => $.execution_log.context_compaction_start)}: ${compact(event.before_tokens)}`}
                  </div>
                ))}
              </div>
            )}
            <div>
              <div className="mb-1 text-caption text-muted-foreground">{t(($) => $.execution_log.context_recent)}</div>
              <div className="overflow-x-auto">
                <table className="w-full text-caption tabular-nums">
                  <thead>
                    <tr className="text-left text-muted-foreground">
                      <th className="py-1 pr-2 font-normal">{t(($) => $.execution_log.context_model)}</th>
                      <th className="py-1 pr-2 font-normal">{t(($) => $.execution_log.context_duration)}</th>
                      <th className="py-1 pr-2 font-normal">{t(($) => $.execution_log.context_pressure)}</th>
                      <th className="py-1 pr-2 font-normal">{t(($) => $.execution_log.context_tokens)}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {recent.map((event, index) => (
                      <tr key={`${event.seq ?? index}`} className="border-t">
                        <td className="py-1 pr-2">{event.model ?? "—"}</td>
                        <td className="py-1 pr-2">{typeof event.duration_ms === "number" ? duration(event.duration_ms) : "—"}</td>
                        <td className="py-1 pr-2">{typeof event.pressure_tokens === "number" ? compact(event.pressure_tokens) : "—"}</td>
                        <td className="py-1 pr-2">
                          {compact(event.input_tokens)} / {compact(event.output_tokens)} / {compact(event.cache_read_tokens)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
            {query.hasNextPage && (
              <div className="space-y-2">
                <p className="text-caption text-muted-foreground">{t(($) => $.execution_log.context_partial)}</p>
                {query.isFetchNextPageError && <p role="alert" className="text-body">{t(($) => $.execution_log.context_load_error)}</p>}
                <Button variant="outline" disabled={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>
                  {query.isFetchingNextPage ? t(($) => $.execution_log.context_loading) : t(($) => $.execution_log.context_load_more)}
                </Button>
              </div>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-md border p-2">
      <div className="text-caption text-muted-foreground">{label}</div>
      <div className="text-body font-medium tabular-nums">{value}</div>
    </div>
  );
}
