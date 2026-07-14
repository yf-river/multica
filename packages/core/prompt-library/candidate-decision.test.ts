// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { PromptEvaluationOptimizationCandidate } from "../types";
import {
  publishPromptEvaluationOptimizationCandidateWithRecovery,
  rejectPromptEvaluationOptimizationCandidateWithRecovery,
  useCandidateDecisionStore,
} from "./candidate-decision";

const candidate = (id: string, status: "已发布" | "已拒绝") => ({ id, status }) as PromptEvaluationOptimizationCandidate;

describe("candidate decision recovery", () => {
  beforeEach(async () => {
    localStorage.clear();
    setCurrentWorkspace("workspace-one", "workspace-1");
    await Promise.resolve();
    useCandidateDecisionStore.setState({ pending: undefined });
  });

  it("replays a published candidate with one persisted key", async () => {
    const publishPromptEvaluationOptimizationCandidate = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST publish", true, new Error("lost")))
      .mockResolvedValueOnce({ candidate: candidate("candidate-1", "已发布"), prompt: { id: "prompt-1" } });
    const client = {
      publishPromptEvaluationOptimizationCandidate,
      rejectPromptEvaluationOptimizationCandidate: vi.fn(),
    };

    await expect(publishPromptEvaluationOptimizationCandidateWithRecovery("candidate-1", client))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(publishPromptEvaluationOptimizationCandidateWithRecovery("candidate-1", client))
      .resolves.toMatchObject({ prompt: { id: "prompt-1" } });
    expect(publishPromptEvaluationOptimizationCandidate.mock.calls[1]?.[1])
      .toBe(publishPromptEvaluationOptimizationCandidate.mock.calls[0]?.[1]);
  });

  it("recovers an older reject before accepting a changed decision", async () => {
    useCandidateDecisionStore.getState().setPending({
      kind: "reject",
      candidateId: "candidate-old",
      reason: "old reason",
      requestKey: "10000000-0000-4000-8000-000000000018",
      createdAt: Date.now(),
    });
    const rejectPromptEvaluationOptimizationCandidate = vi.fn()
      .mockResolvedValueOnce(candidate("candidate-old", "已拒绝"))
      .mockResolvedValueOnce(candidate("candidate-new", "已拒绝"));
    const client = {
      publishPromptEvaluationOptimizationCandidate: vi.fn(),
      rejectPromptEvaluationOptimizationCandidate,
    };

    await expect(rejectPromptEvaluationOptimizationCandidateWithRecovery("candidate-new", "new reason", client))
      .resolves.toMatchObject({ id: "candidate-new", status: "已拒绝" });
    expect(rejectPromptEvaluationOptimizationCandidate.mock.calls[0]).toEqual([
      "candidate-old", { reason: "old reason" }, "10000000-0000-4000-8000-000000000018",
    ]);
    expect(rejectPromptEvaluationOptimizationCandidate.mock.calls[1]?.[0]).toBe("candidate-new");
  });
});
