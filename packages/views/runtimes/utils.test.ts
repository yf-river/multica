import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import type { AgentRuntime, RuntimeUsage, RuntimeUsageByTask } from "@multica/core/types";

import {
  addDaysIso,
  aggregateByWeek,
  aggregateCostByTask,
  computeCostInWindow,
  isSelfHealingRuntime,
  sliceWindow,
  todayIso,
  usageTokenTotal,
  weekStartIso,
} from "./utils";

describe("isSelfHealingRuntime", () => {
  function makeRuntime(overrides: Partial<AgentRuntime>): AgentRuntime {
    return {
      id: "rt-1",
      workspace_id: "ws-1",
      daemon_id: null,
      name: "rt",
      runtime_mode: "local",
      provider: "claude",
      launch_header: "",
      status: "online",
      device_info: "",
      metadata: {},
      owner_id: null,
      scope: "personal",
      last_seen_at: null,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
      ...overrides,
    };
  }

  it("flags an online local runtime as self-healing", () => {
    expect(
      isSelfHealingRuntime(
        makeRuntime({ runtime_mode: "local", status: "online" }),
      ),
    ).toBe(true);
  });

  it("treats an offline local runtime as safe to delete", () => {
    // Daemon isn't running, so the server-side delete is final — no
    // re-registration race to worry about.
    expect(
      isSelfHealingRuntime(
        makeRuntime({ runtime_mode: "local", status: "offline" }),
      ),
    ).toBe(false);
  });

});

describe("aggregateCostByTask", () => {
  it("folds multi-model rows into one task total after pricing each model row", () => {
    const rows: RuntimeUsageByTask[] = [
      {
        task_id: "task-1",
        issue_id: "issue-1",
        issue_number: 42,
        issue_title: "Investigate usage",
        agent_id: "agent-1",
        status: "completed",
        started_at: "2026-05-19T00:00:00Z",
        completed_at: "2026-05-19T00:01:00Z",
        provider: "anthropic",
        model: "claude-sonnet-4-6",
        input_tokens: 1_000_000,
        output_tokens: 0,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        cost_usd: 3,
        input_cost_usd: 3,
        output_cost_usd: 0,
        cache_read_cost_usd: 0,
        cache_write_cost_usd: 0,
        cache_savings_usd: 0,
        priced: true,
      },
      {
        task_id: "task-1",
        issue_id: "issue-1",
        issue_number: 42,
        issue_title: "Investigate usage",
        agent_id: "agent-1",
        status: "completed",
        started_at: "2026-05-19T00:00:00Z",
        completed_at: "2026-05-19T00:01:00Z",
        provider: "cursor",
        model: "auto",
        input_tokens: 1_000_000,
        output_tokens: 0,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        cost_usd: 1.25,
        input_cost_usd: 1.25,
        output_cost_usd: 0,
        cache_read_cost_usd: 0,
        cache_write_cost_usd: 0,
        cache_savings_usd: 0,
        priced: true,
      },
      {
        task_id: "task-2",
        issue_id: null,
        issue_number: 0,
        issue_title: "",
        agent_id: "agent-1",
        status: "failed",
        started_at: null,
        completed_at: null,
        provider: "fictional",
        model: "unknown-model",
        input_tokens: 500,
        output_tokens: 0,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        cost_usd: 0,
        input_cost_usd: 0,
        output_cost_usd: 0,
        cache_read_cost_usd: 0,
        cache_write_cost_usd: 0,
        cache_savings_usd: 0,
        priced: false,
      },
    ];

    const byTask = aggregateCostByTask(rows);

    expect(byTask).toHaveLength(2);
    expect(byTask[0]?.key).toBe("task-1");
    expect(byTask[0]?.tokens).toBe(2_000_000);
    expect(byTask[0]?.cost).toBeCloseTo(4.25, 5);
    expect(byTask[0]?.issueNumber).toBe(42);
    expect(byTask[1]?.key).toBe("task-2");
    expect(byTask[1]?.tokens).toBe(500);
    expect(byTask[1]?.cost).toBe(0);
  });

  it("does not add CodeBuddy cache tokens into task token totals", () => {
    const rows: RuntimeUsageByTask[] = [
      {
        task_id: "task-codebuddy",
        issue_id: "issue-1",
        issue_number: 42,
        issue_title: "Investigate usage",
        agent_id: "agent-1",
        status: "completed",
        started_at: "2026-07-09T00:00:00Z",
        completed_at: "2026-07-09T00:01:00Z",
        provider: "codebuddy",
        model: "deepseek-v4-pro-ioa",
        input_tokens: 59_738,
        output_tokens: 186,
        cache_read_tokens: 29_440,
        cache_write_tokens: 30_298,
        cost_usd: 0.013448,
        input_cost_usd: 0.01318,
        output_cost_usd: 0.000162,
        cache_read_cost_usd: 0.000107,
        cache_write_cost_usd: 0,
        cache_savings_usd: 0.0127,
        priced: true,
      },
    ];

    expect(usageTokenTotal(rows[0]!)).toBe(59_924);
    expect(aggregateCostByTask(rows)[0]?.tokens).toBe(59_924);
  });

  it("recovers CodeBuddy legacy rows where input stored only uncached tokens", () => {
    expect(
      usageTokenTotal({
        provider: "codebuddy",
        input_tokens: 0,
        output_tokens: 186,
        cache_read_tokens: 29_440,
        cache_write_tokens: 30_298,
      }),
    ).toBe(59_924);
  });
});

