import { z } from "zod";
import type {
  ChatMessagesPage,
  ChatPendingTask,
  ChatSession,
  PendingChatTasksResponse,
  SendChatMessageResponse,
} from "../types";
import { EmbeddedAttachmentSchema } from "./schemas-internal";

export const ChatSessionSchema = z.object({
  id: z.string(), workspace_id: z.string(), agent_id: z.string(), creator_id: z.string(),
  title: z.string().default(""), status: z.string().default("active"),
  has_unread: z.boolean().default(false), created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();
export const ChatSessionListSchema = z.array(ChatSessionSchema);

export const ChatMessageSchema = z.object({
  id: z.string(), chat_session_id: z.string(), role: z.string(), content: z.string().default(""),
  task_id: z.string().nullable().optional().transform((value) => value ?? null),
  created_at: z.string().default(""), attachments: z.array(EmbeddedAttachmentSchema).optional(),
  failure_reason: z.string().nullable().optional(), elapsed_ms: z.number().nullable().optional(),
}).loose();
const ChatMessagesCursorSchema = z.object({ created_at: z.string(), id: z.string() }).loose();
export const ChatMessagesPageSchema = z.object({
  messages: z.array(ChatMessageSchema).default([]), limit: z.number().default(50),
  has_more: z.boolean().default(false), next_cursor: ChatMessagesCursorSchema.nullable().optional(),
}).loose();

export const SendChatMessageResponseSchema = z.object({
  message_id: z.string(), task_id: z.string(), created_at: z.string(),
  attachment_ids: z.array(z.string()).optional(),
}).loose();

export const ChatPendingTaskSchema = z.object({
  task_id: z.string().optional(), status: z.string().optional(), created_at: z.string().optional(),
}).loose();
const PendingChatTaskItemSchema = z.object({
  task_id: z.string(), status: z.string(), chat_session_id: z.string(),
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
