// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { PromptLibraryItem, PromptLibraryTrial } from "../types";
import {
  createPromptLibraryItemWithRecovery,
  createPromptLibraryTrialWithRecovery,
  createPromptLibraryVersionWithRecovery,
  usePromptLibraryCreateStore,
} from "./trial-create";

const trial = (id: string) => ({ id }) as PromptLibraryTrial;

describe("createPromptLibraryTrialWithRecovery", () => {
  beforeEach(() => {
    setCurrentWorkspace("test-account", "workspace-1");
    localStorage.clear();
    usePromptLibraryCreateStore.setState({ pending: {}, item: undefined, versions: {} });
  });

  it("recovers an item create before submitting a changed draft", async () => {
    usePromptLibraryCreateStore.getState().setItem({
      request: { name: "old", content: "old" },
      requestKey: "10000000-0000-4000-8000-000000000005",
      createdAt: Date.now(),
    });
    const createPromptLibraryItem = vi.fn()
      .mockResolvedValueOnce({ id: "item-1" } as PromptLibraryItem)
      .mockResolvedValueOnce({ id: "item-2" } as PromptLibraryItem);
    await expect(createPromptLibraryItemWithRecovery(
      { name: "new", content: "new" },
      { createPromptLibraryItem },
    )).resolves.toMatchObject({ id: "item-2" });
    expect(createPromptLibraryItem).toHaveBeenCalledTimes(2);
    expect(usePromptLibraryCreateStore.getState().item).toBeUndefined();
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
    const pending = usePromptLibraryCreateStore.getState().versions["prompt-1"];
    expect(pending?.request).toEqual({ content: "reliable", change_note: "reason" });
    expect(pending?.requestKey).toMatch(/^[0-9a-f-]{36}$/);
  });

  it("preserves the exact trial intent after an unknown outcome", async () => {
    const createPromptLibraryTrial = vi.fn().mockRejectedValue(
      new ApiTransportError("POST trial", true, new Error("lost")),
    );
    await expect(createPromptLibraryTrialWithRecovery("prompt-1", "version-1", {
      agent_id: "agent-1",
      variables: { topic: "reliability" },
    }, { createPromptLibraryTrial })).rejects.toBeInstanceOf(ApiTransportError);
    const pending = Object.values(usePromptLibraryCreateStore.getState().pending)[0];
    expect(pending?.request).toEqual({ agent_id: "agent-1", variables: { topic: "reliability" } });
    expect(pending?.requestKey).toMatch(/^[0-9a-f-]{36}$/);
    expect(localStorage.getItem("multica_prompt_library_trial_create:test-account")).toContain("reliability");
  });

  it("recovers an older trial before submitting changed variables", async () => {
    const scope = "prompt-1:version-1:agent-1";
    usePromptLibraryCreateStore.getState().setPending(scope, {
      promptId: "prompt-1", versionId: "version-1", requestKey: "10000000-0000-4000-8000-000000000004",
      request: { agent_id: "agent-1", variables: { topic: "old" } },
      createdAt: Date.now(),
    });
    const createPromptLibraryTrial = vi.fn().mockResolvedValueOnce(trial("trial-1")).mockResolvedValueOnce(trial("trial-2"));
    const client = { createPromptLibraryTrial };
    await expect(createPromptLibraryTrialWithRecovery("prompt-1", "version-1", {
      agent_id: "agent-1", variables: { topic: "new" },
    }, client)).resolves.toMatchObject({ id: "trial-2" });
    expect(createPromptLibraryTrial).toHaveBeenCalledTimes(2);
    expect(usePromptLibraryCreateStore.getState().pending).toEqual({});
  });
});
