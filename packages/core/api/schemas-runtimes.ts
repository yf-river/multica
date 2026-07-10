import { z } from "zod";
import type { RuntimeProfile } from "../types";

export const RuntimeProfileSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  display_name: z.string(),
  // Keep protocol and visibility forward-compatible with newer daemons.
  protocol_family: z.string().default("claude"),
  command_name: z.string(),
  description: z.string().nullable().optional().transform((value) => value ?? null),
  fixed_args: z.array(z.string()).default([]),
  visibility: z.string().default("workspace"),
  created_by: z.string().nullable().optional().transform((value) => value ?? null),
  enabled: z.boolean().default(true),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

export const RuntimeProfileListResponseSchema = z.object({
  runtime_profiles: z.array(RuntimeProfileSchema).default([]),
}).loose();

export const EMPTY_RUNTIME_PROFILE: RuntimeProfile = {
  id: "",
  workspace_id: "",
  display_name: "",
  protocol_family: "claude",
  command_name: "",
  description: null,
  fixed_args: [],
  visibility: "workspace",
  created_by: null,
  enabled: false,
  created_at: "",
  updated_at: "",
};

export const EMPTY_RUNTIME_PROFILE_LIST_RESPONSE: { runtime_profiles: RuntimeProfile[] } = {
  runtime_profiles: [],
};
