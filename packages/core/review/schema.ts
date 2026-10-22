import { z } from "zod";

/** 「审核中」列的审查设置。agent_id 为空表示还没建过托管审查 Agent。 */
export const reviewSettingsSchema = z
  .object({
    configured: z.boolean().default(false),
    enabled: z.boolean(),
    agent_id: z.string().nullable(),
    runtime_id: z.string().nullable(),
    model: z.string().default(""),
    thinking_level: z.string().default(""),
    instructions: z.string(),
    instructions_customized: z.boolean().default(false),
    default_instructions: z.string(),
    instructions_version: z.string(),
    max_rounds: z.number().int().positive().default(6),
    can_manage: z.boolean().default(false),
  })
  .loose();

export type ReviewSettings = z.infer<typeof reviewSettingsSchema>;

export const reviewInstructionsSchema = z.object({
  instructions: z.string(),
  version: z.string(),
});

export type ReviewInstructions = z.infer<typeof reviewInstructionsSchema>;

export const EMPTY_REVIEW_SETTINGS: ReviewSettings = {
  configured: false,
  enabled: false,
  agent_id: null,
  runtime_id: null,
  model: "",
  thinking_level: "",
  instructions: "",
  instructions_customized: false,
  default_instructions: "",
  instructions_version: "",
  max_rounds: 6,
  can_manage: false,
};
