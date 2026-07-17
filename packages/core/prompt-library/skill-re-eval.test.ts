// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiTransportError, getApi, setApiInstance } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import {
  preparePromptEvaluationSkillReEvalAssetWithRecovery,
  runPromptEvaluationSkillReEvalWithRecovery,
} from "./skill-re-eval";

const assetResponse = (id: string) => ({ assetId: id, caseCount: 0 });
let workspaceSequence = 0;

beforeEach(async () => {
  vi.restoreAllMocks();
  setApiInstance(new ApiClient("http://core.test"));
  localStorage.clear();
  workspaceSequence += 1;
  setCurrentWorkspace(`skill-re-eval-${workspaceSequence}`, `workspace-${workspaceSequence}`);
  await Promise.resolve();
  await Promise.resolve();
});

describe("preparePromptEvaluationSkillReEvalAssetWithRecovery", () => {
  it("replays a persisted unknown outcome with the same request identity", async () => {
    const preparePromptEvaluationSkillReEvalAsset = vi.spyOn(getApi(), "preparePromptEvaluationSkillReEvalAsset")
      .mockRejectedValueOnce(new ApiTransportError("POST skill re-eval asset", true, new Error("lost")))
      .mockResolvedValueOnce(assetResponse("asset-1"));
    const request = { repo_path: "/repo", skill_path: "skills/current/SKILL.md" };

    await expect(preparePromptEvaluationSkillReEvalAssetWithRecovery("candidate-1", request))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(preparePromptEvaluationSkillReEvalAssetWithRecovery("candidate-1", request))
      .resolves.toEqual({ assetId: "asset-1", caseCount: 0 });

    const firstKey = preparePromptEvaluationSkillReEvalAsset.mock.calls[0]?.[2];
    expect(firstKey).toMatch(/^[0-9a-f-]{36}$/);
    expect(preparePromptEvaluationSkillReEvalAsset.mock.calls[1]?.[2]).toBe(firstKey);
  });

  it("recovers an older operation before starting a changed candidate", async () => {
    const preparePromptEvaluationSkillReEvalAsset = vi.spyOn(getApi(), "preparePromptEvaluationSkillReEvalAsset")
      .mockRejectedValueOnce(new ApiTransportError("POST old re-eval asset", true, new Error("lost")))
      .mockResolvedValueOnce(assetResponse("asset-old"))
      .mockResolvedValueOnce(assetResponse("asset-new"));

    await expect(preparePromptEvaluationSkillReEvalAssetWithRecovery(
      "candidate-old", { repo_path: "/old" },
    )).rejects.toBeInstanceOf(ApiTransportError);
    await expect(preparePromptEvaluationSkillReEvalAssetWithRecovery(
      "candidate-new", { repo_path: "/new" },
    )).resolves.toEqual({ assetId: "asset-new", caseCount: 0 });
    expect(preparePromptEvaluationSkillReEvalAsset.mock.calls[1]).toEqual([
      "candidate-old", { repo_path: "/old" }, preparePromptEvaluationSkillReEvalAsset.mock.calls[0]?.[2],
    ]);
    expect(preparePromptEvaluationSkillReEvalAsset.mock.calls[2]?.[0]).toBe("candidate-new");
  });

  it("keeps pending preparation isolated to its workspace", async () => {
    const preparePromptEvaluationSkillReEvalAsset = vi.spyOn(getApi(), "preparePromptEvaluationSkillReEvalAsset")
      .mockRejectedValueOnce(new ApiTransportError("POST workspace one", true, new Error("lost")))
      .mockResolvedValueOnce(assetResponse("asset-two"))
      .mockResolvedValueOnce(assetResponse("asset-one"));
    await expect(preparePromptEvaluationSkillReEvalAssetWithRecovery(
      "candidate-one", { repo_path: "/workspace-one" },
    )).rejects.toBeInstanceOf(ApiTransportError);
    const firstKey = preparePromptEvaluationSkillReEvalAsset.mock.calls[0]?.[2];

    setCurrentWorkspace("skill-re-eval-other", "workspace-other");
    await Promise.resolve();
    await Promise.resolve();
    await expect(preparePromptEvaluationSkillReEvalAssetWithRecovery(
      "candidate-two", { repo_path: "/workspace-two" },
    )).resolves.toEqual({ assetId: "asset-two", caseCount: 0 });
    expect(preparePromptEvaluationSkillReEvalAsset.mock.calls[1]?.[0])
      .toBe("candidate-two");

    setCurrentWorkspace(`skill-re-eval-${workspaceSequence}`, `workspace-${workspaceSequence}`);
    await Promise.resolve();
    await Promise.resolve();
    await expect(preparePromptEvaluationSkillReEvalAssetWithRecovery(
      "candidate-one", { repo_path: "/workspace-one" },
    )).resolves.toEqual({ assetId: "asset-one", caseCount: 0 });
    expect(preparePromptEvaluationSkillReEvalAsset.mock.calls[2]).toEqual([
      "candidate-one", { repo_path: "/workspace-one" }, firstKey,
    ]);
  });
});

describe("runPromptEvaluationSkillReEvalWithRecovery", () => {
  it("replays a persisted unknown outcome with the same request identity", async () => {
    const runPromptEvaluationSkillReEval = vi.spyOn(getApi(), "runPromptEvaluationSkillReEval")
      .mockRejectedValueOnce(new ApiTransportError("POST skill re-eval", true, new Error("lost")))
      .mockResolvedValueOnce("已入队");
    const request = { asset_id: "asset-1" };

    await expect(runPromptEvaluationSkillReEvalWithRecovery("candidate-1", request))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(runPromptEvaluationSkillReEvalWithRecovery("candidate-1", request))
      .resolves.toBe("已入队");

    const firstKey = runPromptEvaluationSkillReEval.mock.calls[0]?.[2];
    expect(firstKey).toMatch(/^[0-9a-f-]{36}$/);
    expect(runPromptEvaluationSkillReEval.mock.calls[1]?.[2]).toBe(firstKey);
  });

  it("recovers an older operation before starting a changed candidate", async () => {
    const runPromptEvaluationSkillReEval = vi.spyOn(getApi(), "runPromptEvaluationSkillReEval")
      .mockRejectedValueOnce(new ApiTransportError("POST old skill re-eval", true, new Error("lost")))
      .mockResolvedValueOnce("运行中")
      .mockResolvedValueOnce("通过");

    await expect(runPromptEvaluationSkillReEvalWithRecovery(
      "candidate-old", { asset_id: "asset-old" },
    )).rejects.toBeInstanceOf(ApiTransportError);
    await expect(runPromptEvaluationSkillReEvalWithRecovery(
      "candidate-new", { asset_id: "asset-new" },
    )).resolves.toBe("通过");
    expect(runPromptEvaluationSkillReEval.mock.calls[1]).toEqual([
      "candidate-old", { asset_id: "asset-old" }, runPromptEvaluationSkillReEval.mock.calls[0]?.[2],
    ]);
    expect(runPromptEvaluationSkillReEval.mock.calls[2]?.[0]).toBe("candidate-new");
  });
});
