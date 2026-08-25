import { describe, expect, it } from "vitest";
import type { AgentRuntime, RuntimeProfile } from "@multica/core/types";
import {
  isPendingCustomRuntime,
  isPendingCustomRuntimeWarning,
  pendingRuntimeCommandName,
  pendingRuntimesForProfiles,
} from "./pending-runtime";

function profile(overrides: Partial<RuntimeProfile> = {}): RuntimeProfile {
  return {
    id: "profile-1",
    display_name: "Team Codex",
    protocol_family: "codex",
    command_name: "team-codex",
    description: null,
    fixed_args: [],
    enabled: true,
    updated_at: "2026-01-02T00:00:00Z",
    ...overrides,
  };
}

function runtime(overrides: Partial<AgentRuntime> = {}): AgentRuntime {
  return {
    id: "runtime-1",
    daemon_id: "daemon-1",
    name: "Codex (MacBook)",
    runtime_mode: "local",
    provider: "codex",
    launch_header: "codex",
    status: "online",
    device_info: "MacBook",
    metadata: {},
    owner_id: "user-1",
    scope: "personal",
    profile_id: null,
    last_seen_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("pending custom runtime rows", () => {
  it("builds a pending runtime from the newly created profile", () => {
    const createdAt = Date.parse("2026-01-01T00:00:00Z");
    const pending = pendingRuntimesForProfiles({
      pendingProfiles: [{ profile: profile(), createdAt }],
      runtimes: [],
      ownerId: "user-1",
      localDaemonId: "daemon-1",
      localMachineName: "MacBook",
    })[0]!;

    expect(pending.id).toBe("pending-runtime-profile:profile-1");
    expect(pending.name).toBe("Team Codex (MacBook)");
    expect(pending.daemon_id).toBeNull();
    expect(pending.profile_id).toBe("profile-1");
    expect(pending.provider).toBe("codex");
    expect(isPendingCustomRuntime(pending)).toBe(true);
    expect(pendingRuntimeCommandName(pending)).toBe("team-codex");
  });

  it("drops the pending row once a real runtime registers for the profile", () => {
    const createdAt = Date.parse("2026-01-01T00:00:00Z");
    const prof = profile();
    const baseRuntime = runtime();
    const registeredRuntime = runtime({
      id: "runtime-custom",
      profile_id: prof.id,
    });

    expect(
      pendingRuntimesForProfiles({
        pendingProfiles: [{ profile: prof, createdAt }],
        runtimes: [baseRuntime],
        ownerId: "user-1",
      }).map((item) => item.id),
    ).toEqual(["runtime-1", "pending-runtime-profile:profile-1"]);

    expect(
      pendingRuntimesForProfiles({
        pendingProfiles: [{ profile: prof, createdAt }],
        runtimes: [baseRuntime, registeredRuntime],
        ownerId: "user-1",
      }).map((item) => item.id),
    ).toEqual(["runtime-1", "runtime-custom"]);
  });

  it("marks pending runtimes as waiting after the grace window", () => {
    const createdAt = Date.parse("2026-01-01T00:00:00Z");
    const pending = pendingRuntimesForProfiles({
      pendingProfiles: [{ profile: profile(), createdAt }],
      runtimes: [],
    })[0]!;

    expect(
      isPendingCustomRuntimeWarning(
        pending,
        createdAt + 45_000 - 1,
      ),
    ).toBe(false);
    expect(
      isPendingCustomRuntimeWarning(
        pending,
        createdAt + 45_000,
      ),
    ).toBe(true);
  });
});
