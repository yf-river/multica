// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import { runPromptEvaluationSkillReEvalWithRecovery } from "./skill-re-eval-run";

let workspaceSequence = 0;

describe("runPromptEvaluationSkillReEvalWithRecovery", () => {
  beforeEach(async () => {
    localStorage.clear();
    workspaceSequence += 1;
    setCurrentWorkspace(`skill-re-eval-run-${workspaceSequence}`, `workspace-${workspaceSequence}`);
    await Promise.resolve();
    await Promise.resolve();
  });

  it("replays a persisted unknown outcome with the same request identity", async () => {
    const runPromptEvaluationSkillReEval = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST skill re-eval", true, new Error("lost")))
      .mockResolvedValueOnce("已入队");
    const client = { runPromptEvaluationSkillReEval };
    const request = { asset_id: "asset-1" };

    await expect(runPromptEvaluationSkillReEvalWithRecovery("candidate-1", request, client))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(runPromptEvaluationSkillReEvalWithRecovery("candidate-1", request, client))
      .resolves.toBe("已入队");

    const firstKey = runPromptEvaluationSkillReEval.mock.calls[0]?.[2];
    expect(firstKey).toMatch(/^[0-9a-f-]{36}$/);
    expect(runPromptEvaluationSkillReEval.mock.calls[1]?.[2]).toBe(firstKey);
  });

  it("recovers an older operation before starting a changed candidate", async () => {
    const runPromptEvaluationSkillReEval = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST old skill re-eval", true, new Error("lost")))
      .mockResolvedValueOnce("运行中")
      .mockResolvedValueOnce("通过");
    const client = { runPromptEvaluationSkillReEval };

    await expect(runPromptEvaluationSkillReEvalWithRecovery(
      "candidate-old", { asset_id: "asset-old" }, client,
    )).rejects.toBeInstanceOf(ApiTransportError);
    await expect(runPromptEvaluationSkillReEvalWithRecovery(
      "candidate-new", { asset_id: "asset-new" }, client,
    )).resolves.toBe("通过");
    expect(runPromptEvaluationSkillReEval.mock.calls[1]).toEqual([
      "candidate-old", { asset_id: "asset-old" }, runPromptEvaluationSkillReEval.mock.calls[0]?.[2],
    ]);
    expect(runPromptEvaluationSkillReEval.mock.calls[2]?.[0]).toBe("candidate-new");
  });
});