// ---------------------------------------------------------------------------
// Calendar helpers + weekly aggregation. All of these run on YYYY-MM-DD
// strings (the wire shape of RuntimeUsage.date) and on a runtime-supplied
// IANA timezone — the host browser's tz should never affect the result.
// ---------------------------------------------------------------------------

describe("weekStartIso", () => {
  it("returns the Monday of the same ISO week", () => {
    // 2026-05-19 is a Tuesday → Monday is 2026-05-18.
    expect(weekStartIso("2026-05-19")).toBe("2026-05-18");
  });

  it("treats Monday as the start of its own week (idempotent)", () => {
    expect(weekStartIso("2026-05-18")).toBe("2026-05-18");
  });

  it("rolls Sunday back to the previous Monday", () => {
    // 2026-05-17 is a Sunday → Monday is 2026-05-11.
    expect(weekStartIso("2026-05-17")).toBe("2026-05-11");
  });

  it("crosses month and year boundaries", () => {
    // 2026-01-03 is a Saturday → Monday is 2025-12-29.
    expect(weekStartIso("2026-01-03")).toBe("2025-12-29");
  });
});

describe("addDaysIso", () => {
  it("adds across month boundary", () => {
    expect(addDaysIso("2026-05-30", 3)).toBe("2026-06-02");
  });

  it("subtracts across year boundary", () => {
    expect(addDaysIso("2026-01-02", -5)).toBe("2025-12-28");
  });
});

describe("todayIso", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("uses the runtime's timezone, not the host's, to decide today", () => {
    // 2026-05-19 16:00 UTC. In Asia/Shanghai (UTC+8) it's already 2026-05-20.
    // In America/Los_Angeles (UTC-7 on this date) it's still 2026-05-19.
    vi.setSystemTime(new Date("2026-05-19T16:00:00Z"));
    expect(todayIso("Asia/Shanghai")).toBe("2026-05-20");
    expect(todayIso("America/Los_Angeles")).toBe("2026-05-19");
    expect(todayIso("UTC")).toBe("2026-05-19");
  });
});

