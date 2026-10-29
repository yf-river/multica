import { z } from "zod";

export const releaseAgentSettingsSchema = z.object({
  configured: z.boolean(),
  enabled: z.boolean(),
  project_id: z.string(),
  agent_id: z.string(),
  runtime_id: z.string(),
  model: z.string(),
  thinking_level: z.string(),
  instructions: z.string(),
  default_instructions: z.string(),
  can_manage: z.boolean(),
}).loose();

export type ReleaseAgentSettings = z.infer<typeof releaseAgentSettingsSchema>;
