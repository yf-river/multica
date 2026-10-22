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
    default_instructions: z.string(),
    max_rounds: z.number().int().positive().default(6),
    can_manage: z.boolean().default(false),
  })
  .loose();

export type ReviewSettings = z.infer<typeof reviewSettingsSchema>;

export const EMPTY_REVIEW_SETTINGS: ReviewSettings = {
  configured: false,
  enabled: false,
  agent_id: null,
  runtime_id: null,
  model: "",
  thinking_level: "",
  instructions: "",
  default_instructions: "",
  max_rounds: 6,
  can_manage: false,
};
