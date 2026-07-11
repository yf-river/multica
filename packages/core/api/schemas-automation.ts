import { z } from "zod";
import type {
  Autopilot,
  AutopilotRun,
  AutopilotTrigger,
  GetAutopilotResponse,
  ListAutopilotRunsResponse,
  ListWebhookDeliveriesResponse,
  WebhookDelivery,
} from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

// Runtime response contracts for automation.
// Squad member status — backs the Squad detail page's Members tab. status
// is `string | null` (not the narrow `SquadMemberStatusValue` union) so a
// new server-side status doesn't fail the parse; the UI defaults to a
// neutral pill for unknown values.
const SquadActiveIssueBriefSchema = z.object({
  issue_id: z.string(),
  identifier: z.string(),
  title: z.string(),
  issue_status: z.string(),
}).loose();

const SquadMemberStatusSchema = z.object({
  member_type: z.string(),
  member_id: z.string(),
  status: z.string().nullable().optional().transform((v) => v ?? null),
  active_issues: z.array(SquadActiveIssueBriefSchema).default([]),
  last_active_at: z.string().nullable().optional().transform((v) => v ?? null),
}).loose();

export const SquadMemberStatusListResponseSchema = z.object({
  members: z.array(SquadMemberStatusSchema).default([]),
}).loose();

export const EMPTY_SQUAD_MEMBER_STATUS_LIST = { members: [] };

// ---------------------------------------------------------------------------
// Webhook delivery schemas — backing the Autopilot Deliveries section. Enums
// (`status`, `signature_status`, `provider`) are kept as `z.string()` so a
// future server-side value (e.g. a Stripe provider, a new dedupe state)
// degrades to a generic UI fallback rather than collapsing the list into
// the empty array. `.loose()` lets unknown fields pass through, matching
// the rule used by every other endpoint here.
// ---------------------------------------------------------------------------

const WebhookDeliverySchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  autopilot_id: z.string(),
  trigger_id: z.string(),
  provider: z.string(),
  event: z.string(),
  dedupe_key: z.string().nullable(),
  dedupe_source: z.string().nullable(),
  signature_status: z.string(),
  status: z.string(),
  attempt_count: z.number().default(0),
  content_type: z.string().nullable(),
  response_status: z.number().nullable(),
  autopilot_run_id: z.string().nullable(),
  replayed_from_delivery_id: z.string().nullable(),
  error: z.string().nullable(),
  received_at: z.string(),
  last_attempt_at: z.string(),
  created_at: z.string(),
  // Detail-only fields. The list endpoint omits them; the detail endpoint
  // populates raw_body / selected_headers / response_body.
  selected_headers: z.record(z.string(), z.unknown()).nullable().optional(),
  raw_body: z.string().nullable().optional(),
  response_body: z.string().nullable().optional(),
}).loose();

export const ListWebhookDeliveriesResponseSchema = z.object({
  deliveries: z.array(WebhookDeliverySchema).default([]),
  total: z.number().default(0),
}).loose();

export const WebhookDeliveryResponseSchema = WebhookDeliverySchema;

export const EMPTY_LIST_WEBHOOK_DELIVERIES_RESPONSE: ListWebhookDeliveriesResponse = {
  deliveries: [],
  total: 0,
};

// ---------------------------------------------------------------------------
// Autopilot list schema. Enums (`status`, `execution_mode`, `trigger_kinds`,
// `last_run_status`) stay `z.string()` so future server-side values degrade
// to a generic UI fallback. The three derived fields (trigger_kinds /
// next_run_at / last_run_status) are list-endpoint-only and absent on older
// servers — optional by contract, the list renders "—" without them.
// ---------------------------------------------------------------------------

const AutopilotSubscriberSchema = z.object({
  user_type: z.string(),
  user_id: NonEmptyStringSchema,
  created_at: z.string(),
}).loose();

export const AutopilotSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  title: z.string(),
  description: z.string().nullable().optional(),
  project_id: z.string().nullable().optional(),
  assignee_type: NonEmptyStringSchema,
  assignee_id: z.string(),
  status: z.string(),
  execution_mode: z.string(),
  issue_title_template: z.string().nullable().optional(),
  created_by_type: z.string(),
  created_by_id: z.string(),
  last_run_at: z.string().nullable().optional(),
  created_at: z.string(),
  updated_at: z.string(),
  trigger_kinds: z.array(z.string()).optional(),
  next_run_at: z.string().nullable().optional(),
  last_run_status: z.string().nullable().optional(),
  subscribers: z.array(AutopilotSubscriberSchema).default([]),
}).loose();

export const ListAutopilotsResponseSchema = z.object({
  autopilots: z.array(AutopilotSchema).default([]),
  total: z.number().default(0),
}).loose();

