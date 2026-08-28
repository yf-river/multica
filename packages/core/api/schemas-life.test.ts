import { describe, expect, it } from "vitest";
import { parseOrThrow, parseWithFallback } from "./schema";
import { EMPTY_LIFE_MEMORIES, LifeMemoryListSchema, LifeMemorySchema } from "./schemas-life";

describe("life API response compatibility", () => {
  it("degrades a malformed memory list to an empty read model", () => {
    const parsed = parseWithFallback(
      { memories: null },
      LifeMemoryListSchema,
      EMPTY_LIFE_MEMORIES,
      { endpoint: "GET /api/life/memories" },
    );
    expect(parsed).toEqual({ memories: [] });
  });

  it("rejects a malformed successful memory mutation response", () => {
    expect(() => parseOrThrow(
      { status: "confirmed", content: "missing identity" },
      LifeMemorySchema,
      { endpoint: "POST /api/life/memories/:id/confirm", mayHaveCommitted: true },
    )).toThrowError(/response format|服务器返回格式异常/);
  });
});
