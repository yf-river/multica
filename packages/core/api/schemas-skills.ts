import { z } from "zod";
import type { Skill, SkillSummary } from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

const SkillSummarySchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  name: z.string(),
  description: z.string().default(""),
  config: z.record(z.string(), z.unknown()).default({}),
  created_by: z.string().nullable(),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

const SkillFileSchema = z.object({
  id: NonEmptyStringSchema,
  skill_id: NonEmptyStringSchema,
  path: z.string(),
  content: z.string(),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

export const SkillSummaryListSchema = z.array(SkillSummarySchema);

export const SkillSchema = SkillSummarySchema.extend({
  content: z.string().default(""),
  files: z.array(SkillFileSchema).default([]),
}).loose();

export const EMPTY_SKILL_SUMMARIES: SkillSummary[] = [];

export const EMPTY_SKILL: Skill = {
  id: "",
  workspace_id: "",
  name: "",
  description: "",
  config: {},
  created_by: null,
  created_at: "",
  updated_at: "",
  content: "",
  files: [],
};