export const EMPTY_LIST_AUTOPILOTS_RESPONSE = {
  autopilots: [],
  total: 0,
};

export const EMPTY_AUTOPILOT: Autopilot = {
  id: "",
  workspace_id: "",
  title: "",
  description: null,
  project_id: null,
  assignee_type: "agent",
  assignee_id: "",
  status: "paused",
  execution_mode: "run_only",
  issue_title_template: null,
  created_by_type: "member",
  created_by_id: "",
  last_run_at: null,
  created_at: "",
  updated_at: "",
  subscribers: [],
};

const WebhookEventFilterSchema = z.object({
  event: z.string(),
  actions: z.array(z.string()).optional(),
}).loose();

const AutopilotTriggerWireSchema = z.object({
  id: NonEmptyStringSchema,
  autopilot_id: NonEmptyStringSchema,
  kind: z.string(),
  enabled: z.boolean(),
  cron_expression: z.string().nullable(),
  timezone: z.string().nullable(),
  next_run_at: z.string().nullable(),
  webhook_token: z.string().nullable(),
  webhook_path: z.string().nullable().optional(),
  webhook_url: z.string().nullable().optional(),
  provider: z.string().nullable().optional(),
  has_signing_secret: z.boolean().optional(),
  signing_secret_hint: z.string().nullable().optional(),
  label: z.string().nullable(),
  event_filters: z.array(WebhookEventFilterSchema).nullable().optional(),
  last_fired_at: z.string().nullable(),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

export const AutopilotTriggerSchema = AutopilotTriggerWireSchema.transform((wire) => {
  const safe: Record<string, unknown> = { ...wire };
  // webhook_token is an intentional current bearer-path contract. Signing
  // secrets are write-only and must never cross this response boundary even
  // if a future server serializer accidentally adds one.
  delete safe.signing_secret;
  delete safe.encrypted_signing_secret;
  delete safe.signing_secret_ciphertext;
  return safe;
});

export const CreateAutopilotResponseSchema = AutopilotSchema.extend({
  initial_trigger: AutopilotTriggerSchema,
});

export const EMPTY_AUTOPILOT_TRIGGER: AutopilotTrigger = {
  id: "",
  autopilot_id: "",
  kind: "api",
  enabled: false,
  cron_expression: null,
  timezone: null,
  next_run_at: null,
  webhook_token: null,
  webhook_path: null,
  webhook_url: null,
  label: null,
  event_filters: null,
  last_fired_at: null,
  created_at: "",
  updated_at: "",
};

export const AutopilotRunSchema = z.object({
  id: NonEmptyStringSchema,
  autopilot_id: NonEmptyStringSchema,
  trigger_id: z.string().nullable(),
  source: z.string(),
  status: z.string(),
  issue_id: z.string().nullable(),
  task_id: z.string().nullable(),
  triggered_at: z.string(),
  completed_at: z.string().nullable(),
  failure_reason: z.string().nullable(),
  trigger_payload: z.unknown().default(null),
  result: z.unknown().default(null),
  created_at: z.string(),
}).loose();

export const EMPTY_AUTOPILOT_RUN: AutopilotRun = {
  id: "",
  autopilot_id: "",
  trigger_id: null,
  source: "manual",
  status: "failed",
  issue_id: null,
  task_id: null,
  triggered_at: "",
  completed_at: null,
  failure_reason: null,
  trigger_payload: null,
  result: null,
  created_at: "",
};

export const GetAutopilotResponseSchema = z.object({
  autopilot: AutopilotSchema,
  triggers: z.array(AutopilotTriggerSchema).default([]),
}).loose();

export const EMPTY_GET_AUTOPILOT_RESPONSE: GetAutopilotResponse = {
  autopilot: EMPTY_AUTOPILOT,
  triggers: [],
};

export const ListAutopilotRunsResponseSchema = z.object({
  runs: z.array(AutopilotRunSchema).default([]),
  total: z.number().default(0),
}).loose();

export const EMPTY_LIST_AUTOPILOT_RUNS_RESPONSE: ListAutopilotRunsResponse = {
  runs: [],
  total: 0,
};

export const EMPTY_WEBHOOK_DELIVERY: WebhookDelivery = {
  id: "",
  workspace_id: "",
  autopilot_id: "",
  trigger_id: "",
  provider: "",
  event: "",
  dedupe_key: null,
  dedupe_source: null,
  signature_status: "not_required",
  status: "queued",
  attempt_count: 0,
  content_type: null,
  response_status: null,
  autopilot_run_id: null,
  replayed_from_delivery_id: null,
  error: null,
  received_at: "",
  last_attempt_at: "",
  created_at: "",
};
