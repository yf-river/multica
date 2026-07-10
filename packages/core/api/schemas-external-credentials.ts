import { z } from "zod";
import type {
  ExternalCredentialProfile,
  ListExternalCredentialProfilesResponse,
  TestExternalCredentialProfileResponse,
} from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

const ExternalCredentialSecretBindingSchema = z.object({
  configured: z.boolean(),
  redacted: z.boolean(),
  mode: NonEmptyStringSchema,
  hint: z.string().optional(),
});

export const ExternalCredentialProfileSchema = z.object({
  id: NonEmptyStringSchema,
  user_id: NonEmptyStringSchema,
  scope: NonEmptyStringSchema,
  provider: NonEmptyStringSchema,
  name: NonEmptyStringSchema,
  secret_binding: ExternalCredentialSecretBindingSchema,
  capabilities: z.record(z.string(), z.unknown()).default({}),
  status: NonEmptyStringSchema,
  last_verified_at: z.string().nullable(),
  last_error: z.string().optional(),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
});

export const ExternalCredentialProfileListResponseSchema = z.object({
  profiles: z.array(ExternalCredentialProfileSchema).default([]),
});

export const TestExternalCredentialProfileResponseSchema = z.object({
  provider: NonEmptyStringSchema,
  secret_binding: ExternalCredentialSecretBindingSchema,
  status: NonEmptyStringSchema,
  last_verified_at: z.string().nullable(),
  last_error: z.string().optional(),
});

export const EMPTY_EXTERNAL_CREDENTIAL_PROFILE: ExternalCredentialProfile = {
  id: "",
  user_id: "",
  scope: "account",
  provider: "",
  name: "",
  secret_binding: { configured: false, redacted: true, mode: "missing" },
  capabilities: {},
  status: "unverified",
  last_verified_at: null,
  created_at: "",
  updated_at: "",
};

export const EMPTY_EXTERNAL_CREDENTIAL_PROFILE_LIST_RESPONSE: ListExternalCredentialProfilesResponse = {
  profiles: [],
};

export const EMPTY_TEST_EXTERNAL_CREDENTIAL_PROFILE_RESPONSE: TestExternalCredentialProfileResponse = {
  provider: "",
  secret_binding: { configured: false, redacted: true, mode: "missing" },
  status: "unverified",
  last_verified_at: null,
};
