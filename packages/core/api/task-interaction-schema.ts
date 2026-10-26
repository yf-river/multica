import { z } from "zod";

const QuestionSchema = z
  .object({
    id: z.string(),
    header: z.string().optional(),
    detail: z.string().optional(),
    question: z.string(),
    options: z
      .array(
        z.object({ label: z.string(), description: z.string().optional() }),
      )
      .default([]),
    multi_select: z.boolean().default(false),
  })
  .transform(({ multi_select, ...question }) => ({
    ...question,
    multiSelect: multi_select,
  }));

const AnswerSchema = z.object({
  decision: z.enum(["allow", "deny", "approved", "keep_planning", "cancelled"]),
  feedback: z.string().optional(),
  answers: z.record(z.string(), z.array(z.string())).optional(),
});

export const TaskInteractionSchema = z
  .object({
    id: z.string(),
    task_id: z.string(),
    provider: z.string(),
    status: z.string(),
    delivery_mode: z.string().default("live"),
    followup_task_id: z.string().optional(),
    prompt: z
      .object({
        kind: z.string(),
        title: z.string(),
        plan_markdown: z.string().optional(),
        questions: z.array(QuestionSchema).default([]),
      })
      .transform(({ plan_markdown, ...prompt }) => ({
        ...prompt,
        planMarkdown: plan_markdown,
      })),
    response: AnswerSchema.optional(),
    created_at: z.string(),
    expires_at: z.string(),
  })
  .superRefine((interaction, ctx) => {
    if (
      interaction.provider === "dsh" &&
      (interaction.delivery_mode !== "suspended" ||
        (interaction.prompt.kind === "plan" &&
          !interaction.prompt.planMarkdown?.trim()))
    ) {
      ctx.addIssue({
        code: "custom",
        message: "DSH interaction has incomplete continuation metadata",
      });
    }
  })
  .transform(
    ({
      task_id,
      created_at,
      expires_at,
      delivery_mode,
      followup_task_id,
      ...interaction
    }) => ({
      ...interaction,
      deliveryMode: delivery_mode,
      followupTaskId: followup_task_id,
      taskId: task_id,
      createdAt: created_at,
      expiresAt: expires_at,
    }),
  );

export const TaskInteractionListSchema = z.object({
  interactions: z.array(TaskInteractionSchema),
  available: z.boolean().default(true),
});
