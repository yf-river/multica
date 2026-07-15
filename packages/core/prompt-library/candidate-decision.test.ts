// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import {
  publishPromptEvaluationOptimizationCandidateWithRecovery,
  rejectPromptEvaluationOptimizationCandidateWithRecovery,
} from "./candidate-decision";

let workspaceSequence = 0;

describe("candidate decision recovery", () => {
  beforeEach(async () => {
    localStorage.clear();
    workspaceSequence += 1;
    setCurrentWorkspace(`candidate-decision-${workspaceSequence}`, `workspace-${workspaceSequence}`);
    await Promise.resolve();
    await Promise.resolve();
  });

  it("replays a published candidate with one persisted key", async () => {
    const publishPromptEvaluationOptimizationCandidate = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST publish", true, new Error("lost")))
      .mockResolvedValueOnce("Prompt One");
    const client = {
      publishPromptEvaluationOptimizationCandidate,
      rejectPromptEvaluationOptimizationCandidate: vi.fn(),
    };

    await expect(publishPromptEvaluationOptimizationCandidateWithRecovery("candidate-1", client))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(publishPromptEvaluationOptimizationCandidateWithRecovery("candidate-1", client))
      .resolves.toBe("Prompt One");
    expect(publishPromptEvaluationOptimizationCandidate.mock.calls[1]?.[1])
      .toBe(publishPromptEvaluationOptimizationCandidate.mock.calls[0]?.[1]);
  });

  it("recovers an older reject before accepting a changed decision", async () => {
    const rejectPromptEvaluationOptimizationCandidate = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST old rejection", true, new Error("lost")))
      .mockResolvedValueOnce("已拒绝")
      .mockResolvedValueOnce("已拒绝");
    const client = {
      publishPromptEvaluationOptimizationCandidate: vi.fn(),
      rejectPromptEvaluationOptimizationCandidate,
    };

    await expect(rejectPromptEvaluationOptimizationCandidateWithRecovery(
      "candidate-old", "old reason", client,
    )).rejects.toBeInstanceOf(ApiTransportError);
    await expect(rejectPromptEvaluationOptimizationCandidateWithRecovery("candidate-new", "new reason", client))
      .resolves.toBe("已拒绝");
    expect(rejectPromptEvaluationOptimizationCandidate.mock.calls[1]).toEqual([
      "candidate-old", { reason: "old reason" },
      rejectPromptEvaluationOptimizationCandidate.mock.calls[0]?.[2],
    ]);
    expect(rejectPromptEvaluationOptimizationCandidate.mock.calls[2]?.[0]).toBe("candidate-new");
  });
});
