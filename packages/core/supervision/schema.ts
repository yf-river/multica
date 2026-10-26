import { z } from "zod";

const time = z.string().nullable();
export const supervisionConfigSchema = z.object({
  concurrency: z.number().int().positive().nullable().optional(), enabled: z.boolean(), investigation_enabled: z.boolean().default(false), agent_id: z.string().nullable(), interval_seconds: z.number(),
  inventory_complete: z.boolean(), last_scan_at: time.optional(), last_success_at: time.optional(), error: z.string(),
  runtime_id: z.string().nullable().optional(), model: z.string().nullable().optional(), thinking_level: z.string().nullable().optional(),
  instructions: z.string().default(""), instructions_customized: z.boolean().default(false), instructions_version: z.string().optional(),
}).loose();
export const supervisionInstructionsSchema = z.object({ instructions: z.string(), version: z.string() }).loose();
export type SupervisionInstructions = z.infer<typeof supervisionInstructionsSchema>;
export const supervisionSnapshotSchema = z.object({
  config: supervisionConfigSchema, can_manage: z.boolean(),
  summary: z.object({ total: z.number(), covered: z.number(), pending: z.number(), running: z.number(), no_runs: z.number(), waiting: z.number().default(0),
    incomplete: z.number(), failed: z.number(), retrying:z.number().default(0), open_findings: z.number(), issues_with_findings: z.number(), oldest_pending_at: time }),
}).loose();
export const supervisionSubjectSchema = z.object({
  issue_id: z.string(), title: z.string(), identifier: z.string(), business_status: z.string(), status_category: z.string().default(""),
  project_id: z.string().nullable(),
  desired_version: z.number(), reviewed_version: z.number(), summary: z.string(), efficiency: z.string(), correctness: z.string(), run_count: z.number(),
  gaps: z.array(z.string()), last_checked_at: time, error: z.string(), failures: z.number(), next_attempt_at:time.optional(), stopped: z.boolean().default(false), running: z.boolean(), open_findings: z.number(),
  round_sequence: z.number().nullable().default(null), round_opened_at: time.default(null), round_cutoff: time.default(null),
  pending_materials: z.number().default(0), manual_running: z.boolean().default(false), manual_checked: z.boolean().default(false),
}).loose();
export const supervisionListSchema = z.object({ items: z.array(supervisionSubjectSchema), has_more: z.boolean(), offset: z.number() });
export const supervisionEvidenceSchema = z.object({ kind: z.string(), id: z.string(), run_id: z.string().optional(), batch_id: z.string().optional() });
export const supervisionFindingSchema = z.object({
  id: z.string(), title: z.string(), description: z.string(), impact: z.string(), recommendation: z.string(), confidence: z.string(),
  evidence: z.array(supervisionEvidenceSchema), status: z.string(), human_resolution: z.boolean(),
  first_seen_at: z.string(), last_seen_at: z.string(), snoozed_until: time,
}).loose();
export const supervisionDetailSchema = z.object({
  supervisor_usage: z.array(z.object({ provider: z.string(), model: z.string(), input_tokens: z.number().nonnegative(), output_tokens: z.number().nonnegative(), cache_read_tokens: z.number().nonnegative(), cache_write_tokens: z.number().nonnegative() })).default([]),
  // 1. Older backends omit preparation summaries; malformed counters remain detectable at the boundary.
  trace_materials: z.array(z.object({
    id: z.string(), run_id: z.string(), runtime: z.string(), status: z.string(), material_version: z.number().int(), queried_at: time,
    gaps: z.array(z.string()).default([]), matched_requests: z.number().int().nonnegative(), local_snapshots: z.number().int().nonnegative(),
    preparation_http_requests: z.number().int().nonnegative(), preparation_duration_ms: z.number().nonnegative(), differences: z.number().int().nonnegative(),
    runtime_observation: z.object({
      requests: z.number().int().nonnegative(),
      context_window: z.number().nonnegative().optional(),
      peak_pressure_tokens: z.number().nonnegative().optional(),
      projected_tokens: z.number().nonnegative().optional(),
      compactions: z.number().int().nonnegative().optional(),
      model_duration_ms: z.number().nonnegative().optional(),
      input_tokens: z.number().nonnegative().optional(),
      output_tokens: z.number().nonnegative().optional(),
      cache_read_tokens: z.number().nonnegative().optional(),
      breakdown: z.object({ system_tokens: z.number().nonnegative(), tools_tokens: z.number().nonnegative(), message_tokens: z.number().nonnegative() }).loose().optional(),
    }).loose().nullable().optional(),
  }).loose()).default([]),
  subject: z.object({ summary: z.string(), efficiency: z.string(), correctness: z.string(), gaps: z.array(z.string()),
    desired_version: z.number(), reviewed_version: z.number(), last_checked_at: time, failures: z.number(), next_attempt_at:time.optional(), stopped: z.boolean().default(false), error: z.string() }).loose(),
  title: z.string(), description: z.string(), findings: z.array(supervisionFindingSchema),
  assignee_type: z.string().nullable(), assignee_id: z.string().nullable(),
  notes: z.array(z.object({ id: z.string(), content: z.string(), created_at: z.string() })),
  actions: z.array(z.object({ id: z.string(), finding_id: z.string(), actor_id: z.string(), action: z.string(), reason: z.string(), created_at: z.string(), receipt: z.object({ evidence: z.array(supervisionEvidenceSchema).optional() }).loose().nullable() })),
  investigations: z.array(z.object({ id:z.string(), batch_id:z.string(), created_at:z.string(), request:z.object({ reason:z.string(), path:z.string().optional() }).loose(), result:z.object({ tool:z.string(), source_label:z.string().optional(), path:z.string().optional(), error:z.string().optional(), offset:z.number(), end:z.number(), length:z.number(), truncated:z.boolean(), scope_truncated:z.boolean().optional(), content_truncated:z.boolean().optional(), next_cursor:z.string().optional() }).loose() })).default([]),
  round: z.object({ id: z.string(), sequence: z.number(), status: z.string(), material_cutoff: z.string(), close_reason: z.string().default(""), opened_at: z.string(), closed_at: time }).loose().nullable().optional(),
  batches: z.array(z.object({ id: z.string(), task_id: z.string().nullable(), status: z.string(), error: z.string(), rules_version: z.string(),
    scope: z.string().nullable().optional(), round_sequence: z.number().nullable().optional(), round_cutoff: time.optional(),
    instructions: z.string().nullable().optional(), instructions_sha256: z.string().nullable().optional(),
    queue_seconds: z.number().nullable().optional(), execution_seconds: z.number().nullable().optional(), persistence_seconds: z.number().nullable().optional(), created_at: z.string(), completed_at: time, material_count: z.number() })),
  coverage: z.object({ materials: z.number(), covered: z.number(), runs: z.number(), partial: z.number().optional(), pending: z.number().optional(),
    round_materials: z.number().optional(), round_covered: z.number().optional(), round_pending: z.number().optional(),
    current: z.object({ kind: z.string(), id: z.string(), offset: z.number(), length: z.number().nullable() }).nullable().optional() }),
}).loose();
export type SupervisionSnapshot = z.infer<typeof supervisionSnapshotSchema>;
export type SupervisionList = z.infer<typeof supervisionListSchema>;
export type SupervisionDetail = z.infer<typeof supervisionDetailSchema>;
export type SupervisionFinding = z.infer<typeof supervisionFindingSchema>;
export type SupervisionConfig = z.infer<typeof supervisionConfigSchema>;

