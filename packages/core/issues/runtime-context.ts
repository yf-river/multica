import type { TaskContextEvent } from "../types/issue-execution";

function finite(value: unknown): value is number {
  return typeof value === "number" && Number.isFinite(value);
}

function sum(values: unknown[]): number | undefined {
  const reported = values.filter(finite);
  return reported.length ? reported.reduce((total, value) => total + value, 0) : undefined;
}

function maximum(values: unknown[]): number | undefined {
  const reported = values.filter(finite);
  return reported.length ? reported.reduce((peak, value) => Math.max(peak, value)) : undefined;
}

/** 只汇总已报告指标，缺失值不参与零值统计。 */
export function summarizeTaskContext(events: TaskContextEvent[]) {
  const contexts = events.filter((event) => event.kind === "context");
  const compactions = events.filter((event) => event.kind === "compaction");
  const cachePairs = contexts.filter((event) => finite(event.input_tokens) && finite(event.cache_read_tokens));
  const cached = sum(cachePairs.map((event) => event.cache_read_tokens));
  const plain = sum(cachePairs.map((event) => event.input_tokens));
  const lastReported = (field: "system_tokens" | "tools_tokens" | "message_tokens") =>
    contexts.map((event) => event.breakdown?.[field]).filter(finite).at(-1);
  return {
    contexts, compactions,
    peak: maximum(contexts.map((event) => event.pressure_tokens)),
    window: maximum(contexts.map((event) => event.context_window)),
    modelMs: sum(contexts.map((event) => event.duration_ms)),
    input: sum(contexts.map((event) => event.input_tokens)),
    output: sum(contexts.map((event) => event.output_tokens)),
    read: sum(contexts.map((event) => event.cache_read_tokens)),
    system: lastReported("system_tokens"),
    tools: lastReported("tools_tokens"),
    messages: lastReported("message_tokens"),
    rate: cached !== undefined && plain !== undefined && cached + plain > 0 ? cached / (cached + plain) : undefined,
  };
}
