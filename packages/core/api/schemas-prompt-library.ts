import { z } from "zod";
import type {
  ListPromptLibraryItemsResponse,
  ListPromptLibraryTrialsResponse,
  ListPromptLibraryVersionsResponse,
  AgentPlaygroundDetail,
  AgentPlaygroundExperiment,
  ListAgentPlaygroundExperimentsResponse,
  PromptLibraryItem,
  PromptLibraryTrial,
  PromptLibraryVersion,
} from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";
// Runtime response contracts for prompt library.
const PromptLibraryVariableSchema = z.object({
  name: z.string(),
  label: z.string().optional(),
  required: z.boolean().optional(),
  description: z.string().optional(),
  default_value: z.string().optional(),
}).loose();

export const PromptLibraryItemSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  project_id: z.string().nullable().optional().transform((v) => v ?? null),
  name: z.string(),
  description: z.string().default(""),
  prompt_type: z.string().default("text"),
  content: z.string(),
  variables: z.array(PromptLibraryVariableSchema).default([]),
  tags: z.array(z.string()).default([]),
  status: z.enum(["启用", "归档"]).default("启用"),
  version: z.number().default(1),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

export const PromptLibraryItemListResponseSchema = z.object({
  items: z.array(PromptLibraryItemSchema).default([]),
  total: z.number().default(0),
}).loose();

export const PromptLibraryVersionSchema = z.object({
  id: NonEmptyStringSchema,
  prompt_id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  project_id: z.string().nullable().optional().transform((v) => v ?? null),
  version: z.number().default(1),
  name: z.string(),
  description: z.string().default(""),
  prompt_type: z.string().default("text"),
  content: z.string(),
  variables: z.array(PromptLibraryVariableSchema).default([]),
  tags: z.array(z.string()).default([]),
  source: z.enum(["手动创建", "手动更新", "优化候选发布", "历史回填"]).default("历史回填"),
  source_candidate_id: z.string().nullable().optional().transform((v) => v ?? null),
  change_note: z.string().default(""),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string(),
}).loose();

export const PromptLibraryVersionListResponseSchema = z.object({
  items: z.array(PromptLibraryVersionSchema).default([]),
  total: z.number().default(0),
}).loose();

export const CreatePromptLibraryVersionResponseSchema = z.object({
  item: PromptLibraryItemSchema,
  version: PromptLibraryVersionSchema,
}).loose();

export const PromptLibraryTrialSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  prompt_id: NonEmptyStringSchema,
  version_id: NonEmptyStringSchema,
  agent_id: NonEmptyStringSchema,
  chat_session_id: z.string().nullable().optional().transform((v) => v ?? null),
  task_id: z.string().nullable().optional().transform((v) => v ?? null),
  input: z.string().default(""),
  rendered_message: z.string().default(""),
  variables: z.record(z.string(), z.unknown()).default({}),
  status: z.string().default("queued"),
  output_preview: z.string().default(""),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

export const PromptLibraryTrialListResponseSchema = z.object({
  items: z.array(PromptLibraryTrialSchema).default([]),
  total: z.number().default(0),
}).loose();

export const EMPTY_PROMPT_LIBRARY_ITEM: PromptLibraryItem = {
  id: "",
  workspace_id: "",
  project_id: null,
  name: "",
  description: "",
  prompt_type: "text",
  content: "",
  variables: [],
  tags: [],
  status: "启用",
  version: 1,
  created_by: null,
  created_at: "",
  updated_at: "",
};

export const EMPTY_PROMPT_LIBRARY_LIST_RESPONSE: ListPromptLibraryItemsResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_LIBRARY_VERSION: PromptLibraryVersion = {
  id: "",
  prompt_id: "",
  workspace_id: "",
  project_id: null,
  version: 1,
  name: "",
  description: "",
  prompt_type: "text",
  content: "",
  variables: [],
  tags: [],
  source: "历史回填",
  source_candidate_id: null,
  change_note: "",
  created_by: null,
  created_at: "",
};

export const EMPTY_PROMPT_LIBRARY_VERSION_LIST_RESPONSE: ListPromptLibraryVersionsResponse = {
  items: [],
  total: 0,
};

