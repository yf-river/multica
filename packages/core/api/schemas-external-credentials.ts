import { z } from "zod";
import { NonEmptyStringSchema } from "./schemas-internal";

const ExternalCredentialSecretBindingSchema = z.object({
  configured: z.boolean(),
  redacted: z.literal(true),
  mode: NonEmptyStringSchema,
  hint: z.string().optional(),
}).transform(({ configured, mode, hint }) => ({
  configured,
  mode,
  ...(hint === undefined ? {} : { hint }),
}));

export const ExternalCredentialProfileSchema = z.object({
  id: NonEmptyStringSchema,
  provider: NonEmptyStringSchema,
  name: NonEmptyStringSchema,
  secret_binding: ExternalCredentialSecretBindingSchema,
  status: NonEmptyStringSchema,
  last_error: z.string().optional(),
});

export const ExternalCredentialProfileListResponseSchema = z.object({
  profiles: z.array(ExternalCredentialProfileSchema).default([]),
}).transform(({ profiles }) => profiles);

export const TestExternalCredentialProfileResponseSchema = z.object({
  provider: NonEmptyStringSchema,
  secret_binding: ExternalCredentialSecretBindingSchema,
  status: NonEmptyStringSchema,
  last_error: z.string().optional(),
}).transform(({ status, last_error }) => ({
  status,
  ...(last_error === undefined ? {} : { last_error }),
}));
