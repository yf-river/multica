// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { PromptLibraryItem, PromptLibraryTrial } from "../types";
import {
  createPromptLibraryItemWithRecovery,
  createPromptLibraryTrialWithRecovery,
  createPromptLibraryVersionWithRecovery,
} from "./trial-create";

const trial = (id: string) => ({ id }) as PromptLibraryTrial;
let workspaceSequence = 0;

describe("createPromptLibraryTrialWithRecovery", () => {
  beforeEach(async () => {
    localStorage.clear();
    workspaceSequence += 1;
    setCurrentWorkspace(`prompt-library-${workspaceSequence}`, `workspace-${workspaceSequence}`);
    await Promise.resolve();
    await Promise.resolve();
  });

  it("recovers an item create before submitting a changed draft", async () => {
    const createPromptLibraryItem = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST old item", true, new Error("lost")))
      .mockResolvedValueOnce({ id: "item-1" } as PromptLibraryItem)
      .mockResolvedValueOnce({ id: "item-2" } as PromptLibraryItem);
    await expect(createPromptLibraryItemWithRecovery(
      { name: "old", content: "old" },
      { createPromptLibraryItem },
    )).rejects.toBeInstanceOf(ApiTransportError);
    await expect(createPromptLibraryItemWithRecovery(
      { name: "new", content: "new" },
      { createPromptLibraryItem },
    )).resolves.toMatchObject({ id: "item-2" });
    expect(createPromptLibraryItem).toHaveBeenCalledTimes(3);
    expect(createPromptLibraryItem.mock.calls[1]).toEqual([
      { name: "old", content: "old" }, createPromptLibraryItem.mock.calls[0]?.[1],
    ]);
  });

  it("persists a version intent when its outcome is unknown", async () => {
    const createPromptLibraryVersion = vi.fn().mockRejectedValue(
      new ApiTransportError("POST version", true, new Error("lost")),
    );
    await expect(createPromptLibraryVersionWithRecovery(
      "prompt-1",
      { content: "reliable", change_note: "reason" },
      { createPromptLibraryVersion },
    )).rejects.toBeInstanceOf(ApiTransportError);
    const stored = localStorage.getItem(
      `multica_prompt_library_trial_create:prompt-library-${workspaceSequence}`,
    ) ?? "";
    expect(stored).toContain("reliable");
    expect(stored).toMatch(/[0-9a-f-]{36}/);
  });

  it("preserves the exact trial intent after an unknown outcome", async () => {
    const createPromptLibraryTrial = vi.fn().mockRejectedValue(
      new ApiTransportError("POST trial", true, new Error("lost")),
    );
    await expect(createPromptLibraryTrialWithRecovery("prompt-1", "version-1", {
      agent_id: "agent-1",
      variables: { topic: "reliability" },
    }, { createPromptLibraryTrial })).rejects.toBeInstanceOf(ApiTransportError);
    const stored = localStorage.getItem(
      `multica_prompt_library_trial_create:prompt-library-${workspaceSequence}`,
    ) ?? "";
    expect(stored).toContain("reliability");
    expect(stored).toMatch(/[0-9a-f-]{36}/);
  });

  it("recovers an older trial before submitting changed variables", async () => {
    const createPromptLibraryTrial = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST old trial", true, new Error("lost")))
      .mockResolvedValueOnce(trial("trial-1"))
      .mockResolvedValueOnce(trial("trial-2"));
    const client = { createPromptLibraryTrial };
    await expect(createPromptLibraryTrialWithRecovery("prompt-1", "version-1", {
      agent_id: "agent-1", variables: { topic: "old" },
    }, client)).rejects.toBeInstanceOf(ApiTransportError);
    await expect(createPromptLibraryTrialWithRecovery("prompt-1", "version-1", {
      agent_id: "agent-1", variables: { topic: "new" },
    }, client)).resolves.toMatchObject({ id: "trial-2" });
    expect(createPromptLibraryTrial).toHaveBeenCalledTimes(3);
    expect(createPromptLibraryTrial.mock.calls[1]).toEqual([
      "prompt-1",
      "version-1",
      { agent_id: "agent-1", variables: { topic: "old" } },
      createPromptLibraryTrial.mock.calls[0]?.[3],
    ]);
  });
});
