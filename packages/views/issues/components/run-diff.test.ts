// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { TimelineEntry } from "@multica/core/types";
import { lastCommentIdByTaskId } from "./run-diff";

function comment(id: string, sourceTaskId: string | null, createdAt: string): TimelineEntry {
  return {
    type: "comment",
    id,
    actor_type: "agent",
    actor_id: "agent-1",
    created_at: createdAt,
    source_task_id: sourceTaskId,
  };
}

describe("lastCommentIdByTaskId", () => {
  it("keeps only the newest comment of each run", () => {
    const map = lastCommentIdByTaskId([
      comment("c1", "task-1", "2026-10-22T01:00:00Z"),
      comment("c2", "task-1", "2026-10-22T01:05:00Z"),
      comment("c3", "task-2", "2026-10-22T01:06:00Z"),
    ]);
    expect(map.get("task-1")).toBe("c2");
    expect(map.get("task-2")).toBe("c3");
  });

  it("ignores member comments and activities", () => {
    const map = lastCommentIdByTaskId([
      { type: "activity", id: "a1", actor_type: "member", actor_id: "u1", created_at: "2026-10-22T01:00:00Z", source_task_id: "task-1" },
      { type: "comment", id: "c1", actor_type: "member", actor_id: "u1", created_at: "2026-10-22T01:01:00Z" },
      comment("c2", "task-1", "2026-10-22T01:02:00Z"),
    ]);
    expect([...map.entries()]).toEqual([["task-1", "c2"]]);
  });

  it("returns an empty map when no comment came from a run", () => {
    expect(lastCommentIdByTaskId([]).size).toBe(0);
  });
});