describe("sliceWindow (timezone-aware)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  function makeUsage(date: string): RuntimeUsage {
    return {
      runtime_id: "r",
      date,
      provider: "anthropic",
      model: "claude-sonnet-4-6",
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      cost_usd: 0,
      input_cost_usd: 0,
      output_cost_usd: 0,
      cache_read_cost_usd: 0,
      cache_write_cost_usd: 0,
      cache_savings_usd: 0,
      priced: true,
    };
  }

  it("cuts the current window at today-in-tz, not today-in-host-utc", () => {
    // Host clock is 2026-05-19 23:00 UTC → still May 19 in UTC, May 20 in Shanghai.
    // A daily-usage row dated 2026-05-20 (the runtime's "today" in Shanghai)
    // should be included in the current window when tz=Asia/Shanghai.
    vi.setSystemTime(new Date("2026-05-19T23:00:00Z"));
    const usage = [
      makeUsage("2026-05-13"),
      makeUsage("2026-05-19"),
      makeUsage("2026-05-20"),
    ];
    const { filtered } = sliceWindow(usage, 7, "Asia/Shanghai");
    expect(filtered.map((u) => u.date)).toEqual([
      "2026-05-13",
      "2026-05-19",
      "2026-05-20",
    ]);
  });

  it("returns the immediately prior window of equal length", () => {
    vi.setSystemTime(new Date("2026-05-19T12:00:00Z"));
    const usage = [
      makeUsage("2026-05-01"),
      makeUsage("2026-05-08"),
      makeUsage("2026-05-15"),
      makeUsage("2026-05-19"),
    ];
    const { filtered, prevFiltered } = sliceWindow(usage, 7, "UTC");
    expect(filtered.map((u) => u.date)).toEqual(["2026-05-15", "2026-05-19"]);
    expect(prevFiltered.map((u) => u.date)).toEqual(["2026-05-08"]);
  });
});

