// @vitest-environment node
import { describe, expect, it } from "vitest";
import { summarizeTaskContext } from "./runtime-context";

describe("runtime context metrics", () => {
  it("keeps absent metrics unknown and reported zero distinct", () => {
    const summary = summarizeTaskContext([{ kind: "context", input_tokens: 0, output_tokens: 0 }]);
    expect(summary.input).toBe(0);
    expect(summary.output).toBe(0);
    expect(summary.read).toBeUndefined();
    expect(summary.peak).toBeUndefined();
    expect(summary.modelMs).toBeUndefined();
    expect(summary.system).toBeUndefined();
    expect(summary.rate).toBeUndefined();
  });

  it("uses only paired cache observations and the latest measured breakdown", () => {
    const summary = summarizeTaskContext([
      { kind: "context", input_tokens: 20, cache_read_tokens: 80, pressure_tokens: 100, breakdown: { system_tokens: 20 } },
      { kind: "context", input_tokens: 1000, pressure_tokens: 200, breakdown: { system_tokens: 0 } },
      { kind: "context", duration_ms: Number.NaN, context_window: Number.POSITIVE_INFINITY },
      { kind: "compaction", phase: "end" },
    ]);
    expect(summary.rate).toBe(0.8);
    expect(summary.input).toBe(1020);
    expect(summary.system).toBe(0);
    expect(summary.peak).toBe(200);
    expect(summary.window).toBeUndefined();
    expect(summary.modelMs).toBeUndefined();
    expect(summary.compactions).toHaveLength(1);
  });
});
