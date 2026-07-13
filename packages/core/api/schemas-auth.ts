import { z } from "zod";
import type { User } from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

// Runtime response contracts for auth.
// ---------------------------------------------------------------------------
// User (`/api/me` GET + PATCH). The auth store and Settings → Account both
// trust this shape — a drift here would knock both surfaces out. Kept
// lenient by the same rules as IssueSchema: enums stay `z.string()`,
// nullable fields are unioned with `null`, unknown server fields pass
// through via `.loose()`.
// ---------------------------------------------------------------------------

export const UserSchema = z.object({
  id: NonEmptyStringSchema,
  name: z.string(),
  account: z.string(),
  avatar_url: z.string().nullable(),
  onboarded_at: z.string().nullable(),
  onboarding_questionnaire: z.record(z.string(), z.unknown()),
  profile_description: z.string(),
  timezone: z.string().nullable(),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

export const LoginResponseSchema = z.object({
  token: NonEmptyStringSchema,
  user: UserSchema,
}).loose();

export const CliTokenResponseSchema = z.object({
  token: NonEmptyStringSchema,
}).loose();

export const EMPTY_USER: User = {
  id: "",
  name: "",
  account: "",
  avatar_url: null,
  onboarded_at: null,
  onboarding_questionnaire: {},
  profile_description: "",
  timezone: null,
  created_at: "",
  updated_at: "",
};