export const supervisionFeedbackSchema = z.object({
  id: z.string().optional(), comment_id: z.string().optional(), pending: z.boolean().optional(),
  reconciled: z.boolean().optional(), trigger_status: z.string().optional(),
  trigger_outcomes: z.array(z.object({ target_type: z.string(), target_id: z.string(), status: z.string(), reason_code: z.string() })).optional(),
}).loose().refine((value) => Boolean(value.id || value.comment_id || value.pending === true));
export type SupervisionFeedback = z.infer<typeof supervisionFeedbackSchema>;

export const supervisionEvidencePageSchema = z.object({ kind: z.string(), id: z.string(), run_id: z.string().optional(), batch_id: z.string().optional(), snapshot: z.boolean().optional(),
  issue_id: z.string(), text: z.string(), offset: z.number(), end: z.number(), length: z.number(), deleted: z.boolean() }).loose();
export type SupervisionEvidencePage = z.infer<typeof supervisionEvidencePageSchema>;

export const EMPTY_SUPERVISION: SupervisionSnapshot = {
  config: { enabled: false, investigation_enabled:false, agent_id: null, interval_seconds: 300, inventory_complete: false, error: "", instructions: "", instructions_customized: false }, can_manage: false,
  summary: { total: 0, covered: 0, pending: 0, running: 0, no_runs: 0, waiting: 0, incomplete: 0, failed: 0, retrying:0, open_findings: 0, issues_with_findings: 0, oldest_pending_at: null },
};
export const EMPTY_SUPERVISION_LIST: SupervisionList = { items: [], has_more: false, offset: 0 };
