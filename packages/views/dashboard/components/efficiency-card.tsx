"use client";

// 运行效率摘要：平均耗时、成功/失败/取消与用量覆盖率。
// 数据来自按 agent 的终态任务汇总；不把未上报用量的任务当成免费。

import type { DashboardAgentRunTime } from "@multica/core/types";
import { useT } from "../../i18n";

interface EfficiencyCardProps {
  rows: DashboardAgentRunTime[];
}

function duration(seconds: number): string {
  if (!seconds || seconds <= 0) return "0s";
  if (seconds < 60) return `${seconds.toFixed(1)}s`;
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${Math.round(seconds % 60)}s`;
}

export function EfficiencyCard({ rows }: EfficiencyCardProps) {
  const { t } = useT("usage");
  if (rows.length === 0) return null;
  let totalSeconds = 0;
  let taskCount = 0;
  let failed = 0;
  let cancelled = 0;
  let metered = 0;
  for (const row of rows) {
    totalSeconds += row.total_seconds;
    taskCount += row.task_count;
    failed += row.failed_count;
    cancelled += row.cancelled_count;
    metered += row.metered_task_count ?? row.task_count;
  }
  const success = Math.max(0, taskCount - failed - cancelled);
  const coverage = taskCount > 0 ? metered / taskCount : null;
  return (
    <section className="rounded-lg border bg-card">
      <header className="border-b px-4 py-3">
        <h2 className="text-body font-medium">{t(($) => $.efficiency.title)}</h2>
        <p className="mt-0.5 text-caption text-muted-foreground">{t(($) => $.efficiency.caption)}</p>
      </header>
      <div className="grid grid-cols-2 gap-3 p-4 sm:grid-cols-3 lg:grid-cols-5">
        <Metric label={t(($) => $.efficiency.avg_time)} value={duration(taskCount > 0 ? totalSeconds / taskCount : 0)} />
        <Metric label={t(($) => $.efficiency.success)} value={String(success)} />
        <Metric label={t(($) => $.efficiency.failed)} value={String(failed)} />
        <Metric label={t(($) => $.efficiency.cancelled)} value={String(cancelled)} />
        <Metric
          label={t(($) => $.efficiency.metered)}
          value={coverage === null ? "—" : `${Math.round(coverage * 100)}%`}
        />
      </div>
    </section>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-caption text-muted-foreground">{label}</div>
      <div className="text-body font-medium tabular-nums">{value}</div>
    </div>
  );
}