export const EMPTY_PROMPT_LIBRARY_TRIAL: PromptLibraryTrial = {
  id: "",
  workspace_id: "",
  prompt_id: "",
  version_id: "",
  agent_id: "",
  chat_session_id: null,
  task_id: null,
  input: "",
  rendered_message: "",
  variables: {},
  status: "queued",
  output_preview: "",
  created_by: null,
  created_at: "",
  updated_at: "",
};

export const EMPTY_PROMPT_LIBRARY_TRIAL_LIST_RESPONSE: ListPromptLibraryTrialsResponse = {
  items: [],
  total: 0,
};

export const AgentPlaygroundExperimentSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  name: z.string(),
  description: z.string().default(""),
  dataset_asset_id: z.string().nullable().optional().transform((v) => v ?? null),
  dataset_version_id: z.string().nullable().optional().transform((v) => v ?? null),
  judge_agent_id: z.string().nullable().optional().transform((v) => v ?? null),
  status: z.string().default("ready"),
  created_by: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
  input_count: z.number().default(0),
  agent_count: z.number().default(0),
}).loose();

export const AgentPlaygroundInputSchema = z.object({
  id: z.string(),
  row_index: z.number().default(0),
  name: z.string().default(""),
  input: z.string().default(""),
  variables: z.record(z.string(), z.unknown()).default({}),
  expected: z.string().default(""),
  dataset_row_id: z.string().nullable().optional().transform((v) => v ?? null),
  created_at: z.string().default(""),
}).loose();

export const AgentPlaygroundAgentSchema = z.object({
  id: z.string(),
  agent_id: z.string(),
  agent_name: z.string().default(""),
  agent_model: z.string().nullable().optional().transform((v) => v ?? null),
  display_order: z.number().default(0),
}).loose();

export const AgentPlaygroundResultSchema = z.object({
  id: z.string(),
  input_id: z.string(),
  experiment_agent_id: z.string(),
  agent_id: z.string(),
  chat_session_id: z.string().nullable().optional().transform((v) => v ?? null),
  task_id: z.string().nullable().optional().transform((v) => v ?? null),
  rendered_input: z.string().default(""),
  status: z.string().default("pending"),
  output: z.string().default(""),
  error: z.string().default(""),
  started_at: z.string().nullable().optional().transform((v) => v ?? null),
  completed_at: z.string().nullable().optional().transform((v) => v ?? null),
  updated_at: z.string().default(""),
}).loose();

export const AgentPlaygroundJudgementSchema = z.object({
  id: z.string(),
  input_id: z.string(),
  judge_agent_id: z.string(),
  chat_session_id: z.string().nullable().optional().transform((v) => v ?? null),
  task_id: z.string().nullable().optional().transform((v) => v ?? null),
  status: z.string().default("pending"),
  output: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

export const AgentPlaygroundDetailSchema = z.object({
  experiment: AgentPlaygroundExperimentSchema,
  inputs: z.array(AgentPlaygroundInputSchema).default([]),
  agents: z.array(AgentPlaygroundAgentSchema).default([]),
  results: z.array(AgentPlaygroundResultSchema).default([]),
  judgements: z.array(AgentPlaygroundJudgementSchema).default([]),
}).loose();

export const AgentPlaygroundExperimentListResponseSchema = z.object({
  items: z.array(AgentPlaygroundExperimentSchema).default([]),
  total: z.number().default(0),
}).loose();

export const EMPTY_AGENT_PLAYGROUND_EXPERIMENT: AgentPlaygroundExperiment = {
  id: "",
  workspace_id: "",
  name: "",
  description: "",
  dataset_asset_id: null,
  dataset_version_id: null,
  judge_agent_id: null,
  status: "ready",
  created_by: null,
  created_at: "",
  updated_at: "",
  input_count: 0,
  agent_count: 0,
};

export const EMPTY_AGENT_PLAYGROUND_DETAIL: AgentPlaygroundDetail = {
  experiment: EMPTY_AGENT_PLAYGROUND_EXPERIMENT,
  inputs: [],
  agents: [],
  results: [],
  judgements: [],
};

export const EMPTY_AGENT_PLAYGROUND_EXPERIMENT_LIST_RESPONSE: ListAgentPlaygroundExperimentsResponse = {
  items: [],
  total: 0,
};
