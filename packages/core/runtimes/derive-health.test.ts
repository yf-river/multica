import { describe, expect, it } from "vitest";
import type { AgentRuntime } from "../types";
import { deriveRuntimeHealth } from "./derive-health";

const FIXED_NOW = new Date("2026-04-27T12:00:00Z").getTime();

function makeRuntime(overrides: Partial<AgentRuntime> = {}): AgentRuntime {
  return {
    id: "rt-1",
    daemon_id: "daemon-1",
    name: "Test Runtime",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: {},
    owner_id: null,
    scope: "personal",
    profile_id: null,
    last_seen_at: new Date(FIXED_NOW - 10_000).toISOString(),
    ...overrides,
  };
}

describe("deriveRuntimeHealth", () => {
  it.each([
    ["online ignores missing heartbeat", "online", null, "online"],
    ["recent loss", "offline", 2 * 60_000, "recently_lost"],
    ["ordinary offline", "offline", 60 * 60_000, "offline"],
    ["approaching GC", "offline", 6.5 * 24 * 3600_000, "about_to_gc"],
    ["missing heartbeat", "offline", null, "about_to_gc"],
    [
      "inside five-minute boundary",
      "offline",
      5 * 60_000 - 1_000,
      "recently_lost",
    ],
    ["outside five-minute boundary", "offline", 5 * 60_000 + 1_000, "offline"],
  ] as const)("derives %s", (_case, status, ageMs, expected) => {
    const lastSeenAt =
      ageMs === null ? null : new Date(FIXED_NOW - ageMs).toISOString();
    expect(
      deriveRuntimeHealth(
        makeRuntime({ status, last_seen_at: lastSeenAt }),
        FIXED_NOW,
      ),
    ).toBe(expected);
  });
});
