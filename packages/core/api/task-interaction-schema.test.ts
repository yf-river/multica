import { describe, expect, it } from "vitest";
import { TaskInteractionListSchema } from "./task-interaction-schema";

describe("task interaction wire schema", () => {
  it("normalizes the native question flags and task identity", () => {
    const parsed = TaskInteractionListSchema.parse({ interactions: [{
      id: "request", task_id: "task", provider: "codebuddy", status: "pending",
      created_at: "2026-10-26T00:00:00Z", expires_at: "2026-10-26T00:01:00Z",
      prompt: { kind: "question", title: "Choose", questions: [{ id: "0", question: "Which?", multi_select: true }] },
    }] });
    expect(parsed.available).toBe(true);
    expect(parsed.interactions[0]?.taskId).toBe("task");
    expect(parsed.interactions[0]?.prompt.questions[0]?.multiSelect).toBe(true);
    expect(parsed.interactions[0]?.prompt.questions[0]?.options).toEqual([]);
  });
  it("rejects a missing collection or malformed answer rather than interpreting it as no requests", () => {
    expect(TaskInteractionListSchema.safeParse({}).success).toBe(false);
    expect(TaskInteractionListSchema.safeParse({ interactions: [{}] }).success).toBe(false);
  });
});
