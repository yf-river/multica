import { describe, expect, it } from "vitest";
import { TaskMessagePayloadSchema, TaskOutputPageSchema } from "./schemas";

describe("task output wire data", () => {
  it("accepts older messages and retains the full-output reference", () => {
    const old = { task_id: "task", seq: 1, type: "tool_result", output: "ok" };
    expect(TaskMessagePayloadSchema.parse(old).issue_id).toBe("");
    expect(TaskMessagePayloadSchema.parse({ ...old, id: "message", output_chars: 120000, output_truncated: true })).toMatchObject({ id: "message", output_chars: 120000, output_truncated: true });
  });
  it("maps pages without accepting missing output as an empty success", () => {
    expect(TaskOutputPageSchema.parse({ output: "tail evidence", offset: 100000, next_offset: 100013, total_chars: 100013, has_more: false, found: true })).toEqual({ output: "tail evidence", offset: 100000, nextOffset: 100013, totalChars: 100013, hasMore: false, found: true });
    expect(TaskOutputPageSchema.safeParse({ offset: 0 }).success).toBe(false);
  });
});
