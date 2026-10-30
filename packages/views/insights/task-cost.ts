import { useMemo } from "react";
import type { AgentTask } from "@multica/core/types";
import type { SessionCostUsage } from "@multica/context-insights/types";
import { addBilledUsage } from "@multica/context-insights/host/foldMetering.js";
import {
  estimateSessionCost,
  billedTokensOf,
  mergeCostUsage,
} from "@multica/context-insights/cost";
import { useModelPrices } from "./native-dist/client/modelPrices";

// 任务摘要与洞察使用同一价格目录和原版 provider/model、峰谷匹配规则。
export function useTaskReferenceCost(tasks: readonly AgentTask[]) {
  const { book } = useModelPrices();
  return useMemo(() => {
    let usage: SessionCostUsage | undefined;
    const models: string[] = [];
    let input = 0,
      output = 0,
      cacheRead = 0,
      cacheWrite = 0;
    for (const task of tasks) {
      if (task.insight_costs) {
        usage = mergeCostUsage(usage, ...task.insight_costs) ?? undefined;
        continue;
      }
      for (const row of task.usage ?? []) {
        usage = addBilledUsage(usage, row.provider ?? "", row.model,
          new Date(task.completed_at ?? task.started_at ?? task.created_at).getTime(),
          {uncached: row.input_tokens, cacheRead: row.cache_read_tokens,
           cacheWrite: row.cache_write_tokens, output: row.output_tokens});
      }
    }
    for (const provider of Object.values(usage ?? {}))
      for (const [model, periods] of Object.entries(provider)) {
        if (!models.includes(model)) models.push(model);
        for (const bucket of [periods.peak, periods.off]) if (bucket) {
          input += bucket.uncached; output += bucket.output;
          cacheRead += bucket.cacheRead; cacheWrite += bucket.cacheWrite;
        }
      }
    return usage
      ? {
          input,
          output,
          cacheRead,
          cacheWrite,
          tokens: billedTokensOf(usage),
          cost: estimateSessionCost(usage, book, "usd"),
          models,
        }
      : null;
  }, [tasks, book]);
}
