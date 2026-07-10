import { z } from "zod";
import type { InboxItem, NotificationPreferenceResponse } from "../types";

export const InboxItemSchema = z.object({
  id: z.string(),
  workspace_id: z.string(),
  recipient_type: z.string(),
  recipient_id: z.string(),
  actor_type: z.string().nullable().optional().transform((value) => value ?? null),
  actor_id: z.string().nullable().optional().transform((value) => value ?? null),
  type: z.string(),
  severity: z.string().default("info"),
  issue_id: z.string().nullable().optional().transform((value) => value ?? null),
  title: z.string().default(""),
  body: z.string().nullable().optional().transform((value) => value ?? null),
  issue_status: z.string().nullable().optional().transform((value) => value ?? null),
  read: z.boolean().default(false),
  archived: z.boolean().default(false),
  created_at: z.string().default(""),
  details: z.record(z.string(), z.string()).nullable().optional().transform((value) => value ?? null),
}).loose();

export const InboxListSchema = z.array(InboxItemSchema);

export const InboxCountResponseSchema = z.object({
  count: z.number().default(0),
}).loose();

export const NotificationPreferenceResponseSchema = z.object({
  workspace_id: z.string(),
  preferences: z.record(z.string(), z.string()).default({}),
}).loose();

export const EMPTY_INBOX_ITEM: InboxItem = {
  id: "", workspace_id: "", recipient_type: "member", recipient_id: "",
  actor_type: null, actor_id: null, type: "issue_subscribed", severity: "info",
  issue_id: null, title: "", body: null, issue_status: null, read: false,
  archived: false, created_at: "", details: null,
};

export const EMPTY_NOTIFICATION_PREFERENCE_RESPONSE: NotificationPreferenceResponse = {
  workspace_id: "",
  preferences: {},
};