describe("aggregateByWeek", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  function makeUsage(
    date: string,
    input: number,
    output: number,
  ): RuntimeUsage {
    return {
      runtime_id: "r",
      date,
      provider: "anthropic",
      model: "claude-sonnet-4-6",
      input_tokens: input,
      output_tokens: output,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      cost_usd: (input * 3 + output * 15) / 1_000_000,
      input_cost_usd: (input * 3) / 1_000_000,
      output_cost_usd: (output * 15) / 1_000_000,
      cache_read_cost_usd: 0,
      cache_write_cost_usd: 0,
      cache_savings_usd: 0,
      priced: true,
    };
  }

  it("groups daily rows into Mon-anchored ISO weeks", () => {
    // 2026-05-24 is Sunday, so the calendar week containing "today" is
    // Mon=05-18..Sun=05-24. With weekCount=2 the window covers weeks
    // 2026-05-11 and 2026-05-18 — exactly the two weeks the rows fall in.
    vi.setSystemTime(new Date("2026-05-24T12:00:00Z"));
    // 2026-05-11 is Mon; 2026-05-17 is Sun (same week).
    // 2026-05-18 is Mon (next week).
    const rows = [
      makeUsage("2026-05-11", 1_000_000, 0),
      makeUsage("2026-05-17", 0, 1_000_000),
      makeUsage("2026-05-18", 2_000_000, 0),
    ];
    const { weeklyTokens } = aggregateByWeek(rows, "UTC", 2);
    expect(weeklyTokens).toHaveLength(2);
    expect(weeklyTokens[0]).toMatchObject({
      weekStart: "2026-05-11",
      weekEnd: "2026-05-17",
      input: 1_000_000,
      output: 1_000_000,
      partial: false,
      daysCovered: 7,
    });
    expect(weeklyTokens[1]).toMatchObject({
      weekStart: "2026-05-18",
      weekEnd: "2026-05-24",
      input: 2_000_000,
      partial: false,
      daysCovered: 7,
    });
  });

  it("flags the in-progress week as partial with days-elapsed count", () => {
    // 2026-05-20 is a Wednesday (Mon=05-18, Sun=05-24).
    vi.setSystemTime(new Date("2026-05-20T08:00:00Z"));
    const rows = [makeUsage("2026-05-18", 1_000_000, 0)];
    const { weeklyTokens } = aggregateByWeek(rows, "UTC", 1);
    expect(weeklyTokens[0]).toMatchObject({
      weekStart: "2026-05-18",
      weekEnd: "2026-05-24",
      partial: true,
      daysCovered: 3, // Mon, Tue, Wed
    });
  });

  it("sums server-computed costs per week", () => {
    // 2026-05-17 sits in the calendar week of 2026-05-11..2026-05-17, so
    // weekCount=1 anchors the window on that same week.
    vi.setSystemTime(new Date("2026-05-17T12:00:00Z"));
    // Each row carries $18 from the backend. Two rows in the same week
    // (Mon + Wed) → $36 weekly total.
    const rows = [
      makeUsage("2026-05-11", 1_000_000, 1_000_000),
      makeUsage("2026-05-13", 1_000_000, 1_000_000),
    ];
    const { weeklyCostStack } = aggregateByWeek(rows, "UTC", 1);
    expect(weeklyCostStack).toHaveLength(1);
    expect(weeklyCostStack[0]?.total).toBeCloseTo(36, 2);
  });

  it("emits trailing calendar weeks pinned to today, dropping older populated weeks", () => {
    // Regression for MUL-2382 weekly window scoping:
    // before the fix, aggregateByWeek built buckets only for weeks that had
    // data and the caller did `.slice(-weekCount)`. With sparse data (an old
    // populated week far outside the selected window plus an empty stretch
    // closer to today), that slice would surface the OLD populated week
    // instead of the trailing in-window weeks. The chart should now show
    // exactly the trailing calendar weeks, with the empty in-range weeks
    // present as zero-valued buckets rather than disappearing.
    vi.setSystemTime(new Date("2026-05-19T12:00:00Z"));
    // 30-day window @ 2026-05-19 → 5 trailing weeks (Mon=04-20, 04-27,
    // 05-04, 05-11, 05-18). 2026-04-13 (Mon) is one week earlier — outside
    // the window. No data in any of the 5 in-range weeks.
    const rows = [makeUsage("2026-04-13", 1_000_000, 1_000_000)];
    const { weeklyTokens, weeklyCostStack } = aggregateByWeek(rows, "UTC", 5);

    expect(weeklyTokens.map((w) => w.weekStart)).toEqual([
      "2026-04-20",
      "2026-04-27",
      "2026-05-04",
      "2026-05-11",
      "2026-05-18",
    ]);
    // Every in-range week is empty — the old populated week was dropped.
    for (const w of weeklyTokens) {
      expect(w.input).toBe(0);
      expect(w.output).toBe(0);
      expect(w.cacheRead).toBe(0);
      expect(w.cacheWrite).toBe(0);
    }
    for (const w of weeklyCostStack) {
      expect(w.total).toBe(0);
    }
  });

  it("keeps in-window weeks empty when nearby data sits inside the window", () => {
    // Sparse-but-in-range case: only the oldest in-window week has data;
    // the remaining trailing weeks must render as empty buckets, not be
    // collapsed to a single populated bar.
    vi.setSystemTime(new Date("2026-05-19T12:00:00Z"));
    const rows = [makeUsage("2026-04-22", 1_000_000, 1_000_000)]; // week of 04-20
    const { weeklyTokens } = aggregateByWeek(rows, "UTC", 5);
    expect(weeklyTokens).toHaveLength(5);
    expect(weeklyTokens[0]).toMatchObject({
      weekStart: "2026-04-20",
      input: 1_000_000,
      output: 1_000_000,
    });
    for (const w of weeklyTokens.slice(1)) {
      expect(w.input).toBe(0);
      expect(w.output).toBe(0);
    }
  });
});

