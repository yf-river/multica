import { z } from "zod";
import type {
  AgentPlaygroundDetail,
  AgentPlaygroundExperiment,
  ListAgentPlaygroundExperimentsResponse,
  PromptLibraryItem,
  PromptLibraryTrial,
  PromptLibraryVersion,
} from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";
// Runtime response contracts for prompt library.
export const PromptLibraryItemSchema = z.object({
  id: NonEmptyStringSchema,
  name: z.string(),
  description: z.string().default(""),
  content: z.string(),
  version: z.number().default(1),
}).loose();

export const PromptLibraryItemListResponseSchema = z.object({
  items: z.array(PromptLibraryItemSchema).default([]),
  total: z.number().default(0),
}).loose();

const PromptLibraryVersionSchema = z.object({
  id: NonEmptyStringSchema,
  version: z.number().default(1),
  name: z.string(),
  description: z.string().default(""),
  content: z.string(),
  source_candidate_id: z.string().nullable().optional().transform((v) => v ?? null),
  change_note: z.string().default(""),
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
  agent_id: NonEmptyStringSchema,
  variables: z.record(z.string(), z.unknown()).default({}),
  status: z.string().default("queued"),
  output_preview: z.string().default(""),
  created_at: z.string(),
}).loose();

export const PromptLibraryTrialListResponseSchema = z.object({
  items: z.array(PromptLibraryTrialSchema).default([]),
  total: z.number().default(0),
}).loose();

export const EMPTY_PROMPT_LIBRARY_ITEM: PromptLibraryItem = {
  id: "",
  name: "",
  description: "",
  content: "",
  version: 1,
};

export const EMPTY_PROMPT_LIBRARY_VERSION: PromptLibraryVersion = {
  id: "",
  version: 1,
  name: "",
  description: "",
  content: "",
  source_candidate_id: null,
  change_note: "",
  created_at: "",
};

export const EMPTY_PROMPT_LIBRARY_TRIAL: PromptLibraryTrial = {
  id: "",
  agent_id: "",
  variables: {},
  status: "queued",
  output_preview: "",
  created_at: "",
};

const AgentPlaygroundExperimentSchema = z.object({
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

const AgentPlaygroundInputSchema = z.object({
  id: z.string(),
  row_index: z.number().default(0),
  name: z.string().default(""),
  input: z.string().default(""),
  variables: z.record(z.string(), z.unknown()).default({}),
  expected: z.string().default(""),
  created_at: z.string().default(""),
}).loose();

const AgentPlaygroundAgentSchema = z.object({
  id: z.string(),
  agent_id: z.string(),
  agent_name: z.string().default(""),
}).loose();

const AgentPlaygroundResultSchema = z.object({
  id: z.string(),
  input_id: z.string(),
  experiment_agent_id: z.string(),
  agent_id: z.string(),
  chat_session_id: z.string().nullable().optional().transform((v) => v ?? null),
  task_id: z.string().nullable().optional().transform((v) => v ?? null),
  status: z.string().default("pending"),
  output: z.string().default(""),
  error: z.string().default(""),
  started_at: z.string().nullable().optional().transform((v) => v ?? null),
  completed_at: z.string().nullable().optional().transform((v) => v ?? null),
  updated_at: z.string().default(""),
}).loose();

const AgentPlaygroundJudgementSchema = z.object({
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

const EMPTY_AGENT_PLAYGROUND_EXPERIMENT: AgentPlaygroundExperiment = {
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
