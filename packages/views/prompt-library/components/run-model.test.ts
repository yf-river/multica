import { describe, expect, it } from "vitest";
import type { PromptEvaluationOptimizationCandidate, PromptEvaluationRun } from "@multica/core/types";
import {
  buildCandidatesByRun,
  canCancelPromptEvaluationRun,
  canGenerateOptimizationCandidate,
  canReviewPromptEvaluationRun,
} from "./run-model";

function run(overrides: Partial<PromptEvaluationRun> = {}): PromptEvaluationRun {
  return {
    prompt_id: "prompt-1",
    failed_cases: 0,
    failure_reason: "",
    status: "通过",
    ...overrides,
  } as PromptEvaluationRun;
}

describe("prompt evaluation run model", () => {
  it("groups optimization candidates by their immutable run id", () => {
    const candidates = [
      { id: "candidate-1", run_id: "run-1" },
      { id: "candidate-2", run_id: "run-1" },
      { id: "candidate-3", run_id: "run-2" },
    ] as PromptEvaluationOptimizationCandidate[];

    const grouped = buildCandidatesByRun(candidates);
    expect(grouped.get("run-1")?.map((candidate) => candidate.id)).toEqual(["candidate-1", "candidate-2"]);
    expect(grouped.get("run-2")?.map((candidate) => candidate.id)).toEqual(["candidate-3"]);
  });

  it("derives available actions from the current run contract", () => {
    expect(canGenerateOptimizationCandidate(run({ failed_cases: 1 }))).toBe(true);
    expect(canGenerateOptimizationCandidate(run({ status: "失败" }))).toBe(true);
    expect(canGenerateOptimizationCandidate(run({ prompt_id: null, failed_cases: 1 }))).toBe(false);
    expect(canCancelPromptEvaluationRun(run({ status: "运行中" }))).toBe(true);
    expect(canCancelPromptEvaluationRun(run({ status: "通过" }))).toBe(false);
    expect(canReviewPromptEvaluationRun(run({ status: "需人工复核" }))).toBe(true);
  });
});
