import { z } from "zod";
import type { NotificationPreferences } from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

export const InboxItemSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  recipient_type: z.string(),
  recipient_id: z.string(),
  actor_type: z.string().nullable().optional().transform((value) => value ?? null),
  actor_id: z.string().nullable().optional().transform((value) => value ?? null),
  type: z.string(),
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
  count: z.number(),
}).loose();

export const NotificationPreferenceResponseSchema = z.object({
  preferences: z.record(z.string(), z.string()).default({}),
}).loose();

export const EMPTY_NOTIFICATION_PREFERENCES: NotificationPreferences = {};
