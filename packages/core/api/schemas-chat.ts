import { z } from "zod";
import type {
  ChatMessagesPage,
  ChatPendingTask,
  ChatSession,
  PendingChatTasksResponse,
  SendChatMessageResponse,
} from "../types";
import { EmbeddedAttachmentSchema, NonEmptyStringSchema } from "./schemas-internal";

export const ChatSessionSchema = z.object({
  id: NonEmptyStringSchema, workspace_id: NonEmptyStringSchema,
  agent_id: NonEmptyStringSchema, creator_id: NonEmptyStringSchema,
  title: z.string().default(""), status: z.string().default("active"),
  has_unread: z.boolean().default(false), created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();
export const ChatSessionListSchema = z.array(ChatSessionSchema);

export const ChatMessageSchema = z.object({
  id: NonEmptyStringSchema, chat_session_id: NonEmptyStringSchema,
  role: z.string(), content: z.string().default(""),
  task_id: z.string().nullable().optional().transform((value) => value ?? null),
  created_at: z.string().default(""), attachments: z.array(EmbeddedAttachmentSchema).optional(),
  failure_reason: z.string().nullable().optional(), elapsed_ms: z.number().nullable().optional(),
}).loose();
const ChatMessagesCursorSchema = z.object({
  created_at: NonEmptyStringSchema,
  id: NonEmptyStringSchema,
}).loose();
export const ChatMessagesPageSchema = z.object({
  messages: z.array(ChatMessageSchema).default([]), limit: z.number().default(50),
  has_more: z.boolean().default(false), next_cursor: ChatMessagesCursorSchema.nullable().optional(),
}).loose();

export const SendChatMessageResponseSchema = z.object({
  message_id: NonEmptyStringSchema,
  task_id: NonEmptyStringSchema,
  created_at: NonEmptyStringSchema,
  attachment_ids: z.array(NonEmptyStringSchema).optional(),
}).loose();

export const ChatPendingTaskSchema = z.object({
  task_id: z.string().optional(), status: z.string().optional(), created_at: z.string().optional(),
}).loose();
const PendingChatTaskItemSchema = z.object({
  task_id: NonEmptyStringSchema,
  status: z.string(),
  chat_session_id: NonEmptyStringSchema,
}).loose();
export const PendingChatTasksResponseSchema = z.object({
  tasks: z.array(PendingChatTaskItemSchema).default([]),
}).loose();

export const EMPTY_CHAT_SESSION: ChatSession = {
  id: "", workspace_id: "", agent_id: "", creator_id: "", title: "", status: "active",
  has_unread: false, created_at: "", updated_at: "",
};
export const EMPTY_CHAT_MESSAGES_PAGE: ChatMessagesPage = {
  messages: [], limit: 50, has_more: false, next_cursor: null,
};
export const EMPTY_SEND_CHAT_MESSAGE_RESPONSE: SendChatMessageResponse = {
  message_id: "", task_id: "", created_at: "",
};
export const EMPTY_CHAT_PENDING_TASK: ChatPendingTask = {};
export const EMPTY_PENDING_CHAT_TASKS_RESPONSE: PendingChatTasksResponse = { tasks: [] };
