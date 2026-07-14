// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { PromptEvaluationOptimizationCandidate } from "../types";
import {
  createPromptEvaluationOptimizationCandidateWithRecovery,
  useCandidateCreateStore,
} from "./candidate-create";

const candidate = (id: string) => ({ id }) as PromptEvaluationOptimizationCandidate;

describe("createPromptEvaluationOptimizationCandidateWithRecovery", () => {
  beforeEach(async () => {
    localStorage.clear();
    setCurrentWorkspace("workspace-one", "workspace-1");
    await Promise.resolve();
    useCandidateCreateStore.setState({ pending: undefined });
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
    expect(useCandidateCreateStore.getState().pending).toBeUndefined();
  });

  it("recovers an older run before creating a candidate for another run", async () => {
    useCandidateCreateStore.getState().setPending({
      runId: "run-old",
      requestKey: "10000000-0000-4000-8000-000000000016",
      createdAt: Date.now(),
    });
    const createPromptEvaluationOptimizationCandidate = vi.fn()
      .mockResolvedValueOnce(candidate("candidate-old"))
      .mockResolvedValueOnce(candidate("candidate-new"));
    const client = { createPromptEvaluationOptimizationCandidate };

    await expect(createPromptEvaluationOptimizationCandidateWithRecovery("run-new", client))
      .resolves.toMatchObject({ id: "candidate-new" });
    expect(createPromptEvaluationOptimizationCandidate.mock.calls[0]).toEqual([
      "run-old", "10000000-0000-4000-8000-000000000016",
    ]);
    expect(createPromptEvaluationOptimizationCandidate.mock.calls[1]?.[0]).toBe("run-new");
  });

  it("isolates pending candidate creation by workspace", async () => {
    const pending = {
      runId: "run-one",
      requestKey: "10000000-0000-4000-8000-000000000017",
      createdAt: Date.now(),
    };
    useCandidateCreateStore.getState().setPending(pending);
    setCurrentWorkspace("workspace-two", "workspace-2");
    await Promise.resolve();
    await Promise.resolve();
    expect(useCandidateCreateStore.getState().pending).toBeUndefined();
    setCurrentWorkspace("workspace-one", "workspace-1");
    await Promise.resolve();
    await Promise.resolve();
    expect(useCandidateCreateStore.getState().pending).toEqual(pending);
  });
});
