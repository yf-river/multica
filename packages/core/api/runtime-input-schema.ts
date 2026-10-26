import { z } from "zod";

export const RuntimeInputSchema = z.object({
  id: z.string().min(1), task_id: z.string().min(1), author_id: z.string(),
  kind: z.string(), content: z.string(), status: z.string(),
  output: z.string(), failure_reason: z.string(), created_at: z.string(),
}).transform(({ task_id, author_id, failure_reason, created_at, ...input }) => ({
  ...input, taskId: task_id, authorId: author_id, failureReason: failure_reason, createdAt: created_at,
}));

export const RuntimeInputPageSchema = z.object({
  inputs: z.array(RuntimeInputSchema), next_cursor: z.string(), available: z.boolean().default(true),
}).transform(({ next_cursor, ...page }) => ({ ...page, nextCursor: next_cursor }));

export const RuntimeInputCapabilitiesSchema = z.object({
  capabilities: z.array(z.string()), available: z.boolean().default(true),
});

export type RuntimeInput = z.output<typeof RuntimeInputSchema>;
export type RuntimeInputPage = z.output<typeof RuntimeInputPageSchema>;
export type RuntimeInputCapabilities = z.output<typeof RuntimeInputCapabilitiesSchema>;
export type CreateRuntimeInput = { id: string; kind: "text" | "command"; content: string };
