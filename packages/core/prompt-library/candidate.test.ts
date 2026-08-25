// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiClient, ApiTransportError, getApi, setApiInstance } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import {
  createPromptEvaluationOptimizationCandidateWithRecovery,
  publishPromptEvaluationOptimizationCandidateWithRecovery,
  rejectPromptEvaluationOptimizationCandidateWithRecovery,
} from "./candidate";

const candidate = (id: string) => ({ id });
let workspaceSequence = 0;

beforeEach(async () => {
  vi.restoreAllMocks();
  setApiInstance(new ApiClient("http://core.test"));
  localStorage.clear();
  workspaceSequence += 1;
  setCurrentWorkspace(`candidate-${workspaceSequence}`, `workspace-${workspaceSequence}`);
  await Promise.resolve();
  await Promise.resolve();
});

describe("createPromptEvaluationOptimizationCandidateWithRecovery", () => {
  it("replays the same run and key after an unknown outcome", async () => {
    const createPromptEvaluationOptimizationCandidate = vi.spyOn(getApi(), "createPromptEvaluationOptimizationCandidate")
      .mockRejectedValueOnce(new ApiTransportError("POST candidate", true, new Error("lost")))
      .mockResolvedValueOnce(candidate("candidate-1"));
    await expect(createPromptEvaluationOptimizationCandidateWithRecovery("run-1"))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(createPromptEvaluationOptimizationCandidateWithRecovery("run-1"))
      .resolves.toMatchObject({ id: "candidate-1" });

    const firstKey = createPromptEvaluationOptimizationCandidate.mock.calls[0]?.[1];
    expect(firstKey).toMatch(/^[0-9a-f-]{36}$/);
    expect(createPromptEvaluationOptimizationCandidate.mock.calls[1]?.[1]).toBe(firstKey);
  });

  it("recovers an older run before creating a candidate for another run", async () => {
    const createPromptEvaluationOptimizationCandidate = vi.spyOn(getApi(), "createPromptEvaluationOptimizationCandidate")
      .mockRejectedValueOnce(new ApiTransportError("POST old candidate", true, new Error("lost")))
      .mockResolvedValueOnce(candidate("candidate-old"))
      .mockResolvedValueOnce(candidate("candidate-new"));
    await expect(createPromptEvaluationOptimizationCandidateWithRecovery("run-old"))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(createPromptEvaluationOptimizationCandidateWithRecovery("run-new"))
      .resolves.toMatchObject({ id: "candidate-new" });
    expect(createPromptEvaluationOptimizationCandidate.mock.calls[1]).toEqual([
      "run-old", createPromptEvaluationOptimizationCandidate.mock.calls[0]?.[1],
    ]);
    expect(createPromptEvaluationOptimizationCandidate.mock.calls[2]?.[0]).toBe("run-new");
  });

  it("isolates pending candidate creation by workspace", async () => {
    const createPromptEvaluationOptimizationCandidate = vi.spyOn(getApi(), "createPromptEvaluationOptimizationCandidate")
      .mockRejectedValueOnce(new ApiTransportError("POST workspace one", true, new Error("lost")))
      .mockResolvedValueOnce(candidate("candidate-two"))
      .mockResolvedValueOnce(candidate("candidate-one"));
    await expect(createPromptEvaluationOptimizationCandidateWithRecovery("run-one"))
      .rejects.toBeInstanceOf(ApiTransportError);
    const firstKey = createPromptEvaluationOptimizationCandidate.mock.calls[0]?.[1];

    setCurrentWorkspace("candidate-other", "workspace-other");
    await Promise.resolve();
    await Promise.resolve();
    await expect(createPromptEvaluationOptimizationCandidateWithRecovery("run-two"))
      .resolves.toMatchObject({ id: "candidate-two" });
    expect(createPromptEvaluationOptimizationCandidate.mock.calls[1]?.[0]).toBe("run-two");

    setCurrentWorkspace(`candidate-${workspaceSequence}`, `workspace-${workspaceSequence}`);
    await Promise.resolve();
    await Promise.resolve();
    await expect(createPromptEvaluationOptimizationCandidateWithRecovery("run-one"))
      .resolves.toMatchObject({ id: "candidate-one" });
    expect(createPromptEvaluationOptimizationCandidate.mock.calls[2]).toEqual([
      "run-one", firstKey,
    ]);
  });
});

describe("candidate decision recovery", () => {
  it("replays a published candidate with one persisted key", async () => {
    const publishPromptEvaluationOptimizationCandidate = vi.spyOn(getApi(), "publishPromptEvaluationOptimizationCandidate")
      .mockRejectedValueOnce(new ApiTransportError("POST publish", true, new Error("lost")))
      .mockResolvedValueOnce("Prompt One");
    await expect(publishPromptEvaluationOptimizationCandidateWithRecovery("candidate-1"))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(publishPromptEvaluationOptimizationCandidateWithRecovery("candidate-1"))
      .resolves.toBe("Prompt One");
    expect(publishPromptEvaluationOptimizationCandidate.mock.calls[1]?.[1])
      .toBe(publishPromptEvaluationOptimizationCandidate.mock.calls[0]?.[1]);
  });

  it("recovers an older reject before accepting a changed decision", async () => {
    const rejectPromptEvaluationOptimizationCandidate = vi.spyOn(getApi(), "rejectPromptEvaluationOptimizationCandidate")
      .mockRejectedValueOnce(new ApiTransportError("POST old rejection", true, new Error("lost")))
      .mockResolvedValueOnce("已拒绝")
      .mockResolvedValueOnce("已拒绝");
    await expect(rejectPromptEvaluationOptimizationCandidateWithRecovery(
      "candidate-old", "old reason",
    )).rejects.toBeInstanceOf(ApiTransportError);
    await expect(rejectPromptEvaluationOptimizationCandidateWithRecovery("candidate-new", "new reason"))
      .resolves.toBe("已拒绝");
    expect(rejectPromptEvaluationOptimizationCandidate.mock.calls[1]).toEqual([
      "candidate-old", { reason: "old reason" },
      rejectPromptEvaluationOptimizationCandidate.mock.calls[0]?.[2],
    ]);
    expect(rejectPromptEvaluationOptimizationCandidate.mock.calls[2]?.[0]).toBe("candidate-new");
  });
});
