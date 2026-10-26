"use client";

// 统计页的模型/运行时拆分：按 provider + model 折叠 token、任务与缓存命中。
// 运行时由 provider 直接表达（codex / dsh / codebuddy …），因此不额外建维度。

import type { ModelDashboardRow } from "../utils";
import { formatTokens, formatUsd } from "../../runtimes/utils";
import { useT } from "../../i18n";

interface ModelBreakdownProps {
  rows: ModelDashboardRow[];
  costKnown: boolean;
}

function cacheHit(row: ModelDashboardRow): number | null {
  const total = row.cacheReadTokens + row.inputTokens;
  return total > 0 ? row.cacheReadTokens / total : null;
}

export function ModelBreakdown({ rows, costKnown }: ModelBreakdownProps) {
  const { t } = useT("usage");
  if (rows.length === 0) return null;
  return (
    <section className="rounded-lg border bg-card">
      <header className="border-b px-4 py-3">
        <h2 className="text-body font-medium">{t(($) => $.models.title)}</h2>
        <p className="mt-0.5 text-caption text-muted-foreground">{t(($) => $.models.caption)}</p>
      </header>
      <div className="overflow-x-auto">
        <table className="w-full text-caption tabular-nums">
          <thead>
            <tr className="text-left text-muted-foreground">
              <th className="px-4 py-2 font-normal">{t(($) => $.models.header_model)}</th>
              <th className="px-3 py-2 text-right font-normal">{t(($) => $.models.header_tasks)}</th>
              <th className="px-3 py-2 text-right font-normal">{t(($) => $.models.header_input)}</th>
              <th className="px-3 py-2 text-right font-normal">{t(($) => $.models.header_output)}</th>
              <th className="px-3 py-2 text-right font-normal">{t(($) => $.models.header_cache)}</th>
              <th className="px-3 py-2 text-right font-normal">{t(($) => $.models.header_cache_hit)}</th>
              <th className="px-4 py-2 text-right font-normal">{t(($) => $.models.header_cost)}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => {
              const hit = cacheHit(row);
              return (
                <tr key={`${row.provider}/${row.model}`} className="border-t">
                  <td className="px-4 py-2">
                    <div className="flex flex-col">
                      <span className="font-medium">{row.model}</span>
                      <span className="text-caption text-muted-foreground">{row.provider || "—"}</span>
                    </div>
                  </td>
                  <td className="px-3 py-2 text-right">{row.taskCount}</td>
                  <td className="px-3 py-2 text-right">{formatTokens(row.inputTokens)}</td>
                  <td className="px-3 py-2 text-right">{formatTokens(row.outputTokens)}</td>
                  <td className="px-3 py-2 text-right">{formatTokens(row.cacheReadTokens)}</td>
                  <td className="px-3 py-2 text-right">{hit === null ? "—" : `${Math.round(hit * 100)}%`}</td>
                  <td className="px-4 py-2 text-right">
                    {row.cost > 0 ? formatUsd(row.cost) : costKnown ? formatUsd(0) : "—"}
                    {row.uncostedTokens > 0 && (
                      <span className="ml-1 text-caption text-muted-foreground">
                        {t(($) => $.models.uncosted, { tokens: formatTokens(row.uncostedTokens) })}
                      </span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}
