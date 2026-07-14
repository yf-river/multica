// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { PromptEvaluationSkillReEvalAssetResponse } from "../types";
import {
  preparePromptEvaluationSkillReEvalAssetWithRecovery,
  useSkillReEvalAssetStore,
} from "./skill-re-eval-asset";

const response = (id: string) => ({ asset: { id } }) as PromptEvaluationSkillReEvalAssetResponse;

describe("preparePromptEvaluationSkillReEvalAssetWithRecovery", () => {
  beforeEach(async () => {
    localStorage.clear();
    setCurrentWorkspace("workspace-one", "workspace-1");
    await Promise.resolve();
    useSkillReEvalAssetStore.setState({ pending: undefined });
  });

  it("replays a persisted unknown outcome with the same request identity", async () => {
    const preparePromptEvaluationSkillReEvalAsset = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST skill re-eval asset", true, new Error("lost")))
      .mockResolvedValueOnce(response("asset-1"));
    const client = { preparePromptEvaluationSkillReEvalAsset };
    const request = { repo_path: "/repo", skill_path: "skills/current/SKILL.md" };

    await expect(preparePromptEvaluationSkillReEvalAssetWithRecovery("candidate-1", request, client))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(preparePromptEvaluationSkillReEvalAssetWithRecovery("candidate-1", request, client))
      .resolves.toMatchObject({ asset: { id: "asset-1" } });

    const firstKey = preparePromptEvaluationSkillReEvalAsset.mock.calls[0]?.[2];
    expect(firstKey).toMatch(/^[0-9a-f-]{36}$/);
    expect(preparePromptEvaluationSkillReEvalAsset.mock.calls[1]?.[2]).toBe(firstKey);
    expect(useSkillReEvalAssetStore.getState().pending).toBeUndefined();
  });

  it("recovers an older operation before starting a changed candidate", async () => {
    useSkillReEvalAssetStore.getState().setPending({
      candidateId: "candidate-old",
      request: { repo_path: "/old" },
      requestKey: "10000000-0000-4000-8000-000000000014",
      createdAt: Date.now(),
    });
    const preparePromptEvaluationSkillReEvalAsset = vi.fn()
      .mockResolvedValueOnce(response("asset-old"))
      .mockResolvedValueOnce(response("asset-new"));
    const client = { preparePromptEvaluationSkillReEvalAsset };

    await expect(preparePromptEvaluationSkillReEvalAssetWithRecovery(
      "candidate-new", { repo_path: "/new" }, client,
    )).resolves.toMatchObject({ asset: { id: "asset-new" } });
    expect(preparePromptEvaluationSkillReEvalAsset.mock.calls[0]).toEqual([
      "candidate-old", { repo_path: "/old" }, "10000000-0000-4000-8000-000000000014",
    ]);
    expect(preparePromptEvaluationSkillReEvalAsset.mock.calls[1]?.[0]).toBe("candidate-new");
  });

  it("keeps pending preparation isolated to its workspace", async () => {
    const pending = {
      candidateId: "candidate-one",
      request: { repo_path: "/workspace-one" },
      requestKey: "10000000-0000-4000-8000-000000000015",
      createdAt: Date.now(),
    };
    useSkillReEvalAssetStore.getState().setPending(pending);

    setCurrentWorkspace("workspace-two", "workspace-2");
    await Promise.resolve();
    await Promise.resolve();
    expect(useSkillReEvalAssetStore.getState().pending).toBeUndefined();

    setCurrentWorkspace("workspace-one", "workspace-1");
    await Promise.resolve();
    await Promise.resolve();
    expect(useSkillReEvalAssetStore.getState().pending).toEqual(pending);
  });
});
