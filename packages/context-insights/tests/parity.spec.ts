import { describe, it, expect } from "vitest";
import { readFileSync, existsSync } from "node:fs";
import { createContextTimelineDefinition as originalTimeline } from "../../../vendor/dsh-context/src/host/timeline";
import { createContextActivityDefinition as originalActivity } from "../../../vendor/dsh-context/src/host/activity";
import { createContextHeadersDefinition as originalHeaders } from "../../../vendor/dsh-context/src/host/headers";
import {
  estimateSessionCost as originalCost,
  priceIndexOf as originalIndex,
} from "../../../vendor/dsh-context/src/client/cost";
import { createContextTimelineDefinition } from "../src/host/timeline";
import { createContextActivityDefinition } from "../src/host/activity";
import { createContextHeadersDefinition } from "../src/host/headers";
import { estimateSessionCost, priceIndexOf } from "../src/client/cost";
import {
  header,
  systemMessage,
  userMessage,
  stepStart,
  assistantMessage,
  toolCall,
  toolResult,
  stepEnd,
  compaction,
  planMode,
} from "./host/helpers/events";

const events = [
  header(0, {
    provider: "deepseek",
    model: "deepseek-v4-flash",
    tools: [{ name: "bash", description: "run a command" }],
  }),
  systemMessage(1),
  userMessage(2, [{ type: "text", text: "你好，比较完整上下文。" }], {
    kind: "user",
  }),
  stepStart(3),
  assistantMessage(4, {
    usage: { inputTokens: 100, cacheReadTokens: 60, outputTokens: 7 },
  }),
  toolCall(5, {
    callId: "bash-1",
    name: "bash",
    arguments: '{"command":"printf hello"}',
  }),
  toolResult(6, {
    callId: "bash-1",
    content: [{ type: "text", text: "hello\u0000原件" }],
  }),
  stepEnd(7),
  planMode(8, { enabled: true }),
  compaction(9, "prune", { seqs: [2] }),
  header(10, { provider: "other", model: "other-model" }),
  userMessage(11, [{ type: "text", text: "继续。" }], { kind: "user" }),
  assistantMessage(12, { usage: { inputTokens: 20, outputTokens: 4 } }),
];
function compare(stream: readonly unknown[]) {
  const pairs = [
    [
      originalTimeline({}, () => false),
      createContextTimelineDefinition({}, () => false),
    ],
    [originalActivity(), createContextActivityDefinition()],
    [originalHeaders(), createContextHeadersDefinition()],
  ] as const;
  for (const [original, ported] of pairs) {
    let left: unknown = original.init(),
      right: unknown = ported.init();
    for (const event of stream) {
      left = original.apply(left as never, event as never);
      right = ported.apply(right as never, event as never);
      expect(ported.wire.view(right as never)).toEqual(
        original.wire.view(left as never),
      );
    }
  }
}
describe("fixed source 402742a projection and pricing parity", () => {
  it("matches every intermediate projection, including tools, pruning and model changes", () =>
    compare(events));
  it("matches peak/off-peak, unknown provider and both currencies", () => {
    const prices = {
      deepseek: {
        "deepseek-v4-flash": { miss: 1, hit: 0.1, write: 1, out: 2 },
      },
    };
    const usage = {
      deepseek: {
        "deepseek-v4-flash": {
          peak: { uncached: 100, cacheRead: 60, cacheWrite: 2, output: 7 },
          off: { uncached: 10, cacheRead: 5, cacheWrite: 0, output: 3 },
        },
      },
      unknown: {
        missing: {
          peak: { uncached: 10, cacheRead: 0, cacheWrite: 0, output: 1 },
        },
      },
    };
    for (const currency of ["cny", "usd"] as const)
      expect(
        estimateSessionCost(
          usage,
          { prices, index: priceIndexOf(prices) },
          currency,
        ),
      ).toEqual(
        originalCost(usage, { prices, index: originalIndex(prices) }, currency),
      );
  });
  const path = process.env.MULTICA_INSIGHTS_PARITY_JOURNAL;
  it.skipIf(!path || !existsSync(path))(
    "matches the observed int task event streams",
    () => {
      const streams = new Map<string, Map<number, unknown>>();
      for (const line of readFileSync(path!, "utf8").trim().split("\n")) {
        const frame = JSON.parse(line);
        if (frame.type !== "archive" && frame.type !== "insight_context")
          continue;
        let stream = streams.get(frame.session_id);
        if (!stream) {
          stream = new Map();
          streams.set(frame.session_id, stream);
        }
        stream.set(frame.event.seq, frame.event);
      }
      expect(streams.size).toBeGreaterThan(0);
      for (const stream of streams.values())
        compare(
          [...stream.entries()]
            .sort((a, b) => a[0] - b[0])
            .map(([, event]) => event),
        );
    },
  );
});
