import { describe, expect, it } from "vitest";
import { TaskInteractionListSchema } from "./task-interaction-schema";

describe("task interaction wire schema", () => {
  it("normalizes the native question flags and task identity", () => {
    const parsed = TaskInteractionListSchema.parse({
      interactions: [
        {
          id: "request",
          task_id: "task",
          provider: "codebuddy",
          status: "pending",
          created_at: "2026-10-26T00:00:00Z",
          expires_at: "2026-10-26T00:01:00Z",
          prompt: {
            kind: "question",
            title: "Choose",
            questions: [{ id: "0", question: "Which?", multi_select: true }],
          },
        },
      ],
    });
    expect(parsed.available).toBe(true);
    expect(parsed.interactions[0]?.taskId).toBe("task");
    expect(parsed.interactions[0]?.prompt.questions[0]?.multiSelect).toBe(true);
    expect(parsed.interactions[0]?.prompt.questions[0]?.options).toEqual([]);
  });
  it("rejects a missing collection or malformed answer rather than interpreting it as no requests", () => {
    expect(TaskInteractionListSchema.safeParse({}).success).toBe(false);
    expect(
      TaskInteractionListSchema.safeParse({ interactions: [{}] }).success,
    ).toBe(false);
  });
});

it("preserves the suspended plan and its continuation identity", () => {
  const planMarkdown = "# Plan\n\n1. Read\n2. Write after approval.";
  const result = TaskInteractionListSchema.parse({
    interactions: [
      {
        id: "plan",
        task_id: "task",
        provider: "dsh",
        status: "responded",
        delivery_mode: "suspended",
        followup_task_id: "continuation",
        created_at: "now",
        expires_at: "tomorrow",
        prompt: { kind: "plan", title: "Plan", plan_markdown: planMarkdown },
        response: { decision: "keep_planning", feedback: "Add rollback." },
      },
    ],
  });
  expect(result.interactions[0]).toMatchObject({
    deliveryMode: "suspended",
    followupTaskId: "continuation",
    prompt: { planMarkdown },
    response: { decision: "keep_planning", feedback: "Add rollback." },
  });
  const malformed = {
    id: "plan",
    task_id: "task",
    provider: "dsh",
    status: "pending",
    delivery_mode: "suspended",
    created_at: "now",
    expires_at: "tomorrow",
    prompt: { kind: "plan", title: "Plan", plan_markdown: planMarkdown },
  };
  expect(
    TaskInteractionListSchema.safeParse({
      interactions: [
        { ...malformed, response: { decision: "unexpected-auto-approval" } },
      ],
    }).success,
  ).toBe(false);
  expect(
    TaskInteractionListSchema.safeParse({
      interactions: [{ ...malformed, prompt: { kind: "plan", title: "Plan" } }],
    }).success,
  ).toBe(false);
  expect(
    TaskInteractionListSchema.safeParse({
      interactions: [{ ...malformed, delivery_mode: "live" }],
    }).success,
  ).toBe(false);
});
