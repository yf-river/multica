// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiTransportError } from "../api";
import { setCurrentWorkspace } from "../platform/workspace-storage";
import type { PromptEvaluationSkillReEvalRunResponse } from "../types";
import {
  runPromptEvaluationSkillReEvalWithRecovery,
  useSkillReEvalRunStore,
} from "./skill-re-eval-run";

const response = (id: string) => ({ run: { id } }) as PromptEvaluationSkillReEvalRunResponse;

describe("runPromptEvaluationSkillReEvalWithRecovery", () => {
  beforeEach(async () => {
    localStorage.clear();
    setCurrentWorkspace("workspace-one", "workspace-1");
    await Promise.resolve();
    useSkillReEvalRunStore.setState({ pending: undefined });
  });

  it("replays a persisted unknown outcome with the same request identity", async () => {
    const runPromptEvaluationSkillReEval = vi.fn()
      .mockRejectedValueOnce(new ApiTransportError("POST skill re-eval", true, new Error("lost")))
      .mockResolvedValueOnce(response("run-1"));
    const client = { runPromptEvaluationSkillReEval };
    const request = { asset_id: "asset-1" };

    await expect(runPromptEvaluationSkillReEvalWithRecovery("candidate-1", request, client))
      .rejects.toBeInstanceOf(ApiTransportError);
    await expect(runPromptEvaluationSkillReEvalWithRecovery("candidate-1", request, client))
      .resolves.toMatchObject({ run: { id: "run-1" } });

    const firstKey = runPromptEvaluationSkillReEval.mock.calls[0]?.[2];
    expect(firstKey).toMatch(/^[0-9a-f-]{36}$/);
    expect(runPromptEvaluationSkillReEval.mock.calls[1]?.[2]).toBe(firstKey);
    expect(useSkillReEvalRunStore.getState().pending).toBeUndefined();
  });

  it("recovers an older operation before starting a changed candidate", async () => {
    useSkillReEvalRunStore.getState().setPending({
      candidateId: "candidate-old",
      request: { asset_id: "asset-old" },
      requestKey: "10000000-0000-4000-8000-000000000012",
      createdAt: Date.now(),
    });
    const runPromptEvaluationSkillReEval = vi.fn()
      .mockResolvedValueOnce(response("run-old"))
      .mockResolvedValueOnce(response("run-new"));
    const client = { runPromptEvaluationSkillReEval };

    await expect(runPromptEvaluationSkillReEvalWithRecovery(
      "candidate-new", { asset_id: "asset-new" }, client,
    )).resolves.toMatchObject({ run: { id: "run-new" } });
    expect(runPromptEvaluationSkillReEval.mock.calls[0]).toEqual([
      "candidate-old", { asset_id: "asset-old" }, "10000000-0000-4000-8000-000000000012",
    ]);
    expect(runPromptEvaluationSkillReEval.mock.calls[1]?.[0]).toBe("candidate-new");
  });
});
