// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { AgentRuntime } from "@multica/core/types";
import {
  buildRuntimeMachines,
  filterRuntimeMachines,
  runtimeMachineCounts,
  runtimeRowLabel,
  sharedCustomName,
  splitRuntimeName,
} from "./runtime-machines";

const NOW = new Date("2026-05-17T12:00:00Z").getTime();

function makeRuntime(overrides: Partial<AgentRuntime> = {}): AgentRuntime {
  return {
    id: "runtime-1",
    workspace_id: "ws-1",
    daemon_id: "daemon-1",
    name: "Claude (dev-machine.local)",
    runtime_mode: "local",
    provider: "claude",
    launch_header: "",
    status: "online",
    device_info: "dev-machine.local · claude 1.0.0",
    metadata: { cli_version: "0.3.0" },
    owner_id: "user-1",
    visibility: "private",
    last_seen_at: new Date(NOW - 10_000).toISOString(),
    created_at: "2026-05-17T11:00:00Z",
    updated_at: "2026-05-17T11:00:00Z",
    ...overrides,
  };
}

describe("runtime machine grouping", () => {
  it("groups providers by daemon and labels local machines as remote to the Web client", () => {
    const machines = buildRuntimeMachines(
      [
        makeRuntime({ id: "rt-claude", provider: "claude", name: "Claude (dev.local)" }),
        makeRuntime({ id: "rt-codex", provider: "codex", name: "Codex (dev.local)" }),
      ],
      { now: NOW },
    );

    expect(machines).toHaveLength(1);
    expect(machines[0]).toMatchObject({
      id: "local:daemon-1",
      title: "dev.local",
      section: "remote",
      onlineCount: 2,
      issueCount: 0,
      providerNames: ["claude", "codex"],
    });
  });

  it("groups cloud runtimes separately", () => {
    const machines = buildRuntimeMachines(
      [makeRuntime({ runtime_mode: "cloud", daemon_id: null, name: "Codex" })],
      { now: NOW },
    );
    expect(machines[0]).toMatchObject({ section: "cloud", mode: "cloud" });
  });

  it("uses an online runtime report for the machine CLI version", () => {
    const machines = buildRuntimeMachines(
      [
        makeRuntime({ id: "online", metadata: { cli_version: "0.4.0" } }),
        makeRuntime({
          id: "stale",
          provider: "copilot",
          status: "offline",
          last_seen_at: new Date(NOW - 4 * 24 * 60 * 60_000).toISOString(),
          metadata: { cli_version: "0.3.17" },
        }),
      ],
      { now: NOW },
    );
    expect(machines[0]?.cliVersion).toBe("0.4.0");
  });

  it("uses a shared custom name and falls back to device information", () => {
    const named = makeRuntime({ custom_name: "Research box" });
    expect(buildRuntimeMachines([named], { now: NOW })[0]?.title).toBe("Research box");
    expect(
      buildRuntimeMachines(
        [makeRuntime({ name: "Claude", device_info: "linux · x86_64" })],
        { now: NOW },
      )[0]?.title,
    ).toBe("linux");
  });

  it("filters and counts machines by health and searchable fields", () => {
    const machines = buildRuntimeMachines(
      [
        makeRuntime({ id: "online", name: "Claude (alpha)", device_info: "alpha" }),
        makeRuntime({
          id: "offline",
          daemon_id: "daemon-2",
          name: "Codex (beta)",
          device_info: "beta",
          status: "offline",
          last_seen_at: new Date(NOW - 10 * 24 * 60 * 60_000).toISOString(),
        }),
      ],
      { now: NOW },
    );
    expect(filterRuntimeMachines(machines, "beta", "all")).toHaveLength(1);
    expect(filterRuntimeMachines(machines, "", "online")).toHaveLength(1);
    expect(runtimeMachineCounts(machines)).toEqual({ all: 2, online: 1, issues: 1 });
  });

  it("derives stable runtime labels and handles missing names", () => {
    expect(splitRuntimeName("Claude (alpha)")).toEqual({ base: "Claude", hostname: "alpha" });
    expect(splitRuntimeName("Claude")).toEqual({ base: "Claude", hostname: null });
    expect(runtimeRowLabel(makeRuntime({ name: "Claude (alpha)" }), "alpha")).toBe("Claude");
    expect(
      sharedCustomName([
        makeRuntime({ custom_name: "box" }),
        makeRuntime({ id: "two", custom_name: "box" }),
      ]),
    ).toBe("box");
    expect(
      sharedCustomName([
        makeRuntime({ custom_name: "box" }),
        makeRuntime({ id: "two", custom_name: "other" }),
      ]),
    ).toBeNull();
  });
});
