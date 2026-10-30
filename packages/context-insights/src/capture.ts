import { sessionStatsProjectionDefinition as officialStats } from "./official/session-stats.js";
import { z } from "zod";
import type { SessionEvent } from "@deepseek-ai/dsh-session";
import { createContextTimelineDefinition } from "./host/timeline.js";
import { createContextActivityDefinition } from "./host/activity.js";
import { createContextHeadersDefinition } from "./host/headers.js";

// 历史事件仅用于重建当前窗口；启用前的请求和累计消耗不进入洞察。
export function captureDefinitions(
  since: number,
): readonly [
  ReturnType<typeof createContextTimelineDefinition>,
  ReturnType<typeof createContextActivityDefinition>,
  ReturnType<typeof createContextHeadersDefinition>,
  typeof officialStats,
] {
  const timeline = createContextTimelineDefinition({}, () => false);
  const activity = createContextActivityDefinition();
  const headers = createContextHeadersDefinition();
  return [
    {
      ...timeline,
      stateSchema: ownedSchema(timeline.stateSchema),
      init: (_header?: unknown, ownedFrom = 0) => ({
        ...timeline.init(),
        multicaOwnedFrom: ownedFrom,
      }),
      stateVersion: timeline.stateVersion + since,
      apply(state: ReturnType<typeof timeline.init>, event: SessionEvent) {
        const next = timeline.apply(state, event);
        if (
          event.time >= since &&
          event.seq >=
            ((state as typeof state & { multicaOwnedFrom?: number })
              .multicaOwnedFrom ?? 0)
        )
          return next;
        const clean = {
          ...next,
          humanInputs: 0,
          answers: 0,
          requests: [],
          events: [],
          commands: [],
          fileOps: [],
          spans: [],
        };
        delete clean.cost;
        delete clean.timing;
        delete clean.generatedReasoning;
        delete clean.generatedAnswerTokens;
        delete clean.generatedCallTokens;
        delete clean.commandStats;
        return clean;
      },
    },
    {
      ...activity,
      stateSchema: ownedSchema(activity.stateSchema),
      init: (_header?: unknown, ownedFrom = 0) => ({
        ...activity.init(),
        multicaOwnedFrom: ownedFrom,
      }),
      stateVersion: activity.stateVersion + since,
      apply(state: ReturnType<typeof activity.init>, event: SessionEvent) {
        if (
          event.time >= since &&
          event.seq >=
            ((state as typeof state & { multicaOwnedFrom?: number })
              .multicaOwnedFrom ?? 0)
        )
          return activity.apply(state, event);
        const next = activity.apply(state, event);
        const clean = { ...next, days: {} };
        delete clean.toolStarts;
        delete clean.stepStart;
        delete clean.lastTurn;
        delete clean.skillCalls;
        return clean;
      },
    },
    { ...headers, stateVersion: headers.stateVersion + since },
    {
      ...officialStats,
      key: "multicaSessionStats",
      stateSchema: ownedSchema(officialStats.stateSchema),
      stateVersion: officialStats.stateVersion + since,
      init: (_header?: unknown, ownedFrom = 0) => ({
        ...officialStats.init(),
        multicaOwnedFrom: ownedFrom,
      }),
      apply(state, event) {
        if (
          event.time < since ||
          event.seq < ((state.multicaOwnedFrom as number | undefined) ?? 0)
        )
          return state;
        return officialStats.apply(state, event);
      },
    },
  ] as const;
}

function ownedSchema<S>(schema: z.ZodType<S>): z.ZodType<S> {
  return z
    .preprocess((value) => {
      if (value !== null && typeof value === "object") {
        const clean = { ...value } as Record<string, unknown>;
        delete clean.multicaOwnedFrom;
        return clean;
      }
      return value;
    }, schema)
    .and(z.object({ multicaOwnedFrom: z.number().optional() })) as z.ZodType<S>;
}
