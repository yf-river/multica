"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import type { ObservabilitySummary } from "@multica/core/types";

type ObservabilitySummaryCardProps = {
  title: string;
  scopeLabel: string;
  projectId?: string | null;
  squadId?: string | null;
};

export function ObservabilitySummaryCard({ title, scopeLabel, projectId, squadId }: ObservabilitySummaryCardProps) {
  const workspaceId = useWorkspaceId();
  const { data } = useQuery({
    queryKey: ["observability-summary", workspaceId, projectId ?? "", squadId ?? ""],
    queryFn: () => api.getWorkspaceObservabilitySummary(workspaceId, {
      ...(projectId ? { project_id: projectId } : {}),
      ...(squadId ? { squad_id: squadId } : {}),
    }),
    enabled: !!workspaceId,
    staleTime: 30_000,
  });

  const metrics = data?.指标 ?? {};
  const rows = [
    ["SOP 执行数", metricValue(metrics["SOP 执行数"])],
    ["SOP 事件数", metricValue(metrics["SOP 事件数"])],
    ["队列等待", durationValue(metrics["队列等待"])],
    ["执行耗时", durationValue(metrics["执行耗时"])],
    ["总耗时", durationValue(metrics["总耗时"])],
    ["输入 token", metricValue(metrics["输入 token"])],
    ["输出 token", metricValue(metrics["输出 token"])],
    ["预估成本", moneyValue(metrics["预估成本"])],
    ["重试次数", metricValue(metrics["重试次数"])],
    ["证据数", metricValue(metrics["证据数"])],
  ];

  return (
    <section className="rounded-md border border-border/70 bg-muted/20 px-3 py-2">
      <div className="mb-2 flex items-center justify-between gap-2">
        <div>
          <div className="text-xs font-medium text-foreground">{title}</div>
          <div className="mt-0.5 text-[11px] text-muted-foreground">{scopeLabel}</div>
        </div>
        <div className="text-[11px] text-muted-foreground">
          任务观测 {data?.task_trace_total ?? 0}
        </div>
      </div>
      <div className="grid gap-1.5 sm:grid-cols-3">
        {rows.map(([label, value]) => (
          <div key={label} className="min-w-0">
            <div className="text-[11px] text-muted-foreground">{label}</div>
            <div className="truncate text-xs font-medium text-foreground">{value}</div>
          </div>
        ))}
      </div>
      <FailureReasons summary={data} />
      <UsageBreakdown title="模型明细" rows={data?.model_breakdown ?? []} />
      <UsageBreakdown title="runtime 明细" rows={data?.runtime_breakdown ?? []} />
    </section>
  );
}

function FailureReasons({ summary }: { summary?: ObservabilitySummary }) {
  const reasons = summary?.指标?.["失败原因"];
  if (!Array.isArray(reasons) || reasons.length === 0) return null;
  return (
    <div className="mt-2 border-t border-border/60 pt-2 text-[11px] text-muted-foreground">
      失败原因：{reasons.map((item) => String((item as Record<string, unknown>)["原因"] ?? "")).filter(Boolean).join("、")}
    </div>
  );
}

function UsageBreakdown({
  title,
  rows,
}: {
  title: string;
  rows: ObservabilitySummary["model_breakdown"];
}) {
  const topRows = rows.slice(0, 3);
  if (topRows.length === 0) return null;
  return (
    <div className="mt-2 border-t border-border/60 pt-2">
      <div className="mb-1 text-[11px] font-medium text-muted-foreground">{title}</div>
      <div className="space-y-1">
        {topRows.map((row) => {
          const name = String(row["名称"] || row.model || row.runtime || "未记录");
          const tokenTotal =
            Number(row["输入 token"] ?? 0) +
            Number(row["输出 token"] ?? 0) +
            Number(row["缓存读 token"] ?? 0) +
            Number(row["缓存写 token"] ?? 0);
          return (
            <div key={`${title}-${name}`} className="flex min-w-0 items-center gap-2 text-[11px]">
              <span className="min-w-0 flex-1 truncate text-foreground">{name}</span>
              <span className="shrink-0 text-muted-foreground">
                {tokenTotal.toLocaleString("zh-CN")} token · {moneyValue(row["预估成本"])}
                {row["价格已知"] ? "" : " · 缺少价格"}
              </span>
            </div>
          );
        })}
      </div>
    </div>
  );
}

function metricValue(value: unknown): string {
  if (typeof value === "number") return value.toLocaleString("zh-CN");
  if (value === null || value === undefined || value === "") return "0";
  return String(value);
}

function moneyValue(value: unknown): string {
  const n = typeof value === "number" ? value : Number(value ?? 0);
  if (!Number.isFinite(n) || n <= 0) return "$0.00";
  if (n < 0.01) return `$${n.toFixed(6)}`;
  return `$${n.toFixed(2)}`;
}

function durationValue(value: unknown): string {
  if (typeof value !== "number" || value <= 0) return "0ms";
  if (value < 1000) return `${value}ms`;
  const seconds = Math.round(value / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  return rest === 0 ? `${minutes}m` : `${minutes}m ${rest}s`;
}
