// @vitest-environment node
import { describe, it, expect, vi } from "vitest";
import { applyAgentRuntimeBatch } from "./batch-runtime";

const settings = { runtime_id: "target", model: "model", thinking_level: "high", service_tier: "priority" };
describe("applyAgentRuntimeBatch", () => {
  it("limits concurrency to four, deduplicates and keeps result order despite failures", async () => {
    let active = 0;
    let peak = 0;
    const progress = vi.fn();
    const update = vi.fn(async (id: string) => {
      peak = Math.max(peak, ++active);
      await new Promise((resolve) => setTimeout(resolve, id === "a" ? 15 : 1));
      active--;
      if (id === "b") throw new Error("No access");
    });
    const results = await applyAgentRuntimeBatch(["a", "b", "c", "d", "e", "a"], settings, update, progress);
    expect(peak).toBe(4);
    expect(update).toHaveBeenCalledTimes(5);
    expect(update).toHaveBeenCalledWith("a", settings);
    expect(results).toEqual([{ id: "a" }, { id: "b", error: "No access" }, { id: "c" }, { id: "d" }, { id: "e" }]);
    expect(progress.mock.calls.map(([count]) => count)).toEqual([1, 2, 3, 4, 5]);
    update.mockClear();
    await applyAgentRuntimeBatch(results.filter((result) => result.error !== undefined).map((result) => result.id), settings, update);
    expect(update).toHaveBeenCalledTimes(1);
    expect(update).toHaveBeenCalledWith("b", settings);
  });
});
