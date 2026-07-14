// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { PromptEvaluationSkillReEvalAssetResponse } from "../types";
import { preparePromptEvaluationSkillReEvalAssetWithRecovery } from "./skill-re-eval-asset";

const response = (id: string) => ({ asset: { id }, case_count: 0 }) as PromptEvaluationSkillReEvalAssetResponse;
let workspaceSequence = 0;

describe("preparePromptEvaluationSkillReEvalAssetWithRecovery", () => {
  beforeEach(async () => {
    localStorage.clear();
    workspaceSequence += 1;
    setCurrentWorkspace(`skill-re-eval-asset-${workspaceSequence}`, `workspace-${workspaceSequence}`);
    await Promise.resolve();
    await Promise.resolve();
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
  });

  it("recovers an older operation before starting a changed candidate", async () => {
    const preparePromptEvaluationSkillReEvalAsset = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST old re-eval asset", true, new Error("lost")))
      .mockResolvedValueOnce(response("asset-old"))
      .mockResolvedValueOnce(response("asset-new"));
    const client = { preparePromptEvaluationSkillReEvalAsset };

    await expect(preparePromptEvaluationSkillReEvalAssetWithRecovery(
      "candidate-old", { repo_path: "/old" }, client,
    )).rejects.toBeInstanceOf(ApiTransportError);
    await expect(preparePromptEvaluationSkillReEvalAssetWithRecovery(
      "candidate-new", { repo_path: "/new" }, client,
    )).resolves.toMatchObject({ asset: { id: "asset-new" } });
    expect(preparePromptEvaluationSkillReEvalAsset.mock.calls[1]).toEqual([
      "candidate-old", { repo_path: "/old" }, preparePromptEvaluationSkillReEvalAsset.mock.calls[0]?.[2],
    ]);
    expect(preparePromptEvaluationSkillReEvalAsset.mock.calls[2]?.[0]).toBe("candidate-new");
  });

  it("keeps pending preparation isolated to its workspace", async () => {
    const workspaceOneClient = {
      preparePromptEvaluationSkillReEvalAsset: vi.fn()
        .mockRejectedValueOnce(new ApiTransportError("POST workspace one", true, new Error("lost")))
        .mockResolvedValueOnce(response("asset-one")),
    };
    await expect(preparePromptEvaluationSkillReEvalAssetWithRecovery(
      "candidate-one", { repo_path: "/workspace-one" }, workspaceOneClient,
    )).rejects.toBeInstanceOf(ApiTransportError);
    const firstKey = workspaceOneClient.preparePromptEvaluationSkillReEvalAsset.mock.calls[0]?.[2];

    setCurrentWorkspace("skill-re-eval-asset-other", "workspace-other");
    await Promise.resolve();
    await Promise.resolve();
    const workspaceTwoClient = {
      preparePromptEvaluationSkillReEvalAsset: vi.fn().mockResolvedValue(response("asset-two")),
    };
    await expect(preparePromptEvaluationSkillReEvalAssetWithRecovery(
      "candidate-two", { repo_path: "/workspace-two" }, workspaceTwoClient,
    )).resolves.toMatchObject({ asset: { id: "asset-two" } });
    expect(workspaceTwoClient.preparePromptEvaluationSkillReEvalAsset.mock.calls[0]?.[0])
      .toBe("candidate-two");

    setCurrentWorkspace(`skill-re-eval-asset-${workspaceSequence}`, `workspace-${workspaceSequence}`);
    await Promise.resolve();
    await Promise.resolve();
    await expect(preparePromptEvaluationSkillReEvalAssetWithRecovery(
      "candidate-one", { repo_path: "/workspace-one" }, workspaceOneClient,
    )).resolves.toMatchObject({ asset: { id: "asset-one" } });
    expect(workspaceOneClient.preparePromptEvaluationSkillReEvalAsset.mock.calls[1]).toEqual([
      "candidate-one", { repo_path: "/workspace-one" }, firstKey,
    ]);
  });
});
