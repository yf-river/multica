import { describe, expect, it } from "vitest";
import type { AgentRuntime, RuntimeModel } from "@multica/core/types";
import {
  bestRuntimeForPMProvider,
  pmProviderChoices,
  preferredPMModel,
} from "./pm-runtime-selection";

function runtime(input: Partial<AgentRuntime> & { id: string }): AgentRuntime {
  return {
    id: input.id,
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
    profile_id: input.profile_id ?? null,
    last_seen_at: input.last_seen_at ?? "2026-07-02T01:00:00Z",
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

function model(input: Partial<RuntimeModel> & { id: string }): RuntimeModel {
  return {
    id: input.id,
    label: input.label ?? input.id,
    provider: input.provider ?? "",
    default: input.default ?? false,
    thinking: input.thinking,
  };
}

describe("preferredPMModel", () => {
  it("prefers deepseek-v4-pro-ioa over other DeepSeek models", () => {
    expect(
      preferredPMModel([
        model({ id: "claude-sonnet-4.6", provider: "anthropic" }),
        model({ id: "deepseek-v4-flash-ioa", provider: "deepseek" }),
        model({ id: "deepseek-v4-pro-ioa", provider: "deepseek" }),
      ]),
    ).toBe("deepseek-v4-pro-ioa");
  });

  it("falls back to deepseek-v4-pro when the IOA variant is absent", () => {
    expect(
      preferredPMModel([
        model({ id: "deepseek-v4-flash-ioa", provider: "deepseek" }),
        model({ id: "deepseek-v4-pro", provider: "deepseek" }),
      ]),
    ).toBe("deepseek-v4-pro");
  });

  it("falls back to a DeepSeek model matched by id or label", () => {
    expect(
      preferredPMModel([
        model({ id: "glm-5.2-ioa", provider: "zhipu" }),
        model({ id: "vendor-deepseek-v4", label: "DeepSeek V4" }),
      ]),
    ).toBe("vendor-deepseek-v4");
  });

  it("uses the first returned model when no DeepSeek model exists", () => {
    expect(
      preferredPMModel([
        model({ id: "claude-sonnet-4.6", provider: "anthropic" }),
        model({ id: "gpt-5.5", provider: "openai" }),
      ]),
    ).toBe("claude-sonnet-4.6");
  });

  it("returns empty when the runtime has no models", () => {
    expect(preferredPMModel([])).toBe("");
  });
});
