import { describe, expect, it } from "vitest";
import type { RuntimeDevice } from "@multica/core/types";
import {
  bestRuntimeForPMProvider,
  isRuntimeCompatibleWithPMScope,
  pmProviderChoices,
} from "./pm-runtime-selection";

function runtime(input: Partial<RuntimeDevice> & { id: string }): RuntimeDevice {
  return {
    id: input.id,
    workspace_id: input.workspace_id ?? "ws-1",
    daemon_id: input.daemon_id ?? null,
    name: input.name ?? input.id,
    runtime_mode: input.runtime_mode ?? "local",
    provider: input.provider ?? "codebuddy",
    launch_header: input.launch_header ?? "",
    status: input.status ?? "online",
    device_info: input.device_info ?? "",
    metadata: input.metadata ?? {},
    owner_id: input.owner_id ?? "user-1",
    scope: input.scope ?? "workspace",
    profile_id: input.profile_id,
    last_seen_at: input.last_seen_at ?? "2026-07-02T01:00:00Z",
    created_at: input.created_at ?? "2026-07-02T00:00:00Z",
    updated_at: input.updated_at ?? "2026-07-02T00:00:00Z",
  };
}

describe("PM runtime selection", () => {
  it("keeps workspace PM squads on workspace runtimes", () => {
    const runtimes = [
      runtime({ id: "personal", scope: "personal", owner_id: "user-1" }),
      runtime({ id: "workspace", scope: "workspace" }),
    ];

    expect(pmProviderChoices(runtimes, "workspace", "user-1")).toEqual([
      "codebuddy",
    ]);
    expect(
      isRuntimeCompatibleWithPMScope(runtimes[0]!, "workspace", "user-1"),
    ).toBe(false);
    expect(
      bestRuntimeForPMProvider(runtimes, "codebuddy", "workspace", "user-1")
        ?.id,
    ).toBe("workspace");
  });

  it("keeps personal PM squads on the creator's personal runtimes", () => {
    const runtimes = [
      runtime({ id: "mine", scope: "personal", owner_id: "user-1" }),
      runtime({ id: "other", scope: "personal", owner_id: "user-2" }),
      runtime({ id: "workspace", scope: "workspace" }),
    ];

    expect(pmProviderChoices(runtimes, "personal", "user-1")).toEqual([
      "codebuddy",
    ]);
    expect(
      isRuntimeCompatibleWithPMScope(runtimes[1]!, "personal", "user-1"),
    ).toBe(false);
    expect(
      isRuntimeCompatibleWithPMScope(runtimes[2]!, "personal", "user-1"),
    ).toBe(false);
    expect(
      bestRuntimeForPMProvider(runtimes, "codebuddy", "personal", "user-1")
        ?.id,
    ).toBe("mine");
  });

  it("chooses the newest online compatible runtime for the provider", () => {
    const runtimes = [
      runtime({
        id: "old",
        provider: "codebuddy",
        scope: "workspace",
        last_seen_at: "2026-07-02T01:00:00Z",
      }),
      runtime({
        id: "newer",
        provider: "codebuddy",
        scope: "workspace",
        last_seen_at: "2026-07-02T02:00:00Z",
      }),
      runtime({
        id: "offline",
        provider: "codebuddy",
        scope: "workspace",
        status: "offline",
        last_seen_at: "2026-07-02T03:00:00Z",
      }),
    ];

    expect(
      bestRuntimeForPMProvider(runtimes, "codebuddy", "workspace", "user-1")
        ?.id,
    ).toBe("newer");
  });
});
