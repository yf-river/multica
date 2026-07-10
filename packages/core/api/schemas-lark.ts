import { z } from "zod";
import type {
  BeginLarkInstallResponse,
  LarkInstallStatusResponse,
  ListLarkInstallationsResponse,
  RedeemLarkBindingTokenResponse,
} from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

const LarkInstallationSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  agent_id: NonEmptyStringSchema,
  app_id: NonEmptyStringSchema,
  tenant_key: z.string().nullable().optional(),
  bot_open_id: NonEmptyStringSchema,
  installer_user_id: NonEmptyStringSchema,
  status: NonEmptyStringSchema,
  region: z.string().default("feishu"),
  installed_at: z.string().default(""),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
});

export const LarkInstallationListResponseSchema = z.object({
  installations: z.array(LarkInstallationSchema).default([]),
  configured: z.boolean(),
  install_supported: z.boolean().default(false),
});

export const BeginLarkInstallResponseSchema = z.object({
  session_id: NonEmptyStringSchema,
  qr_code_url: NonEmptyStringSchema,
  expires_in_seconds: z.number().positive(),
  poll_interval_seconds: z.number().positive(),
});

export const LarkInstallStatusResponseSchema = z.object({
  status: NonEmptyStringSchema,
  installation_id: z.string().optional(),
  error_reason: z.string().optional(),
  error_message: z.string().optional(),
}).superRefine((response, context) => {
  if (response.status === "success" && !response.installation_id) {
    context.addIssue({
      code: "custom",
      path: ["installation_id"],
      message: "successful install status requires installation_id",
    });
  }
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
  lark_open_id: NonEmptyStringSchema,
});

export const EMPTY_LARK_INSTALLATION_LIST_RESPONSE: ListLarkInstallationsResponse = {
  installations: [],
  configured: false,
  install_supported: false,
};

export const EMPTY_BEGIN_LARK_INSTALL_RESPONSE: BeginLarkInstallResponse = {
  session_id: "",
  qr_code_url: "",
  expires_in_seconds: 0,
  poll_interval_seconds: 0,
};

export const EMPTY_LARK_INSTALL_STATUS_RESPONSE: LarkInstallStatusResponse = {
  status: "error",
  error_reason: "invalid_response",
};

export const EMPTY_REDEEM_LARK_BINDING_TOKEN_RESPONSE: RedeemLarkBindingTokenResponse = {
  workspace_id: "",
  installation_id: "",
  lark_open_id: "",
};
