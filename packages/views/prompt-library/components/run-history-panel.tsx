import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { CheckCircle, Loader2, Play, Plus, XCircle } from "lucide-react";
import { api } from "@multica/core/api";
import type { PromptEvaluationOptimizationCandidate, PromptEvaluationRun } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n/use-t";
import { promptLibraryKeys } from "./prompt-library-query-keys";
import { RunEvidencePanel } from "./run-evidence-panel";
import {
  buildCandidatesByRun,
  canCancelPromptEvaluationRun,
  canGenerateOptimizationCandidate,
  canReviewPromptEvaluationRun,
  type EvidenceFocus,
  RUN_STATUS_FILTERS,
  type RunStatusFilter,
} from "./run-model";
import type { SkillResourceOption } from "./skill-candidate-model";

export function RunHistoryPanel({
  workspaceId,
  runs,
  focusedRunId,
  evidenceFocus,
  runStatusFilter,
  onRunStatusFilterChange,
  candidates,
  skillResources,
  loading,
  onSyncRun,
  syncingRunId,
  onCancelRun,
  cancellingRunId,
  onReviewRun,
  reviewingRunId,
  onCreateEvidenceSnapshot,
  creatingEvidenceSnapshotRunId,
  onGenerateCandidate,
  generatingCandidateRunId,
}: {
  workspaceId: string;
  runs: PromptEvaluationRun[];
  focusedRunId: string | null;
  evidenceFocus: EvidenceFocus;
  runStatusFilter: RunStatusFilter;
  onRunStatusFilterChange: (status: RunStatusFilter) => void;
  candidates: PromptEvaluationOptimizationCandidate[];
  skillResources: SkillResourceOption[];
  loading: boolean;
  onSyncRun: (runId: string) => void;
  syncingRunId: string | null;
  onCancelRun: (runId: string) => void;
  cancellingRunId: string | null;
  onReviewRun: (run: PromptEvaluationRun, decision: "通过" | "未通过") => void;
  reviewingRunId: string | null;
  onCreateEvidenceSnapshot: (runId: string) => void;
  creatingEvidenceSnapshotRunId: string | null;
  onGenerateCandidate: (runId: string) => void;
  generatingCandidateRunId: string | null;
}) {
  const { t } = useT("prompt-library");
  const candidatesByRun = useMemo(() => buildCandidatesByRun(candidates), [candidates]);
  const [expandedRunId, setExpandedRunId] = useState<string | null>(null);

  useEffect(() => {
    if (!focusedRunId || !runs.some((run) => run.id === focusedRunId)) return;
    setExpandedRunId(focusedRunId);
    window.requestAnimationFrame(() => {
      document.querySelector(`[data-testid="prompt-evaluation-run-${focusedRunId}"]`)?.scrollIntoView({ block: "center" });
    });
  }, [focusedRunId, runs]);

  const evidenceQuery = useQuery({
    queryKey: promptLibraryKeys.runEvidence(workspaceId, expandedRunId),
    queryFn: () => api.getPromptEvaluationRunEvidence(expandedRunId ?? ""),
    enabled: !!expandedRunId,
  });
  const evidenceSnapshotQuery = useQuery({
    queryKey: promptLibraryKeys.runEvidenceSnapshots(workspaceId, expandedRunId),
    queryFn: () => api.listPromptEvaluationEvidenceSnapshots(expandedRunId ?? "", 5),
    enabled: !!expandedRunId,
  });

  return (
    <section
      className="grid gap-3"
      aria-label={t(($) => $.run_history.aria_label)}
      data-testid="training-route-panel-run-history"
    >
      {loading ? (
        <div className="h-20 rounded-md bg-muted/60" />
      ) : runs.length === 0 ? (
        <div className="grid gap-3">
          <RunStatusFilterBar value={runStatusFilter} onChange={onRunStatusFilterChange} />
          <div
            className="rounded-md border border-dashed px-3 py-6 text-center text-sm text-muted-foreground"
            data-testid="training-route-empty-run-history"
          >
            {runStatusFilter === "全部"
              ? t(($) => $.run_history.empty_all)
              : t(($) => $.run_history.empty_status, { status: runStatusFilter })}
          </div>
        </div>
      ) : (
        <div className="grid gap-3">
          <RunStatusFilterBar value={runStatusFilter} onChange={onRunStatusFilterChange} />
          <div className="divide-y rounded-md border" data-testid="prompt-evaluation-run-list">
            {runs.map((run) => {
              const hasPendingCandidate =
                candidatesByRun.get(run.id)?.some((candidate) => candidate.status === "待确认") ?? false;
              const runKind = run.run_kind === "Agent执行" ? t(($) => $.run_history.agent_run) : run.run_kind;
              return (
                <div
                  key={run.id}
                  data-testid={`prompt-evaluation-run-${run.id}`}
                  className="grid gap-2 px-3 py-3 md:grid-cols-[minmax(0,1fr)_auto]"
                >
                  <div className="min-w-0">
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="truncate text-sm font-medium">
                        {t(($) => $.run_history.kind_status, { kind: runKind, status: run.status })}
                      </span>
                      <Badge
                        variant={
                          run.status === "通过"
                            ? "secondary"
                            : run.status === "已入队" || run.status === "运行中"
                              ? "outline"
                              : "destructive"
                        }
                        className="shrink-0"
                      >
                        {t(($) => $.run_history.pass_rate, {
                          count: run.total_cases,
                          rate: Math.round(run.pass_rate * 100),
                        })}
                      </Badge>
                    </div>
                    <div className="mt-1 text-xs text-muted-foreground">
                      {t(($) => $.run_history.summary, {
                        model: run.model || t(($) => $.run_history.not_recorded),
                        runtime: run.runtime_provider || t(($) => $.run_history.not_recorded),
                        passed: run.passed_cases,
                        total: run.total_cases,
                        input: run.input_tokens,
                        output: run.output_tokens,
                      })}
                      {run.failure_reason
                        ? t(($) => $.run_history.failure_suffix, { reason: run.failure_reason })
                        : ""}
                      {run.conclusion ? t(($) => $.run_history.conclusion_suffix, { conclusion: run.conclusion }) : ""}
                    </div>
                    {run.review_decision && (
                      <div className="mt-1 text-xs text-muted-foreground">
                        {t(($) => $.run_history.review, {
                          decision: run.review_decision,
                          note: run.review_note ? ` · ${run.review_note}` : "",
                          time: run.reviewed_at ? ` · ${run.reviewed_at}` : "",
                        })}
                      </div>
                    )}
                    <div className="mt-1 break-all text-[11px] text-muted-foreground">
                      {t(($) => $.run_history.run_task, {
                        run: run.id,
                        task: run.task_id ? ` · ${t(($) => $.run_history.task, { id: run.task_id })}` : "",
                      })}
                    </div>
                  </div>
                  <div className="flex items-center justify-end gap-2 text-right text-[11px] text-muted-foreground">
                    <div>
                      <div>{run.created_at}</div>
                      <div>{t(($) => $.run_history.duration_ms, { value: run.total_duration_ms })}</div>
                    </div>
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() => setExpandedRunId(expandedRunId === run.id ? null : run.id)}
                    >
                      {expandedRunId === run.id
                        ? t(($) => $.run_history.collapse_evidence)
                        : t(($) => $.run_history.show_evidence)}
                    </Button>
                    {run.task_id && (
                      <Button size="sm" variant="secondary" onClick={() => onSyncRun(run.id)} disabled={syncingRunId === run.id}>
                        {syncingRunId === run.id ? (
                          <Loader2 className="size-3.5 animate-spin" />
                        ) : (
                          <Play className="size-3.5" />
                        )}
                        {t(($) => $.run_history.sync_task)}
                      </Button>
                    )}
                    {canCancelPromptEvaluationRun(run) && (
                      <Button
                        size="sm"
                        variant="destructive"
                        onClick={() => onCancelRun(run.id)}
                        disabled={cancellingRunId === run.id}
                        data-testid={`cancel-prompt-evaluation-run-${run.id}`}
                      >
                        {cancellingRunId === run.id ? (
                          <Loader2 className="size-3.5 animate-spin" />
                        ) : (
                          <XCircle className="size-3.5" />
                        )}
                        {t(($) => $.run_history.cancel)}
                      </Button>
                    )}
                    {canReviewPromptEvaluationRun(run) && (
                      <>
                        <Button
                          size="sm"
                          variant="secondary"
                          onClick={() => onReviewRun(run, "通过")}
                          disabled={reviewingRunId === run.id}
                          data-testid={`review-prompt-evaluation-run-pass-${run.id}`}
                        >
                          {reviewingRunId === run.id ? (
                            <Loader2 className="size-3.5 animate-spin" />
                          ) : (
                            <CheckCircle className="size-3.5" />
                          )}
                          {t(($) => $.run_history.review_pass)}
                        </Button>
                        <Button
                          size="sm"
                          variant="destructive"
                          onClick={() => onReviewRun(run, "未通过")}
                          disabled={reviewingRunId === run.id}
                          data-testid={`review-prompt-evaluation-run-fail-${run.id}`}
                        >
                          {reviewingRunId === run.id ? (
                            <Loader2 className="size-3.5 animate-spin" />
                          ) : (
                            <XCircle className="size-3.5" />
                          )}
                          {t(($) => $.run_history.review_fail)}
                        </Button>
                      </>
                    )}
                    {canGenerateOptimizationCandidate(run) && (
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() => onGenerateCandidate(run.id)}
                        disabled={generatingCandidateRunId === run.id || hasPendingCandidate}
                      >
                        {generatingCandidateRunId === run.id ? (
                          <Loader2 className="size-3.5 animate-spin" />
                        ) : (
                          <Plus className="size-3.5" />
                        )}
                        {hasPendingCandidate
                          ? t(($) => $.run_history.candidate_exists)
                          : t(($) => $.run_history.generate_candidate)}
                      </Button>
                    )}
                  </div>
                  {expandedRunId === run.id && (
                    <RunEvidencePanel
                      evidence={evidenceQuery.data ?? null}
                      snapshots={evidenceSnapshotQuery.data?.items ?? []}
                      snapshotsLoading={evidenceSnapshotQuery.isLoading || evidenceSnapshotQuery.isFetching}
                      loading={evidenceQuery.isLoading || evidenceQuery.isFetching}
                      error={evidenceQuery.isError}
                      skillResources={skillResources}
                      evidenceFocus={evidenceFocus}
                      optimizationActions={{
                        canGenerate: canGenerateOptimizationCandidate(run),
                        hasPendingCandidate,
                        generatingCandidate: generatingCandidateRunId === run.id,
                        onGenerateCandidate: () => onGenerateCandidate(run.id),
                      }}
                      creatingSnapshot={creatingEvidenceSnapshotRunId === run.id}
                      onCreateSnapshot={() => onCreateEvidenceSnapshot(run.id)}
                    />
                  )}
                </div>
              );
            })}
          </div>
        </div>
      )}
    </section>
  );
}

function RunStatusFilterBar({ value, onChange }: { value: RunStatusFilter; onChange: (status: RunStatusFilter) => void }) {
  const { t } = useT("prompt-library");
  return (
    <div
      className="flex flex-wrap items-center gap-2 rounded-md border border-border/70 bg-muted/10 px-3 py-2"
      role="group"
      aria-label={t(($) => $.run_history.filter_aria)}
      data-testid="run-status-filter-bar"
    >
      <span className="text-xs font-medium text-muted-foreground">{t(($) => $.run_history.filter_label)}</span>
      {RUN_STATUS_FILTERS.map((status) => (
        <button
          key={status}
          type="button"
          className={`inline-flex h-7 items-center rounded-md border px-2.5 text-xs transition-colors ${
            value === status
              ? "border-foreground bg-foreground text-background"
              : "border-border bg-background text-muted-foreground hover:text-foreground"
          }`}
          data-active={value === status ? "true" : undefined}
          onClick={() => onChange(status)}
        >
          {status === "需人工复核" ? t(($) => $.run_history.manual_review_queue) : status}
        </button>
      ))}
    </div>
  );
}
