import { z } from "zod";

// Runtime response contracts for app config.
export interface AppConfigResponse {
  cdn_domain: string;
  // True when the CDN domain serves private content via time-bounded signed
  // URLs (CloudFront signing) — raw storage URLs on that domain are NOT
  // publicly fetchable and must not be used as native media sources.
  cdn_signed: boolean;
  allow_signup: boolean;
  posthog_key: string;
  posthog_host: string;
  analytics_environment: string;
  daemon_server_url?: string;
  daemon_app_url?: string;
  workspace_creation_disabled: boolean;
}

const OptionalStringSchema = z.preprocess(
  (value) => (typeof value === "string" ? value : undefined),
  z.string().optional(),
);

export const AppConfigSchema = z.object({
  cdn_domain: z.string(),
  cdn_signed: z.boolean(),
  allow_signup: z.boolean(),
  posthog_key: z.string(),
  posthog_host: z.string(),
  analytics_environment: z.string(),
  daemon_server_url: OptionalStringSchema,
  daemon_app_url: OptionalStringSchema,
  workspace_creation_disabled: z.boolean(),
}).loose();

export const EMPTY_APP_CONFIG: AppConfigResponse = {
  cdn_domain: "",
  cdn_signed: false,
  allow_signup: true,
  posthog_key: "",
  posthog_host: "",
  analytics_environment: "",
  daemon_server_url: "",
  daemon_app_url: "",
  workspace_creation_disabled: false,
};
