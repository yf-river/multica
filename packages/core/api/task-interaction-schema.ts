import { z } from "zod";

const QuestionSchema = z.object({
  id: z.string(),
  header: z.string().optional(),
  question: z.string(),
  options: z.array(z.object({ label: z.string(), description: z.string().optional() })).default([]),
  multi_select: z.boolean().default(false),
}).transform(({ multi_select, ...question }) => ({ ...question, multiSelect: multi_select }));

const AnswerSchema = z.object({
  decision: z.enum(["allow", "deny"]),
  answers: z.record(z.string(), z.array(z.string())).optional(),
});

export const TaskInteractionSchema = z.object({
  id: z.string(), task_id: z.string(), provider: z.string(), status: z.string(),
  prompt: z.object({ kind: z.string(), title: z.string(), questions: z.array(QuestionSchema).default([]) }),
  response: AnswerSchema.optional(),
  created_at: z.string(), expires_at: z.string(),
}).transform(({ task_id, created_at, expires_at, ...interaction }) => ({
  ...interaction, taskId: task_id, createdAt: created_at, expiresAt: expires_at,
}));

export const TaskInteractionListSchema = z.object({
  interactions: z.array(TaskInteractionSchema),
  available: z.boolean().default(true),
});
