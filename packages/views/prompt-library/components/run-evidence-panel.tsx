import { useQuery } from "@tanstack/react-query";
import { Archive, Loader2, Plus, RefreshCw } from "lucide-react";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import type {
  PromptEvaluationEvidenceSnapshot,
  PromptEvaluationRunEvidence,
} from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n/use-t";
import { asRecord, stringFromUnknown } from "./record-utils";
import { promptLibraryKeys } from "./prompt-library-query-keys";
import type { EvidenceFocus } from "./run-model";
import {
  candidateSkillWorkflowEvidence,
  type SkillResourceOption,
} from "./skill-candidate-model";
import { SkillCandidateWorkflowPanel } from "./skill-candidate-workflow";
import { useSkillCandidateWorkflowActions } from "./use-skill-candidate-workflow-actions";

export type RunOptimizationActions = {
  canGenerate: boolean;
  hasPendingCandidate: boolean;
  generatingCandidate: boolean;
  onGenerateCandidate: () => void;
};

export function RunEvidencePanel({
  evidence,
  snapshots,
  snapshotsLoading,
  loading,
  error,
  skillResources,
  evidenceFocus,
  optimizationActions,
  creatingSnapshot,
  onCreateSnapshot,
}: {
  evidence: PromptEvaluationRunEvidence | null;
  snapshots: PromptEvaluationEvidenceSnapshot[];
  snapshotsLoading: boolean;
  loading: boolean;
  error: boolean;
  skillResources: SkillResourceOption[];
  evidenceFocus?: EvidenceFocus;
  optimizationActions?: RunOptimizationActions;
  creatingSnapshot: boolean;
  onCreateSnapshot: () => void;
}) {
  const { t } = useT("prompt-library");
  const workspaceId = useWorkspaceId();
  const run = evidence?.run ?? null;
  const runId = run?.id ?? "";
  const {
    setDrafts: setSkillDrafts,
    activeAction: skillAction,
    runAction: runSkillWorkflowAction,
    draftFor: skillDraftFor,
  } = useSkillCandidateWorkflowActions(workspaceId ?? "", runId);
  const candidatesQuery = useQuery({
    queryKey: promptLibraryKeys.runCandidates(workspaceId ?? "", runId),
    queryFn: () => api.listPromptEvaluationOptimizationCandidates({ run_id: runId, limit: 5 }),
    enabled: Boolean(workspaceId && runId),
  });
  const candidates = candidatesQuery.data?.items ?? [];
  const candidate = candidates[0] ?? null;

  if (loading) {
    return (
      <div className="rounded-md border border-dashed px-3 py-4 text-sm text-muted-foreground md:col-span-2">
        {t(($) => $.run_evidence.loading)}
      </div>
    );
  }
  if (error || !evidence || !run) {
    return (
      <div className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-4 text-sm text-destructive md:col-span-2">
        {t(($) => $.run_evidence.load_failed)}
      </div>
    );
  }

  const totalTokens = Number(run.input_tokens ?? 0) + Number(run.output_tokens ?? 0);
  const focusLabels = [
    evidenceFocus?.traceSeq ? t(($) => $.run_evidence.focus.trace, { id: evidenceFocus.traceSeq }) : "",
    evidenceFocus?.toolChainId ? t(($) => $.run_evidence.focus.tool, { id: evidenceFocus.toolChainId }) : "",
    evidenceFocus?.trialAnchor ? t(($) => $.run_evidence.focus.trial, { id: evidenceFocus.trialAnchor }) : "",
    evidenceFocus?.assertionAnchor ? t(($) => $.run_evidence.focus.assertion, { id: evidenceFocus.assertionAnchor }) : "",
    evidenceFocus?.messageSeq ? t(($) => $.run_evidence.focus.message, { id: evidenceFocus.messageSeq }) : "",
    evidenceFocus?.spanAnchor ? t(($) => $.run_evidence.focus.span, { id: evidenceFocus.spanAnchor }) : "",
    evidenceFocus?.failureAnchor ? t(($) => $.run_evidence.focus.failure, { id: evidenceFocus.failureAnchor }) : "",
  ].filter(Boolean);
  const rawPayload = {
    run: evidence.run,
    trials: evidence.trials,
    task_usage: evidence.task_usage,
    task_messages: evidence.task_messages,
    trace_events: evidence.trace_events,
    execution_spans: evidence.execution_spans,
    tool_call_chains: evidence.tool_call_chains,
    tool_call_summary: evidence.tool_call_summary,
    execution_summary: evidence.execution_summary,
    evidence: evidence.evidence,
    context: evidence.上下文,
  };

  return (
    <section className="grid gap-3 rounded-md border bg-muted/10 p-3 md:col-span-2" data-testid="run-evidence-panel">
      <div className="grid gap-2 md:grid-cols-[minmax(0,1fr)_auto]">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium text-foreground">{t(($) => $.run_evidence.title)}</span>
            <Badge variant={run.status === "通过" ? "secondary" : run.failed_cases > 0 ? "destructive" : "outline"}>
              {run.status}
            </Badge>
            <Badge variant={totalTokens > 0 ? "secondary" : "outline"}>
              {t(($) => $.run_evidence.tokens, { value: formatNumber(totalTokens) })}
            </Badge>
            <Badge variant={snapshots.length > 0 ? "secondary" : "outline"}>
              {snapshotsLoading
                ? t(($) => $.run_evidence.snapshots_loading)
                : t(($) => $.run_evidence.snapshot_count, { count: snapshots.length })}
            </Badge>
            {focusLabels.map((label) => (
              <Badge key={label} variant="outline">
                {label}
              </Badge>
            ))}
          </div>
          <div className="mt-1 break-all text-[11px] leading-5 text-muted-foreground">
            {t(($) => $.run_evidence.run_metadata, {
              run: run.id,
              task: run.task_id || t(($) => $.run_evidence.unbound),
              model: run.model || t(($) => $.run_evidence.not_recorded),
              runtime: run.runtime_provider || t(($) => $.run_evidence.not_recorded),
            })}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2 md:justify-end">
          {optimizationActions?.canGenerate && (
            <Button
              size="sm"
              variant="secondary"
              onClick={optimizationActions.onGenerateCandidate}
              disabled={optimizationActions.generatingCandidate || optimizationActions.hasPendingCandidate}
            >
              {optimizationActions.generatingCandidate ? (
                <Loader2 className="size-3.5 animate-spin" />
              ) : (
                <Plus className="size-3.5" />
              )}
              {optimizationActions.hasPendingCandidate
                ? t(($) => $.run_evidence.candidate_exists)
                : t(($) => $.run_evidence.generate_candidate)}
            </Button>
          )}
          <Button size="sm" variant="secondary" onClick={() => candidatesQuery.refetch()} disabled={candidatesQuery.isFetching}>
            {candidatesQuery.isFetching ? <Loader2 className="size-3.5 animate-spin" /> : <RefreshCw className="size-3.5" />}
            {t(($) => $.run_evidence.refresh_candidates)}
          </Button>
          <Button size="sm" variant="secondary" onClick={onCreateSnapshot} disabled={creatingSnapshot}>
            {creatingSnapshot ? <Loader2 className="size-3.5 animate-spin" /> : <Archive className="size-3.5" />}
            {t(($) => $.run_evidence.archive_snapshot)}
          </Button>
        </div>
      </div>

      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
        <EvidenceMetric
          label={t(($) => $.run_evidence.metrics.cases)}
          value={`${formatNumber(run.passed_cases)}/${formatNumber(run.total_cases)}`}
        />
        <EvidenceMetric label={t(($) => $.run_evidence.metrics.duration)} value={formatDuration(run.total_duration_ms, t(($) => $.run_evidence.units.second), t(($) => $.run_evidence.units.minute))} />
        <EvidenceMetric label={t(($) => $.run_evidence.metrics.cost)} value={formatMoney(run.estimated_cost)} />
        <EvidenceMetric
          label={t(($) => $.run_evidence.metrics.evidence)}
          value={t(($) => $.run_evidence.metrics.evidence_value, {
            trials: evidence.trials.length,
            messages: evidence.task_messages.length,
            traces: evidence.trace_events.length,
          })}
        />
      </div>

      <div className="grid gap-2 rounded border bg-background px-3 py-2 text-xs" data-testid="run-evidence-snapshots">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium text-foreground">{t(($) => $.run_evidence.snapshots_title)}</span>
          <Badge variant={snapshots.length > 0 ? "secondary" : "outline"}>
            {snapshotsLoading
              ? t(($) => $.run_evidence.reading)
              : t(($) => $.run_evidence.snapshot_count_short, { count: snapshots.length })}
          </Badge>
        </div>
        <div className="break-all text-muted-foreground">
          {t(($) => $.run_evidence.run_task, { run: run.id, task: run.task_id || t(($) => $.run_evidence.unbound) })}
        </div>
        {snapshotsLoading ? (
          <div className="text-muted-foreground">{t(($) => $.run_evidence.reading_snapshots)}</div>
        ) : snapshots.length === 0 ? (
          <div className="text-muted-foreground">{t(($) => $.run_evidence.no_snapshots)}</div>
        ) : (
          <div className="grid gap-1.5">
            {snapshots.map((snapshot) => (
              <div key={snapshot.id} className="grid gap-1 rounded border bg-muted/10 px-2 py-1.5">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge variant="outline">{snapshot.snapshot_type}</Badge>
                  <span className="break-all font-mono text-[11px] text-muted-foreground">{snapshot.id}</span>
                  <span className="text-muted-foreground">{snapshot.created_at}</span>
                </div>
                <div className="break-all text-muted-foreground">
                  {snapshotSummary(snapshot, t(($) => $.run_evidence.recorded))}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {candidate && (
        <div className="grid gap-2 rounded border bg-background px-2 py-2 text-xs" data-testid="run-evidence-candidate">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{candidate.candidate_name}</span>
            <Badge variant={candidate.status === "待确认" ? "secondary" : "outline"}>{candidate.status}</Badge>
            <Badge variant="outline">
              {t(($) => $.run_evidence.failed_cases, { count: candidate.failed_case_count })}
            </Badge>
          </div>
          <div className="text-muted-foreground">
            {candidate.rationale || t(($) => $.run_evidence.candidate_rationale)}
          </div>
          <SkillCandidateWorkflowPanel
            candidate={candidate}
            draft={skillDraftFor(candidate)}
            evidence={candidateSkillWorkflowEvidence(candidate)}
            resources={skillResources}
            pendingAction={skillAction?.candidateId === candidate.id ? skillAction.action : null}
            disabled={candidate.status !== "待确认"}
            onDraftChange={(next) => setSkillDrafts((current) => ({ ...current, [candidate.id]: next }))}
            onRunAction={(action) => void runSkillWorkflowAction(candidate, action)}
          />
        </div>
      )}

      <details className="rounded border bg-background px-3 py-2 text-xs" open={focusLabels.length > 0}>
        <summary className="cursor-pointer font-medium text-muted-foreground">
          {t(($) => $.run_evidence.raw_json)}
        </summary>
        <pre className="mt-2 max-h-96 overflow-auto whitespace-pre-wrap font-mono text-[11px] leading-5">
          {JSON.stringify(rawPayload, null, 2)}
        </pre>
      </details>
    </section>
  );
}

function EvidenceMetric({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0 rounded border bg-background px-2 py-1.5 text-xs">
      <div className="truncate text-[11px] text-muted-foreground">{label}</div>
      <div className="mt-0.5 truncate font-semibold">{value}</div>
    </div>
  );
}

function snapshotSummary(snapshot: PromptEvaluationEvidenceSnapshot, recordedLabel: string): string {
  const summary = asRecord(snapshot.summary);
  const candidates = [
    stringFromUnknown(summary["说明"]),
    stringFromUnknown(summary["结论"]),
    stringFromUnknown(summary["summary"]),
    stringFromUnknown(summary["status"]),
  ].filter(Boolean);
  if (candidates.length > 0) return candidates.join(" · ");
  const keys = Object.keys(summary).slice(0, 4);
  if (keys.length === 0) return `run ${snapshot.run_id}`;
  return keys.map((key) => `${key}: ${stringFromUnknown(summary[key]) || recordedLabel}`).join(" · ");
}

function formatNumber(value: unknown): string {
  return typeof value === "number" && Number.isFinite(value) ? value.toLocaleString("zh-CN") : "0";
}

function formatMoney(value: unknown): string {
  const amount = typeof value === "number" ? value : Number(value ?? 0);
  if (!Number.isFinite(amount) || amount <= 0) return "$0.00";
  return amount < 0.01 ? `$${amount.toFixed(6)}` : `$${amount.toFixed(2)}`;
}

function formatDuration(value: unknown, secondLabel: string, minuteLabel: string): string {
  const milliseconds = typeof value === "number" ? value : Number(value ?? 0);
  if (!Number.isFinite(milliseconds) || milliseconds <= 0) return "0 ms";
  if (milliseconds < 1000) return `${Math.round(milliseconds)} ms`;
  const seconds = milliseconds / 1000;
  if (seconds < 60) return `${Math.round(seconds * 10) / 10}${secondLabel}`;
  return `${Math.round((seconds / 60) * 10) / 10}${minuteLabel}`;
}
