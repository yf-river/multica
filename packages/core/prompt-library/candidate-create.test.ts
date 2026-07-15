// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import { createPromptEvaluationOptimizationCandidateWithRecovery } from "./candidate-create";

const candidate = (id: string) => ({ id });
let workspaceSequence = 0;

describe("createPromptEvaluationOptimizationCandidateWithRecovery", () => {
  beforeEach(async () => {
    localStorage.clear();
    workspaceSequence += 1;
    setCurrentWorkspace(`candidate-create-${workspaceSequence}`, `workspace-${workspaceSequence}`);
    await Promise.resolve();
    await Promise.resolve();
  });

  it("replays the same run and key after an unknown outcome", async () => {
    const createPromptEvaluationOptimizationCandidate = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST candidate", true, new Error("lost")))
      .mockResolvedValueOnce(candidate("candidate-1"));
    const client = { createPromptEvaluationOptimizationCandidate };

    await expect(createPromptEvaluationOptimizationCandidateWithRecovery("run-1", client))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(createPromptEvaluationOptimizationCandidateWithRecovery("run-1", client))
      .resolves.toMatchObject({ id: "candidate-1" });

    const firstKey = createPromptEvaluationOptimizationCandidate.mock.calls[0]?.[1];
    expect(firstKey).toMatch(/^[0-9a-f-]{36}$/);
    expect(createPromptEvaluationOptimizationCandidate.mock.calls[1]?.[1]).toBe(firstKey);
  });

  it("recovers an older run before creating a candidate for another run", async () => {
    const createPromptEvaluationOptimizationCandidate = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST old candidate", true, new Error("lost")))
      .mockResolvedValueOnce(candidate("candidate-old"))
      .mockResolvedValueOnce(candidate("candidate-new"));
    const client = { createPromptEvaluationOptimizationCandidate };

    await expect(createPromptEvaluationOptimizationCandidateWithRecovery("run-old", client))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(createPromptEvaluationOptimizationCandidateWithRecovery("run-new", client))
      .resolves.toMatchObject({ id: "candidate-new" });
    expect(createPromptEvaluationOptimizationCandidate.mock.calls[1]).toEqual([
      "run-old", createPromptEvaluationOptimizationCandidate.mock.calls[0]?.[1],
    ]);
    expect(createPromptEvaluationOptimizationCandidate.mock.calls[2]?.[0]).toBe("run-new");
  });

  it("isolates pending candidate creation by workspace", async () => {
    const workspaceOneClient = {
      createPromptEvaluationOptimizationCandidate: vi.fn()
        .mockRejectedValueOnce(new ApiTransportError("POST workspace one", true, new Error("lost")))
        .mockResolvedValueOnce(candidate("candidate-one")),
    };
    await expect(createPromptEvaluationOptimizationCandidateWithRecovery("run-one", workspaceOneClient))
      .rejects.toBeInstanceOf(ApiTransportError);
    const firstKey = workspaceOneClient.createPromptEvaluationOptimizationCandidate.mock.calls[0]?.[1];

    setCurrentWorkspace("candidate-create-other", "workspace-other");
    await Promise.resolve();
    await Promise.resolve();
    const workspaceTwoClient = {
      createPromptEvaluationOptimizationCandidate: vi.fn().mockResolvedValue(candidate("candidate-two")),
    };
    await expect(createPromptEvaluationOptimizationCandidateWithRecovery("run-two", workspaceTwoClient))
      .resolves.toMatchObject({ id: "candidate-two" });
    expect(workspaceTwoClient.createPromptEvaluationOptimizationCandidate.mock.calls[0]?.[0]).toBe("run-two");

    setCurrentWorkspace(`candidate-create-${workspaceSequence}`, `workspace-${workspaceSequence}`);
    await Promise.resolve();
    await Promise.resolve();
    await expect(createPromptEvaluationOptimizationCandidateWithRecovery("run-one", workspaceOneClient))
      .resolves.toMatchObject({ id: "candidate-one" });
    expect(workspaceOneClient.createPromptEvaluationOptimizationCandidate.mock.calls[1]).toEqual([
      "run-one", firstKey,
    ]);
  });
});
