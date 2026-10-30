import { describe, it, expect } from "vitest";
import { captureDefinitions } from "../src/capture";
import { header, assistantMessage, userMessage } from "./host/helpers/events";

describe("activation and inherited history", () => {
  it("retains old context without charging old requests", () => {
    const [def] = captureDefinitions(100);
    let state = def.init();
    for (const event of [
      header(0, { provider: "p", model: "m", time: 1 }),
      userMessage(1, [{ type: "text", text: "old question" }], null, {
        time: 2,
      }),
      assistantMessage(2, {
        time: 3,
        usage: { inputTokens: 90, outputTokens: 10 },
      }),
    ])
      state = def.apply(state, event as never);
    expect(def.wire.view(state).cost).toBeUndefined();
    expect(def.wire.view(state).requests).toEqual([]);
    expect(def.wire.view(state).current.total).toBeGreaterThan(0);
    state = def.apply(
      state,
      assistantMessage(3, {
        time: 101,
        usage: { inputTokens: 5, outputTokens: 2 },
      }) as never,
    );
    expect(def.wire.view(state).cost).toEqual({
      p: {
        m: { peak: { uncached: 5, cacheRead: 0, cacheWrite: 0, output: 2 } },
      },
    });
    expect(
      def.stateSchema.safeParse(JSON.parse(JSON.stringify(state))).success,
    ).toBe(true);
  });
  it("does not charge parent events inherited by a child session", () => {
    const [def, activity] = captureDefinitions(1);
    const init = def.init as (
      header: unknown,
      from: number,
    ) => ReturnType<typeof def.init>;
    let state = init({}, 3);
    for (const event of [
      header(0, { provider: "p", model: "m", time: 2 }),
      assistantMessage(1, {
        time: 3,
        usage: { inputTokens: 20, outputTokens: 4 },
      }),
      assistantMessage(3, {
        time: 4,
        usage: { inputTokens: 2, outputTokens: 1 },
      }),
    ])
      state = def.apply(state, event as never);
    expect(def.wire.view(state).cost).toEqual({
      p: {
        m: { peak: { uncached: 2, cacheRead: 0, cacheWrite: 0, output: 1 } },
      },
    });
    const child = (
      activity.init as (
        header: unknown,
        from: number,
      ) => ReturnType<typeof activity.init>
    )({}, 3);
    expect(activity.stateSchema.safeParse(child).success).toBe(true);
  });
  it("partitions resumed runs without recharging the previous run", () => {
    const [whole] = captureDefinitions(1);
    const [first] = captureDefinitions(1);
    const [second] = captureDefinitions(100);
    const events = [header(0, {provider:"p",model:"m",time:2}),
      assistantMessage(1,{time:3,usage:{inputTokens:20,outputTokens:4}}),
      assistantMessage(2,{time:101,usage:{inputTokens:7,outputTokens:2}})];
    const project = (def: typeof whole, list: typeof events) => {
      let state=def.init(); for (const event of list) state=def.apply(state,event as never);
      return def.wire.view(state).cost;
    };
    expect(project(first,events.slice(0,2))?.p?.m?.peak?.uncached).toBe(20);
    expect(project(second,events)?.p?.m?.peak?.uncached).toBe(7);
    expect(project(whole,events)?.p?.m?.peak?.uncached).toBe(27);
  });
  it("uses the official closed-step counter for cancelled steps", () => {
    const def = captureDefinitions(1)[3];
    let state = def.init();
    state = def.apply(state, {
      type: "step/start",
      seq: 0,
      time: 5,
      data: { turn: 1, step: 1 },
    } as never);
    state = def.apply(state, {
      type: "step/end",
      seq: 1,
      time: 6,
      data: { turn: 1, step: 1 },
    } as never);
    expect(def.wire.view(state).steps).toBe(1);
  });
});
