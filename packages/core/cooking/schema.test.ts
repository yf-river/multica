// @vitest-environment node
import { describe, it, expect } from "vitest";
import { parseWithFallback } from "../api/schema";
import { CookingDetailSchema, CookingListSchema } from "./schema";
describe("cooking response boundary", () => {
  it("rejects malformed lists instead of showing a false empty success", () => {
    expect(
      parseWithFallback(
        { items: [{ id: 9 }], total: "many" },
        CookingListSchema,
        null,
        { endpoint: "cooking" },
      ),
    ).toBeNull();
  });
  it("retains text and calculations while defaulting additive display fields", () => {
    const parsed = CookingDetailSchema.parse({
      entry: {
        id: "a",
        name: "鸡蛋",
        kind: "recipe",
        category: "breakfast",
        markdown: "鸡蛋 = 人数 * 2",
      },
      related: [],
      version: "v",
    });
    expect(parsed.entry.markdown).toBe("鸡蛋 = 人数 * 2");
    expect(parsed.entry.illustrations).toEqual([]);
    expect(parsed.entry.favorite).toBe(false);
  });
});
