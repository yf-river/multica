import { z } from "zod";
import type {
  ListLarkInstallationsResponse,
} from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

const LarkInstallationSchema = z.object({
  id: NonEmptyStringSchema,
  agent_id: NonEmptyStringSchema,
  app_id: NonEmptyStringSchema,
  status: NonEmptyStringSchema,
  region: z.string(),
  installed_at: z.string(),
});

export const LarkInstallationListResponseSchema = z.object({
  installations: z.array(LarkInstallationSchema).default([]),
  configured: z.boolean(),
  install_supported: z.boolean(),
});

export const BeginLarkInstallResponseSchema = z.object({
  session_id: NonEmptyStringSchema,
  qr_code_url: NonEmptyStringSchema,
  expires_in_seconds: z.number().positive(),
  poll_interval_seconds: z.number().positive(),
});

export const LarkInstallStatusResponseSchema = z.object({
  status: NonEmptyStringSchema,
  error_reason: z.string().optional(),
  error_message: z.string().optional(),
}).superRefine((response, context) => {
  if (response.status === "error" && !response.error_reason) {
    context.addIssue({
      code: "custom",
      path: ["error_reason"],
      message: "failed install status requires error_reason",
    });
  }
});

export const RedeemLarkBindingTokenResponseSchema = z.object({
  workspace_id: NonEmptyStringSchema,
  installation_id: NonEmptyStringSchema,
});

export const EMPTY_LARK_INSTALLATION_LIST_RESPONSE: ListLarkInstallationsResponse = {
  installations: [],
  configured: false,
  install_supported: false,
};
