import { z } from "zod";

// Runtime response contracts for app config.
export interface AppConfigResponse {
  cdn_domain: string;
  // True when the CDN domain serves private content via time-bounded signed
  // URLs (CloudFront signing) — raw storage URLs on that domain are NOT
  // publicly fetchable and must not be used as native media sources
  // (MUL-3254). Older servers omit the field; treat that as false.
  cdn_signed?: boolean;
  allow_signup: boolean;
  posthog_key?: string;
  posthog_host?: string;
  analytics_environment?: string;
  daemon_server_url?: string;
  daemon_app_url?: string;
  workspace_creation_disabled?: boolean;
}

const OptionalStringSchema = z.preprocess(
  (value) => (typeof value === "string" ? value : undefined),
  z.string().optional(),
);

const BooleanWithDefaultSchema = (fallback: boolean) =>
  z.preprocess(
    (value) => (typeof value === "boolean" ? value : undefined),
    z.boolean().default(fallback),
  );

export const AppConfigSchema = z.object({
  cdn_domain: z.string().default(""),
  cdn_signed: BooleanWithDefaultSchema(false),
  allow_signup: BooleanWithDefaultSchema(true),
  posthog_key: OptionalStringSchema,
  posthog_host: OptionalStringSchema,
  analytics_environment: OptionalStringSchema,
  daemon_server_url: OptionalStringSchema,
  daemon_app_url: OptionalStringSchema,
  workspace_creation_disabled: BooleanWithDefaultSchema(false).optional(),
}).loose();

export const EMPTY_APP_CONFIG: AppConfigResponse = {
  cdn_domain: "",
  cdn_signed: false,
  allow_signup: true,
  daemon_server_url: "",
  daemon_app_url: "",
  workspace_creation_disabled: false,
};
