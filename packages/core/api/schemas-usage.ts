import { z } from "zod";

// Runtime response contracts for usage.
// ---------------------------------------------------------------------------
// Workspace dashboard schemas
//
// The dashboard hits three independent rollup endpoints. Each returns a flat
// array, and every field is consumed by chart / KPI math — a missing number
// silently degrades to NaN downstream, so we coerce missing numbers to 0.
// String fields default to "" (no enum narrowing) to survive future model /
// agent ID drift, and so a single null from tz-aware SQL bucketing fails
// only that row instead of dropping the whole array to the `[]` fallback.
// ---------------------------------------------------------------------------

const UsageTokenFieldsSchema = {
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
};

const AttributedUsageFieldsSchema = {
  provider: z.string().default(""),
  ...UsageTokenFieldsSchema,
  cost_usd: z.number().default(0),
};

const UsageCostBreakdownFieldsSchema = {
  input_cost_usd: z.number().default(0),
  output_cost_usd: z.number().default(0),
  cache_write_cost_usd: z.number().default(0),
};

const DashboardUsageDailySchema = z.object({
  date: z.string().default(""),
  ...UsageTokenFieldsSchema,
  cost_usd: z.number().default(0),
  ...UsageCostBreakdownFieldsSchema,
  task_count: z.number().default(0),
}).loose();

export const DashboardUsageDailyListSchema = z.array(DashboardUsageDailySchema);

const DashboardUsageByAgentSchema = z.object({
  agent_id: z.string().default(""),
  ...AttributedUsageFieldsSchema,
  task_count: z.number().default(0),
}).loose();

export const DashboardUsageByAgentListSchema = z.array(DashboardUsageByAgentSchema);

const DashboardAgentRunTimeSchema = z.object({
  agent_id: z.string().default(""),
  total_seconds: z.number().default(0),
  task_count: z.number().default(0),
  failed_count: z.number().default(0),
}).loose();

export const DashboardAgentRunTimeListSchema = z.array(DashboardAgentRunTimeSchema);

const DashboardRunTimeDailySchema = z.object({
  date: z.string().default(""),
  total_seconds: z.number().default(0),
  task_count: z.number().default(0),
  failed_count: z.number().default(0),
}).loose();

export const DashboardRunTimeDailyListSchema = z.array(DashboardRunTimeDailySchema);

// ---------------------------------------------------------------------------
// Runtime usage schemas — the runtime-detail page's current usage endpoints
// (`/api/runtimes/:id/usage*`). Same leniency rules as the dashboard
// schemas above: numbers default to 0, strings to "", `.loose()` passes
// unknown fields.
// ---------------------------------------------------------------------------

const RuntimeUsageSchema = z.object({
  date: z.string().default(""),
  model: z.string().default(""),
  ...AttributedUsageFieldsSchema,
  ...UsageCostBreakdownFieldsSchema,
  cache_savings_usd: z.number().default(0),
  priced: z.boolean().default(false),
}).loose();

export const RuntimeUsageListSchema = z.array(RuntimeUsageSchema);

const RuntimeUsageByAgentSchema = z.object({
  agent_id: z.string().default(""),
  ...AttributedUsageFieldsSchema,
  task_count: z.number().default(0),
}).loose();

export const RuntimeUsageByAgentListSchema = z.array(RuntimeUsageByAgentSchema);

const RuntimeUsageByTaskSchema = z.object({
  task_id: z.string().default(""),
  issue_id: z.string().nullable().default(null),
  issue_number: z.number().default(0),
  issue_title: z.string().default(""),
  agent_id: z.string().default(""),
  status: z.string().default(""),
  started_at: z.string().nullable().default(null),
  completed_at: z.string().nullable().default(null),
  ...AttributedUsageFieldsSchema,
}).loose();

export const RuntimeUsageByTaskListSchema = z.array(RuntimeUsageByTaskSchema);
