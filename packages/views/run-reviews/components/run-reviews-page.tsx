"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/paths";
import { cn } from "@multica/ui/lib/utils";
import { PageHeader } from "../../layout/page-header";

type TraceEvent = {
  id: string; task_id: string; event_type: string; event_name: string;
  status: string; provider: string; model: string; failure_reason: string;
  duration_ms?: number; input_tokens: number; output_tokens: number; created_at: string;
};

export function RunReviewsPage() {
  const wsId = useWorkspaceId();
  const [selectedIssue, setSelectedIssue] = useState<string | null>(null);
  const issues = useQuery({
    queryKey: ["run-reviews", wsId, "issues"],
    queryFn: () => api.listIssues({ limit: 100, offset: 0 }),
    enabled: Boolean(wsId),
  });
  const traces = useQuery({
    queryKey: ["run-reviews", selectedIssue, "trace"],
    queryFn: () => api.listIssueTaskTraceEvents(selectedIssue!),
    enabled: Boolean(selectedIssue),
  });
  const rows = useMemo(() => (traces.data?.events ?? []) as TraceEvent[], [traces.data]);

  return (
    <div className="flex h-full flex-col">
      <PageHeader><h1 className="text-title">运行复盘</h1></PageHeader>
      <div className="grid min-h-0 flex-1 grid-cols-[280px_1fr] divide-x">
        <aside className="overflow-auto p-4">
          <div className="mb-3 text-caption text-muted-foreground">任务运行</div>
          {issues.isLoading && <div className="text-body text-muted-foreground">正在加载…</div>}
          {issues.data?.issues.map((issue) => (
            <button key={issue.id} type="button" onClick={() => setSelectedIssue(issue.id)}
              className={cn("mb-1 w-full rounded-md px-3 py-2 text-left text-body hover:bg-muted", selectedIssue === issue.id && "bg-muted font-medium")}>{issue.identifier} {issue.title}</button>
          ))}
          {!issues.isLoading && !issues.data?.issues.length && <div className="text-body text-muted-foreground">暂无任务运行记录</div>}
        </aside>
        <main className="min-w-0 overflow-auto p-6">
          {!selectedIssue && <div className="rounded-lg border border-dashed p-10 text-center text-body text-muted-foreground">选择一个任务查看运行复盘</div>}
          {selectedIssue && traces.isLoading && <div className="text-body text-muted-foreground">正在加载运行记录…</div>}
          {selectedIssue && traces.isError && <div className="rounded-lg border border-destructive/40 p-4 text-body text-destructive">运行记录加载失败，请稍后重试</div>}
          {selectedIssue && !traces.isLoading && !traces.isError && !rows.length && <div className="rounded-lg border border-dashed p-10 text-center text-body text-muted-foreground">这个任务还没有持久化运行记录</div>}
          {rows.length > 0 && <div className="space-y-3"><h2 className="text-title">运行时间线</h2>{rows.map((event) => <article key={event.id} className="rounded-lg border bg-card p-4"><div className="flex items-center justify-between"><span className="font-medium">{event.event_name || event.event_type}</span><span className="text-caption text-muted-foreground">{new Date(event.created_at).toLocaleString("zh-CN")}</span></div><div className="mt-2 flex flex-wrap gap-4 text-caption text-muted-foreground"><span>状态：{event.status}</span>{event.provider && <span>运行时：{event.provider} / {event.model}</span>}{event.duration_ms != null && <span>耗时：{event.duration_ms} ms</span>}<span>用量：{event.input_tokens + event.output_tokens} tokens</span></div>{event.failure_reason && <p className="mt-2 text-body text-destructive">失败原因：{event.failure_reason}</p>}</article>)}</div>}
        </main>
      </div>
    </div>
  );
}