// computeCostInWindow drives the runtime-list cost cell and its ↑/↓ delta.
// The `tz` argument was inserted as the THIRD positional parameter (before
// `offsetDays`) in the timezone-architecture RFC — a positional-arg slip
// here is otherwise silent, so the window math, the inclusive-today boundary,
// the offset shift, and the tz-of-"today" all need explicit coverage.
describe("computeCostInWindow", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  // claude-sonnet-4-6 is priced at $3 / 1M input tokens, so a row with
  // 1M input tokens contributes exactly $3.
  function priced(date: string, inputTokens: number): RuntimeUsage {
    return {
      runtime_id: "r",
      date,
      provider: "anthropic",
      model: "claude-sonnet-4-6",
      input_tokens: inputTokens,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      cost_usd: (inputTokens * 3) / 1_000_000,
      input_cost_usd: (inputTokens * 3) / 1_000_000,
      output_cost_usd: 0,
      cache_read_cost_usd: 0,
      cache_write_cost_usd: 0,
      cache_savings_usd: 0,
      priced: true,
    };
  }

  it("sums cost over the trailing daysBack window, including today", () => {
    // 2026-05-19 23:00 UTC is already 2026-05-20 in Asia/Shanghai, so
    // "today" is 2026-05-20 and the 7-day window is [2026-05-14, 2026-05-21).
    vi.setSystemTime(new Date("2026-05-19T23:00:00Z"));
    const rows = [
      priced("2026-05-13", 1_000_000), // before window — excluded
      priced("2026-05-14", 1_000_000), // window start — included
      priced("2026-05-19", 1_000_000), // included
      priced("2026-05-20", 1_000_000), // today — included
      priced("2026-05-21", 1_000_000), // after today — excluded
    ];
    expect(computeCostInWindow(rows, 7, "Asia/Shanghai")).toBeCloseTo(9, 5);
  });

  it("offsetDays shifts the window back to the prior period", () => {
    // today = 2026-05-20; offsetDays=7, daysBack=7 → window [05-07, 05-14).
    vi.setSystemTime(new Date("2026-05-20T12:00:00Z"));
    const rows = [
      priced("2026-05-06", 1_000_000), // before prior window — excluded
      priced("2026-05-07", 1_000_000), // prior window start — included
      priced("2026-05-12", 1_000_000), // included
      priced("2026-05-13", 1_000_000), // included
      priced("2026-05-14", 1_000_000), // in the current window, not prior — excluded
    ];
    expect(computeCostInWindow(rows, 7, "UTC", 7)).toBeCloseTo(9, 5);
  });

  it("reads 'today' in the supplied tz, not the host clock", () => {
    // Host clock is 2026-05-19 in UTC but already 2026-05-20 in Shanghai.
    // A row dated 2026-05-20 falls inside the 1-day window only when the
    // tz pushes "today" forward to 2026-05-20.
    vi.setSystemTime(new Date("2026-05-19T20:00:00Z"));
    const rows = [priced("2026-05-20", 1_000_000)];
    expect(computeCostInWindow(rows, 1, "UTC")).toBe(0); // today=05-19, window [05-19,05-20)
    expect(computeCostInWindow(rows, 1, "Asia/Shanghai")).toBeCloseTo(3, 5);
  });

  it("returns 0 for an unpriced model rather than NaN", () => {
    vi.setSystemTime(new Date("2026-05-20T12:00:00Z"));
    const rows: RuntimeUsage[] = [
      {
        ...priced("2026-05-19", 1_000_000),
        model: "totally-made-up-model",
        cost_usd: 0,
        input_cost_usd: 0,
        priced: false,
      },
    ];
    expect(computeCostInWindow(rows, 7, "UTC")).toBe(0);
  });

  it("returns 0 for an empty row set", () => {
    vi.setSystemTime(new Date("2026-05-20T12:00:00Z"));
    expect(computeCostInWindow([], 7, "UTC")).toBe(0);
  });
});
