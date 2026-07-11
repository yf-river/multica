import { z } from "zod";

export const NonEmptyStringSchema = z.string().min(1);

// Nested attachment records intentionally validate only the identity field.
// Timeline and cancellation payloads must survive additive server fields.
export const EmbeddedAttachmentSchema = z.object({
  id: NonEmptyStringSchema,
}).loose();

export const TaskTraceEventSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  task_id: NonEmptyStringSchema,
  issue_id: z.string().nullable().optional().transform((value) => value ?? null),
  agent_id: NonEmptyStringSchema,
  runtime_id: z.string().nullable().optional().transform((value) => value ?? null),
  squad_id: z.string().nullable().optional().transform((value) => value ?? null),
  project_id: z.string().nullable().optional().transform((value) => value ?? null),
  source: z.string().default(""),
  event_type: z.string().default(""),
  event_name: z.string().default(""),
  status: z.string().default(""),
  attempt: z.number().default(0),
  duration_ms: z.number().nullable().optional(),
  queue_wait_ms: z.number().nullable().optional(),
  run_ms: z.number().nullable().optional(),
  total_ms: z.number().nullable().optional(),
  provider: z.string().default(""),
  model: z.string().default(""),
  input_tokens: z.number().default(0),
  output_tokens: z.number().default(0),
  cache_read_tokens: z.number().default(0),
  cache_write_tokens: z.number().default(0),
  failure_reason: z.string().default(""),
  error_type: z.string().default(""),
  trigger_comment_id: z.string().nullable().optional().transform((value) => value ?? null),
  autopilot_run_id: z.string().nullable().optional().transform((value) => value ?? null),
  chat_session_id: z.string().nullable().optional().transform((value) => value ?? null),
  metadata: z.record(z.string(), z.unknown()).default({}),
  created_at: z.string().default(""),
}).loose();
