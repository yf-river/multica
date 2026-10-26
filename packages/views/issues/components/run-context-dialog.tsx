"use client";

// 单次运行的上下文遥测：DSH 的 context / compaction 帧。
//
// 数据来自 GET /api/issues/:id/tasks/:taskId/context。事件按 kind 区分：
// context 是一次模型请求的占用/构成/耗时，compaction 是压缩前后 token。
// 这里只做只读汇总，不参与运行控制。

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { taskContextOptions } from "@multica/core/issues/queries";
import type { TaskContextEvent } from "@multica/core/types/issue-execution";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useT } from "../../i18n";

interface RunContextDialogProps {
  issueId: string;
  taskId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

function num(value: number | undefined): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function compact(value: number): string {
  return new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

function duration(ms: number): string {
  if (ms <= 0) return "0s";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const sec = ms / 1000;
  if (sec < 60) return `${sec.toFixed(1)}s`;
  return `${Math.floor(sec / 60)}m ${Math.round(sec % 60)}s`;
}

function cacheRate(contexts: TaskContextEvent[]): number | null {
  let read = 0;
  let input = 0;
  for (const event of contexts) {
    read += num(event.cache_read_tokens);
    input += num(event.input_tokens);
  }
  const total = read + input;
  return total > 0 ? read / total : null;
}

export function RunContextDialog({ issueId, taskId, open, onOpenChange }: RunContextDialogProps) {
  const { t } = useT("issues");
  const query = useQuery({ ...taskContextOptions(issueId, taskId), enabled: open && Boolean(issueId && taskId) });
  const events = query.data?.events ?? [];
  const contexts = useMemo(() => events.filter((event) => event.kind === "context"), [events]);
  const compactions = useMemo(() => events.filter((event) => event.kind === "compaction"), [events]);

  const summary = useMemo(() => {
    let peak = 0;
    let window = 0;
    let modelMs = 0;
    let input = 0;
    let output = 0;
    let read = 0;
    let system = 0;
    let tools = 0;
    let messages = 0;
    for (const event of contexts) {
      peak = Math.max(peak, num(event.pressure_tokens));
      window = Math.max(window, num(event.context_window));
      modelMs += num(event.duration_ms);
      input += num(event.input_tokens);
      output += num(event.output_tokens);
      read += num(event.cache_read_tokens);
      system = num(event.breakdown?.system_tokens) || system;
      tools = num(event.breakdown?.tools_tokens) || tools;
      messages = num(event.breakdown?.message_tokens) || messages;
    }
    return { peak, window, modelMs, input, output, read, system, tools, messages, rate: cacheRate(contexts) };
  }, [contexts]);

  const recent = contexts.slice(-8).reverse();

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85dvh] overflow-y-auto overscroll-contain sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t(($) => $.execution_log.context_title)}</DialogTitle>
        </DialogHeader>
        {query.isLoading ? (
          <Skeleton className="h-40 w-full" />
        ) : events.length === 0 ? (
          <p className="py-6 text-body text-muted-foreground">{t(($) => $.execution_log.context_empty)}</p>
        ) : (
          <div className="space-y-4">
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
              <Metric label={t(($) => $.execution_log.context_requests)} value={String(contexts.length)} />
              <Metric label={t(($) => $.execution_log.context_peak)} value={compact(summary.peak)} />
              <Metric label={t(($) => $.execution_log.context_window)} value={compact(summary.window)} />
              <Metric label={t(($) => $.execution_log.context_model_time)} value={duration(summary.modelMs)} />
              <Metric label={t(($) => $.execution_log.context_cache_hit)} value={summary.rate === null ? "—" : `${Math.round(summary.rate * 100)}%`} />
              <Metric label={t(($) => $.execution_log.context_compactions)} value={String(compactions.filter((event) => event.phase === "end").length)} />
            </div>
            <div className="rounded-md border p-3 text-body">
              <div className="text-caption text-muted-foreground">{t(($) => $.execution_log.context_breakdown)}</div>
              <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1 tabular-nums">
                <span>system {compact(summary.system)}</span>
                <span>tools {compact(summary.tools)}</span>
                <span>messages {compact(summary.messages)}</span>
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
                      ? `${compact(num(event.before_tokens))} → ${compact(num(event.after_tokens))} ${event.reason ?? ""}`
                      : `compaction start: ${compact(num(event.before_tokens))}`}
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
                      <th className="py-1 pr-2 font-normal">model</th>
                      <th className="py-1 pr-2 font-normal">{t(($) => $.execution_log.context_duration)}</th>
                      <th className="py-1 pr-2 font-normal">{t(($) => $.execution_log.context_pressure)}</th>
                      <th className="py-1 pr-2 font-normal">{t(($) => $.execution_log.context_tokens)}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {recent.map((event, index) => (
                      <tr key={`${event.seq ?? index}`} className="border-t">
                        <td className="py-1 pr-2">{event.model ?? "—"}</td>
                        <td className="py-1 pr-2">{duration(num(event.duration_ms))}</td>
                        <td className="py-1 pr-2">{compact(num(event.pressure_tokens))}</td>
                        <td className="py-1 pr-2">
                          {compact(num(event.input_tokens))} / {compact(num(event.output_tokens))} / {compact(num(event.cache_read_tokens))}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
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
