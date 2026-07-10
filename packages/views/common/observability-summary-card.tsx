"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import type { ObservabilitySummary } from "@multica/core/types";
import { useT } from "../i18n";

type ObservabilitySummaryCardProps = {
  title: string;
  scopeLabel: string;
  projectId?: string | null;
  squadId?: string | null;
  agentId?: string | null;
};

export function ObservabilitySummaryCard({ title, scopeLabel, projectId, squadId, agentId }: ObservabilitySummaryCardProps) {
  const { t } = useT("common");
  const workspaceId = useWorkspaceId();
  const { data } = useQuery({
    queryKey: ["observability-summary", workspaceId, projectId ?? "", squadId ?? "", agentId ?? ""],
    queryFn: () => api.getWorkspaceObservabilitySummary(workspaceId, {
      ...(projectId ? { project_id: projectId } : {}),
      ...(squadId ? { squad_id: squadId } : {}),
      ...(agentId ? { agent_id: agentId } : {}),
    }),
    enabled: !!workspaceId,
    staleTime: 30_000,
  });

  const metrics = data?.指标 ?? {};
  const completeness = data?.summary_completeness;
  const completenessStatus = String(
    completeness?.["状态"] ??
    metrics["汇总完整性"] ??
    t(($) => $.observability.complete),
  );
  const maybeTruncated = Boolean(data?.sop_run_maybe_truncated || data?.task_trace_maybe_truncated || completenessStatus === "可能截断");
  const rows = [
    [t(($) => $.observability.metric_sop_runs), metricValue(metrics["SOP 执行数"])],
    [t(($) => $.observability.metric_sop_events), metricValue(metrics["SOP 事件数"])],
    [t(($) => $.observability.metric_queue_wait), durationValue(metrics["队列等待"])],
    [t(($) => $.observability.metric_execution_duration), durationValue(metrics["执行耗时"])],
    [t(($) => $.observability.metric_total_duration), durationValue(metrics["总耗时"])],
    [t(($) => $.observability.metric_input_tokens), metricValue(metrics["输入 token"])],
    [t(($) => $.observability.metric_output_tokens), metricValue(metrics["输出 token"])],
    [t(($) => $.observability.metric_estimated_cost), moneyValue(metrics["预估成本"])],
    [t(($) => $.observability.metric_retries), metricValue(metrics["重试次数"])],
    [t(($) => $.observability.metric_evidence), metricValue(metrics["证据数"])],
  ];

  return (
    <section className="rounded-md border border-border/70 bg-muted/20 px-3 py-2">
      <div className="mb-2 flex items-center justify-between gap-2">
        <div>
          <div className="text-xs font-medium text-foreground">{title}</div>
          <div className="mt-0.5 text-[11px] text-muted-foreground">{scopeLabel}</div>
        </div>
        <div className="text-right text-[11px] text-muted-foreground">
          <div>{t(($) => $.observability.task_trace_count, { count: data?.task_trace_sample_total ?? data?.task_trace_total ?? 0 })}</div>
          <div className={maybeTruncated ? "text-amber-700 dark:text-amber-300" : ""}>{completenessStatus}</div>
        </div>
      </div>
      {maybeTruncated && (
        <div className="mb-2 rounded-sm border border-amber-500/30 bg-amber-500/10 px-2 py-1 text-[11px] text-amber-800 dark:text-amber-200">
          {String(completeness?.["说明"] ?? t(($) => $.observability.truncated_hint))}
        </div>
      )}
      <div className="grid gap-1.5 sm:grid-cols-3">
        {rows.map(([label, value]) => (
          <div key={label} className="min-w-0">
            <div className="text-[11px] text-muted-foreground">{label}</div>
            <div className="truncate text-xs font-medium text-foreground">{value}</div>
          </div>
        ))}
      </div>
      <FailureReasons summary={data} />
      <UsageBreakdown title={t(($) => $.observability.model_breakdown)} rows={data?.model_breakdown ?? []} />
      <UsageBreakdown title={t(($) => $.observability.runtime_breakdown)} rows={data?.runtime_breakdown ?? []} />
    </section>
  );
}

function FailureReasons({ summary }: { summary?: ObservabilitySummary }) {
  const { t } = useT("common");
  const reasons = summary?.指标?.["失败原因"];
  if (!Array.isArray(reasons) || reasons.length === 0) return null;
  const reasonText = reasons
    .map((item) => String((item as Record<string, unknown>)["原因"] ?? ""))
    .filter(Boolean)
    .join("、");
  return (
    <div className="mt-2 border-t border-border/60 pt-2 text-[11px] text-muted-foreground">
      {t(($) => $.observability.failure_reasons, { reasons: reasonText })}
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
  const { t } = useT("common");
  const topRows = rows.slice(0, 3);
  if (topRows.length === 0) return null;
  return (
    <div className="mt-2 border-t border-border/60 pt-2">
      <div className="mb-1 text-[11px] font-medium text-muted-foreground">{title}</div>
      <div className="space-y-1">
        {topRows.map((row) => {
          const name = String(row["名称"] || row.model || row.runtime || t(($) => $.observability.unknown_name));
          const tokenTotal =
            Number(row["输入 token"] ?? 0) +
            Number(row["输出 token"] ?? 0) +
            Number(row["缓存读 token"] ?? 0) +
            Number(row["缓存写 token"] ?? 0);
          return (
            <div key={`${title}-${name}`} className="flex min-w-0 items-center gap-2 text-[11px]">
              <span className="min-w-0 flex-1 truncate text-foreground">{name}</span>
              <span className="shrink-0 text-muted-foreground">
                {t(($) => $.observability.usage_line, {
                  tokens: tokenTotal.toLocaleString("zh-CN"),
                  cost: moneyValue(row["预估成本"]),
                })}
                {row["价格已知"] ? "" : t(($) => $.observability.missing_price_suffix)}
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
