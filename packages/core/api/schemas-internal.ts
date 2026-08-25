import { z } from "zod";

export const NonEmptyStringSchema = z.string().min(1);

// Nested attachment records intentionally validate only the identity field.
// Timeline and cancellation payloads must survive additive server fields.
export const EmbeddedAttachmentSchema = z.object({
  id: NonEmptyStringSchema,
}).loose();

export const TaskTraceEventSchema = z.object({
  id: NonEmptyStringSchema,
  task_id: NonEmptyStringSchema,
  agent_id: NonEmptyStringSchema,
  runtime_id: z.string().nullable().optional().transform((value) => value ?? null),
  source: z.string().default(""),
  event_type: z.string().default(""),
  event_name: z.string().default(""),
  status: z.string().default(""),
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
  metadata: z.record(z.string(), z.unknown()).default({}),
  created_at: z.string().default(""),
}).loose();
