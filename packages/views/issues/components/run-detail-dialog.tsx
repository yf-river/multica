"use client";

// 单次运行的详情：从 issue 执行树里筛出该任务的节点、消息与产物。
// 这是运行复盘页移除任务级列表后的承接入口；上下文指标仍由独立弹窗提供。

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Download } from "lucide-react";
import { issueExecutionTreeOptions } from "@multica/core/issues/queries";
import type { IssueTimelineNode } from "@multica/core/types";
import type { TaskMessagePayload } from "@multica/core/types/events";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useT } from "../../i18n";

interface RunDetailDialogProps {
  issueId: string;
  taskId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

function compact(value: number | undefined): string {
  return new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 1 }).format(value ?? 0);
}

function ms(value: number | undefined): string {
  if (!value || value <= 0) return "0s";
  if (value < 1000) return `${Math.round(value)}ms`;
  const sec = value / 1000;
  if (sec < 60) return `${sec.toFixed(1)}s`;
  return `${Math.floor(sec / 60)}m ${Math.round(sec % 60)}s`;
}

function durationBetween(start?: string | null, end?: string | null): number {
  if (!start || !end) return 0;
  const a = new Date(start).getTime();
  const b = new Date(end).getTime();
  return Number.isFinite(a) && Number.isFinite(b) && b > a ? b - a : 0;
}

export function RunDetailDialog({ issueId, taskId, open, onOpenChange }: RunDetailDialogProps) {
  const { t } = useT("issues");
  const treeQuery = useQuery({ ...issueExecutionTreeOptions(issueId), enabled: open && Boolean(issueId) });
  const tree = treeQuery.data;
  const task = tree?.root.tasks?.find((item) => item.id === taskId);
  const nodes = useMemo<IssueTimelineNode[]>(() => {
    const all = tree?.timeline_nodes ?? [];
    return all.filter(
      (node) => node.node_id === `task:${taskId}` || node.parent_node_id === `task:${taskId}` || node.root_task_id === taskId,
    );
  }, [tree, taskId]);
  const messages = useMemo<TaskMessagePayload[]>(
    () => (tree?.root.task_messages ?? []).filter((message) => message.task_id === taskId),
    [tree, taskId],
  );
  const tokens = useMemo(() => {
    let input = 0;
    let output = 0;
    let cache = 0;
    for (const usage of task?.usage ?? []) {
      input += usage.input_tokens;
      output += usage.output_tokens;
      cache += usage.cache_read_tokens;
    }
    return { input, output, cache };
  }, [task]);

  const exportJson = () => {
    const payload = { task, nodes, messages };
    const blob = new Blob([JSON.stringify(payload, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `run-${taskId}.json`;
    anchor.click();
    URL.revokeObjectURL(url);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88dvh] overflow-y-auto overscroll-contain sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="flex items-center justify-between gap-3">
            <span>{t(($) => $.execution_log.detail_title)}</span>
            <button
              type="button"
              onClick={exportJson}
              className="inline-flex items-center gap-1 rounded border px-2 py-1 text-caption text-muted-foreground hover:text-foreground"
            >
              <Download className="h-3.5 w-3.5" />
              {t(($) => $.execution_log.detail_export)}
            </button>
          </DialogTitle>
        </DialogHeader>
        {treeQuery.isLoading ? (
          <Skeleton className="h-48 w-full" />
        ) : !task ? (
          <p className="py-6 text-body text-muted-foreground">{t(($) => $.execution_log.detail_empty)}</p>
        ) : (
          <div className="space-y-4">
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              <Metric label={t(($) => $.execution_log.detail_duration)} value={ms(durationBetween(task.started_at, task.completed_at))} />
              <Metric label="tokens" value={compact(tokens.input + tokens.output + tokens.cache)} />
              <Metric label={t(($) => $.execution_log.detail_nodes)} value={String(nodes.length)} />
              <Metric label={t(($) => $.execution_log.detail_events)} value={String(messages.length)} />
            </div>
            <div className="rounded-md border p-3 text-caption">
              <span className="text-muted-foreground">{t(($) => $.execution_log.detail_tokens)}: </span>
              <span className="tabular-nums">
                {compact(tokens.input)} / {compact(tokens.output)} / {compact(tokens.cache)}
              </span>
            </div>
            <section>
              <div className="mb-1 text-caption text-muted-foreground">{t(($) => $.execution_log.detail_nodes)}</div>
              {nodes.length === 0 ? (
                <p className="text-caption text-muted-foreground">{t(($) => $.execution_log.detail_empty)}</p>
              ) : (
                <div className="max-h-64 overflow-y-auto rounded-md border">
                  <table className="w-full text-caption tabular-nums">
                    <tbody>
                      {nodes.slice(0, 200).map((node) => (
                        <tr key={node.node_id} className="border-b last:border-0">
                          <td className="px-2 py-1">{node.node_type}</td>
                          <td className="px-2 py-1 text-muted-foreground">{node.status}</td>
                          <td className="px-2 py-1 text-right">{ms(node.duration_ms)}</td>
                          <td className="px-2 py-1 text-right">
                            {compact((node.input_tokens ?? 0) + (node.output_tokens ?? 0) + (node.cache_read_tokens ?? 0))}
                          </td>
                          <td className="max-w-[22rem] truncate px-2 py-1 text-muted-foreground">{node.summary}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </section>
            <section>
              <div className="mb-1 text-caption text-muted-foreground">{t(($) => $.execution_log.detail_events)}</div>
              {messages.length === 0 ? (
                <p className="text-caption text-muted-foreground">{t(($) => $.execution_log.detail_no_events)}</p>
              ) : (
                <div className="max-h-72 space-y-1 overflow-y-auto rounded-md border p-2">
                  {messages.slice(0, 300).map((message) => (
                    <div key={`${message.seq}`} className="rounded border p-2 text-caption">
                      <div className="flex items-center justify-between gap-2 text-muted-foreground">
                        <span>{message.type}{message.tool ? ` · ${message.tool}` : ""}</span>
                        <span>#{message.seq}</span>
                      </div>
                      {(message.content || message.output) && (
                        <pre className="mt-1 max-h-32 overflow-auto whitespace-pre-wrap break-words font-mono text-caption">
                          {(message.content || message.output || "").slice(0, 4000)}
                        </pre>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </section>
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
