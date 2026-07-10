import type { PromptEvaluationOptimizationCandidate, PromptEvaluationRun } from "@multica/core/types";

export type RunStatusFilter = "全部" | PromptEvaluationRun["status"];

export type EvidenceFocus = {
  traceSeq: string | null;
  toolChainId: string | null;
  trialAnchor: string | null;
  assertionAnchor: string | null;
  messageSeq: string | null;
  spanAnchor: string | null;
  failureAnchor: string | null;
};

export const RUN_STATUS_FILTERS: RunStatusFilter[] = [
  "全部",
  "已入队",
  "运行中",
  "通过",
  "未通过",
  "失败",
  "已取消",
  "需人工复核",
];

export function buildCandidatesByRun(
  candidates: PromptEvaluationOptimizationCandidate[],
): Map<string, PromptEvaluationOptimizationCandidate[]> {
  const result = new Map<string, PromptEvaluationOptimizationCandidate[]>();
  for (const candidate of candidates) {
    const bucket = result.get(candidate.run_id) ?? [];
    bucket.push(candidate);
    result.set(candidate.run_id, bucket);
  }
  return result;
}

export function canGenerateOptimizationCandidate(run: PromptEvaluationRun): boolean {
  if (!run.prompt_id) return false;
  if (run.failed_cases > 0) return true;
  if (run.status === "未通过" || run.status === "失败") return true;
  return Boolean(run.failure_reason && run.failure_reason !== "无");
}

export function canCancelPromptEvaluationRun(run: PromptEvaluationRun): boolean {
  return run.status === "已入队" || run.status === "运行中";
}

export function canReviewPromptEvaluationRun(run: PromptEvaluationRun): boolean {
  return run.status === "需人工复核";
}
