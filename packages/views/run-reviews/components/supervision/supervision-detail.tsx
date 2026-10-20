"use client";

import { useId, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { RotateCw } from "lucide-react";
import { api } from "@multica/core/api";
import { createSafeId } from "@multica/core/utils";
import { useWorkspaceId, useWorkspacePaths } from "@multica/core/paths";
import { supervisionDetailOptions, supervisionKeys } from "@multica/core/supervision/queries";
import type { SupervisionFinding } from "@multica/core/supervision/schema";
import { taskMessagesOptions } from "@multica/core/chat/queries";
import { isActiveAgentTaskStatus } from "@multica/core/agents";
import { cn } from "@multica/ui/lib/utils";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@multica/ui/components/ui/sheet";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogMedia,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { AppLink, useNavigation } from "../../../navigation";
import { useT } from "../../../i18n";
import { AgentTranscriptDialog, buildTimeline } from "../../../common/task-transcript";
import { statusLabel } from "../run-review-format";
import { SupervisionFeedbackDialog } from "./supervision-feedback";
import { SupervisionEvidenceDialog, type EvidenceTarget } from "./supervision-evidence";
import { errorText, moment } from "./supervision-shared";

/**
 * Detail sheet for one reviewed task. Reading order follows the operator's
 * questions: what was concluded, what is wrong, what evidence backs it up,
 * where reading stopped, then the audit trails.
 */
export function SupervisionDetail({ issueId, canManage, close }: { issueId: string; canManage: boolean; close: () => void }) {
  const { t } = useT("run-reviews");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const nav = useNavigation();
  const queryClient = useQueryClient();
  const detail = useQuery(supervisionDetailOptions(wsId, issueId));
  const [confirming, setConfirming] = useState(false);
  const [evidence, setEvidence] = useState<EvidenceTarget | null>(null);
  const recheck = useMutation({
    mutationFn: () => api.checkSupervision({ issue_id: issueId, full: true }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: supervisionKeys.all(wsId) });
      setConfirming(false);
      toast.success(t(($) => $.supervision.scheduled));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const data = detail.data;
  const actionLabel = (action: string) => {
    if (action === "auto_resolved") return t(($) => $.supervision.auto_resolved);
    if (action === "feedback") return t(($) => $.supervision.feedback);
    if (action === "resolved") return t(($) => $.supervision.resolved);
    if (action === "ignored") return t(($) => $.supervision.ignored);
    if (action === "in_progress") return t(($) => $.supervision.in_progress);
    return t(($) => $.supervision.reopen);
  };
  const recordsHref = (target: EvidenceTarget) => {
    const next = new URLSearchParams(nav.searchParams);
    next.set("tab", "records");
    next.set("issue", issueId);
    next.set("return_supervision", issueId);
    if (target.run) next.set("task", target.run);
    if (target.kind && target.id) {
      next.set("evidence_id", target.id);
      next.set("evidence_kind", target.kind);
    }
    if (target.batch) next.set("evidence_batch", target.batch);
    else next.delete("evidence_batch");
    return paths.runReviews() + "?" + next.toString();
  };
  return (
    <Sheet open={Boolean(issueId)} onOpenChange={(open) => { if (!open) close(); }}>
      <SheetContent className="!w-full overflow-y-auto sm:!max-w-[640px]">
        <SheetHeader>
          <SheetTitle className="break-words pr-8">{data?.title ?? t(($) => $.supervision.task_detail)}</SheetTitle>
          <SheetDescription className="text-caption">{t(($) => $.supervision.detail_hint)}</SheetDescription>
        </SheetHeader>
        {detail.isPending ? (
          <p className="p-5" role="status">{t(($) => $.supervision.loading)}</p>
        ) : detail.isError ? (
          <div role="alert" className="p-5">
            {errorText(detail.error)}
            <Button className="ml-2" variant="outline" onClick={() => void detail.refetch()}>{t(($) => $.supervision.retry)}</Button>
          </div>
        ) : data ? (
          <div className="min-w-0 space-y-6 break-words p-5">
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" render={<AppLink href={paths.issueDetail(issueId)} />}>{t(($) => $.supervision.open_task)}</Button>
              {canManage && (
                <Button variant="outline" disabled={recheck.isPending} onClick={() => setConfirming(true)}>
                  {t(($) => $.supervision.recheck)}
                </Button>
              )}
            </div>

            <section aria-labelledby="supervision-conclusion">
              <h3 id="supervision-conclusion" className="font-semibold">{t(($) => $.supervision.current_conclusion)}</h3>
              <p className="mt-2 whitespace-pre-wrap text-body">{data.subject.summary || t(($) => $.supervision.no_conclusion)}</p>
              <div className="mt-3 grid gap-3 sm:grid-cols-2">
                <AssessmentCard
                  title={t(($) => $.supervision.efficiency)}
                  value={data.subject.efficiency}
                  clearNote={t(($) => $.supervision.efficiency_clear)}
                  problemNote={t(($) => $.supervision.efficiency_problem)}
                  unknownNote={t(($) => $.supervision.efficiency_unknown)}
                />
                <AssessmentCard
                  title={t(($) => $.supervision.correctness)}
                  value={data.subject.correctness}
                  clearNote={t(($) => $.supervision.correctness_clear)}
                  problemNote={t(($) => $.supervision.correctness_problem)}
                  unknownNote={t(($) => $.supervision.correctness_unknown)}
                />
              </div>
            </section>

            {data.notes.length > 0 && (
              <section>
                <h3 className="font-semibold">{t(($) => $.supervision.user_notes)}</h3>
                <div className="mt-2 space-y-2">
                  {data.notes.map((note) => (
                    <blockquote key={note.id} className="whitespace-pre-wrap border-l-2 border-info pl-3 text-body">
                      <p>{note.content}</p>
                      <footer className="mt-1 text-caption text-muted-foreground">{moment(note.created_at)}</footer>
                    </blockquote>
                  ))}
                </div>
              </section>
            )}

            <section aria-labelledby="supervision-findings">
              <h3 id="supervision-findings" className="mb-3 font-semibold">
                {t(($) => $.supervision.findings)}
                {data.findings.length > 0 && <span className="ml-2 text-caption font-normal text-muted-foreground">{data.findings.length}</span>}
              </h3>
              {data.findings.length === 0 ? (
                <p className="text-body text-muted-foreground">
                  {data.subject.last_checked_at ? t(($) => $.supervision.no_findings_scope) : t(($) => $.supervision.no_conclusion)}
                </p>
              ) : (
                <div className="space-y-3">
                  {data.findings.map((finding) => (
                    <FindingCard
                      key={finding.id}
                      finding={finding}
                      canManage={canManage}
                      issueId={issueId}
                      assigneeType={data.assignee_type}
                      assigneeId={data.assignee_id}
                      openEvidence={setEvidence}
                    />
                  ))}
                </div>
              )}
            </section>

            <section aria-labelledby="supervision-gaps">
              <h3 id="supervision-gaps" className="font-semibold">
                {t(($) => $.supervision.gaps)}
                {data.subject.gaps.length > 0 && <span className="ml-2 text-caption font-normal text-muted-foreground">{data.subject.gaps.length}</span>}
              </h3>
              {data.subject.gaps.length === 0 ? (
                <p className="mt-2 text-body text-muted-foreground">{t(($) => $.supervision.no_gaps)}</p>
              ) : (
                <ul className="mt-2 list-disc space-y-1 pl-5 text-body">
                  {data.subject.gaps.map((gap, index) => <li key={index}>{gap}</li>)}
                </ul>
              )}
            </section>

            <section aria-labelledby="supervision-coverage">
              <h3 id="supervision-coverage" className="font-semibold">{t(($) => $.supervision.coverage)}</h3>
              <div className="mt-2 rounded-lg border p-3">
                <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 text-body">
                  <strong className="font-medium">
                    {t(($) => $.supervision.coverage_materials, { covered: data.coverage.covered, total: data.coverage.materials })}
                  </strong>
                  <span className="text-caption text-muted-foreground">{t(($) => $.supervision.coverage_runs, { count: data.coverage.runs })}</span>
                </div>
                <div
                  className="my-3 h-1.5 overflow-hidden rounded-full bg-muted"
                  role="progressbar"
                  aria-valuenow={data.coverage.covered}
                  aria-valuemax={Math.max(1, data.coverage.materials)}
                  aria-valuemin={0}
                  aria-label={t(($) => $.supervision.coverage)}
                >
                  <div
                    className="h-full bg-info transition-[width] motion-reduce:transition-none"
                    style={{ width: (data.coverage.materials ? Math.min(100, (data.coverage.covered / data.coverage.materials) * 100) : 0) + "%" }}
                  />
                </div>
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.supervision.last_check)} {moment(data.subject.last_checked_at)}
                  {data.subject.desired_version > data.subject.reviewed_version && " · " + t(($) => $.supervision.status_changed)}
                </p>
                <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.supervision.coverage_hint)}</p>
                {data.subject.gaps.length > 0 && (
                  <p className="mt-1 text-caption text-warning">{t(($) => $.supervision.coverage_gap_hint, { count: data.subject.gaps.length })}</p>
                )}
                {data.subject.stopped && <p className="mt-2 text-body">{t(($) => $.supervision.stopped)}</p>}
                {data.subject.error && <p role="alert" className="mt-2 text-destructive">{data.subject.error}</p>}
                <details className="mt-3 rounded-md border p-3">
                  <summary className="cursor-pointer font-medium">
                    {t(($) => $.supervision.investigation_history)}
                    <span className="ml-2 text-caption font-normal text-muted-foreground">{data.investigations.length}</span>
                  </summary>
                  <p className="mt-2 text-caption text-muted-foreground">{t(($) => $.supervision.investigation_history_hint)}</p>
                  {data.investigations.length === 0 ? (
                    <p className="mt-3 text-body text-muted-foreground">{t(($) => $.supervision.investigation_empty)}</p>
                  ) : (
                    <div className="mt-3 space-y-3">
                      {data.investigations.map((entry) => (
                        <InvestigationRow key={entry.id} entry={entry} openEvidence={setEvidence} />
                      ))}
                    </div>
                  )}
                </details>
              </div>
            </section>

            <details className="rounded-lg border p-3">
              <summary className="cursor-pointer font-medium">{t(($) => $.supervision.requirement)}</summary>
              <p className="mt-3 whitespace-pre-wrap break-words text-body">{data.description}</p>
            </details>

            <details className="rounded-lg border p-3">
              <summary className="cursor-pointer font-medium">
                {t(($) => $.supervision.actions)}
                <span className="ml-2 text-caption font-normal text-muted-foreground">{data.actions.length}</span>
              </summary>
              <div className="mt-3 space-y-3">
                {data.actions.length === 0 ? <p className="text-caption text-muted-foreground">{t(($) => $.supervision.actions_empty)}</p> : null}
                {data.actions.map((action) => (
                  <div key={action.id} className="text-caption">
                    <p>{moment(action.created_at)} · {actionLabel(action.action)}</p>
                    <p className="mt-1 whitespace-pre-wrap break-words text-muted-foreground">{action.reason}</p>
                    {action.receipt?.evidence?.map((item) => (
                      <Button
                        key={item.kind + ":" + item.id}
                        variant="link"
                        size="sm"
                        onClick={() => setEvidence({ kind: item.kind, id: item.id, batch: item.batch_id, run: item.run_id })}
                      >
                        {item.kind} · {item.id.slice(0, 8)}
                      </Button>
                    ))}
                  </div>
                ))}
              </div>
            </details>

            <details className="rounded-lg border p-3">
              <summary className="cursor-pointer font-medium">
                {t(($) => $.supervision.history)}
                <span className="ml-2 text-caption font-normal text-muted-foreground">{data.batches.length}</span>
              </summary>
              <div className="mt-3 space-y-3">
                {data.batches.length === 0 ? <p className="text-caption text-muted-foreground">{t(($) => $.supervision.history_empty)}</p> : null}
                {data.batches.map((batch) => (
                  <div key={batch.id} className={cn("rounded-md border p-3 text-caption", batch.error && "border-destructive/40")}>
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="tabular-nums">{moment(batch.created_at)}</span>
                      <Badge variant={batch.status === "failed" ? "destructive" : "outline"}>{statusLabel(batch.status)}</Badge>
                      <span className="text-muted-foreground">{batch.rules_version} · {t(($) => $.supervision.materials_count, { count: batch.material_count })}</span>
                    </div>
                    {batch.error && <p className="mt-1 break-words text-destructive">{batch.error}</p>}
                    {canManage && (
                      <details className="mt-2">
                        <summary className="cursor-pointer underline">{t(($) => $.supervision.agent_instructions)}</summary>
                        {batch.instructions ? (
                          <>
                            <pre className="mt-2 max-h-96 overflow-y-auto whitespace-pre-wrap break-words font-sans text-body">{batch.instructions}</pre>
                            <p className="mt-2 break-all text-muted-foreground">{t(($) => $.supervision.instructions_hash, { hash: batch.instructions_sha256 })}</p>
                          </>
                        ) : (
                          <p className="mt-2 text-muted-foreground">{t(($) => $.supervision.instructions_missing)}</p>
                        )}
                      </details>
                    )}
                    {batch.task_id && <SupervisionRunTranscript batchId={batch.id} />}
                  </div>
                ))}
              </div>
            </details>
          </div>
        ) : null}
      </SheetContent>
      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent className="sm:!max-w-[440px]">
          <AlertDialogHeader>
            <AlertDialogMedia className="bg-info/10 text-info"><RotateCw /></AlertDialogMedia>
            <AlertDialogTitle>{t(($) => $.supervision.recheck_title)}</AlertDialogTitle>
            <AlertDialogDescription>{t(($) => $.supervision.recheck_body)}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={recheck.isPending}>{t(($) => $.supervision.cancel)}</AlertDialogCancel>
            <AlertDialogAction disabled={recheck.isPending} onClick={() => recheck.mutate()}>
              {t(($) => $.supervision.recheck_confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      {evidence && <SupervisionEvidenceDialog target={evidence} recordsHref={recordsHref(evidence)} close={() => setEvidence(null)} />}
    </Sheet>
  );
}

function AssessmentCard({ title, value, clearNote, problemNote, unknownNote }: { title: string; value: string; clearNote: string; problemNote: string; unknownNote: string }) {
  const { t } = useT("run-reviews");
  const label = value === "clear" ? t(($) => $.supervision.no_findings) : value === "problem" ? t(($) => $.supervision.problems) : t(($) => $.supervision.no_conclusion);
  const note = value === "clear" ? clearNote : value === "problem" ? problemNote : unknownNote;
  return (
    <div className="rounded-lg border p-3">
      <p className="text-caption text-muted-foreground">{title}</p>
      <p className={cn("mt-1 text-body font-medium", value === "problem" ? "text-warning" : value === "clear" ? "text-foreground" : "text-muted-foreground")}>{label}</p>
      <p className="mt-1 text-caption text-muted-foreground">{note}</p>
    </div>
  );
}

type Investigation = {
  id: string;
  created_at: string;
  request: { reason: string; path?: string };
  result: { tool: string; source_label?: string; path?: string; error?: string; offset: number; end: number; length: number; truncated: boolean };
};

function InvestigationRow({ entry, openEvidence }: { entry: Investigation; openEvidence: (target: EvidenceTarget) => void }) {
  const { t } = useT("run-reviews");
  const isIndex = entry.result.tool === "supervision_material" && entry.result.path === "index";
  const isRead = entry.result.tool === "supervision_read" || entry.result.tool === "supervision_material";
  const action = isIndex ? t(($) => $.supervision.investigation_index)
    : entry.result.tool === "supervision_search" ? t(($) => $.supervision.investigation_search)
    : entry.result.tool === "supervision_git" ? t(($) => $.supervision.investigation_git)
    : t(($) => $.supervision.investigation_read);
  const range = { start: entry.result.offset, end: entry.result.end, total: entry.result.length };
  return (
    <article className="rounded-md border p-3 text-caption">
      <p className="text-muted-foreground">{moment(entry.created_at)} · {action}</p>
      <p className="mt-1 whitespace-pre-wrap break-words text-body">{entry.request.reason}</p>
      {entry.result.source_label && <p className="mt-1 break-words text-muted-foreground">{entry.result.source_label}</p>}
      <p className="mt-1 break-all font-mono">{entry.result.path || entry.request.path || entry.result.tool}</p>
      <p className="mt-1 tabular-nums">
        {isIndex ? t(($) => $.supervision.investigation_index_range, range)
          : isRead ? t(($) => $.supervision.investigation_read_range, range)
          : t(($) => $.supervision.investigation_result_range, range)}
      </p>
      {entry.result.error ? (
        <p className="mt-1 whitespace-pre-wrap break-words text-destructive">{entry.result.error}</p>
      ) : entry.result.truncated ? (
        <p className="mt-1 text-warning">{t(($) => $.supervision.investigation_partial)}</p>
      ) : null}
      <Button variant="link" size="sm" className="mt-1 px-0" onClick={() => openEvidence({ kind: "investigation", id: entry.id })}>
        {t(($) => $.supervision.investigation_evidence)}
      </Button>
    </article>
  );
}

function FindingCard({ finding, canManage, issueId, assigneeType, assigneeId, openEvidence }: {
  finding: SupervisionFinding;
  canManage: boolean;
  issueId: string;
  assigneeType: string | null;
  assigneeId: string | null;
  openEvidence: (target: EvidenceTarget) => void;
}) {
  const { t } = useT("run-reviews");
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const [feedback, setFeedback] = useState(false);
  const [editing, setEditing] = useState(false);
  const [reason, setReason] = useState("");
  const reasonId = useId();
  const update = useMutation({
    mutationFn: ({ status, snoozed_until }: { status: string; snoozed_until?: string }) =>
      api.updateSupervisionFinding(finding.id, { status, reason, snoozed_until, operation_id: createSafeId() }),
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: supervisionKeys.all(wsId) }); },
    onError: (e) => toast.error(errorText(e)),
  });
  const status = finding.status === "open" ? t(($) => $.supervision.open)
    : finding.status === "in_progress" ? t(($) => $.supervision.in_progress)
    : finding.status === "resolved" ? t(($) => $.supervision.resolved)
    : finding.status === "ignored" ? t(($) => $.supervision.ignored)
    : finding.status;
  const statusTone = finding.status === "open" ? "border-warning/40 bg-warning/10 text-warning"
    : finding.status === "in_progress" ? "border-info/40 bg-info/10 text-info"
    : "text-muted-foreground";
  const snoozed = finding.snoozed_until && new Date(finding.snoozed_until).getTime() > Date.now();
  return (
    <article className="rounded-lg border p-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <h4 className="min-w-0 break-words text-body font-medium">{finding.title}</h4>
        <Badge variant="outline" className={statusTone}>{status}</Badge>
      </div>
      <p className="mt-1 text-caption text-muted-foreground">
        {finding.confidence === "confirmed" ? t(($) => $.supervision.confirmed) : t(($) => $.supervision.risk)}
        {finding.human_resolution && " · " + t(($) => $.supervision.human_resolved)}
      </p>
      <p className="mt-2 whitespace-pre-wrap break-words text-body">{finding.description}</p>
      {snoozed && <p className="mt-2 text-caption text-muted-foreground">{t(($) => $.supervision.snoozed_until)} {moment(finding.snoozed_until)}</p>}
      {canManage && (
        <div className="mt-3 flex flex-wrap gap-2">
          <Button variant="outline" aria-expanded={editing} onClick={() => setEditing((value) => !value)}>
            {editing ? t(($) => $.supervision.finding_actions_hide) : t(($) => $.supervision.finding_actions)}
          </Button>
          <Button variant="outline" onClick={() => setFeedback(true)}>{t(($) => $.supervision.feedback)}</Button>
        </div>
      )}
      {canManage && editing && (
        <div className="mt-3 space-y-2 rounded-md border bg-muted/30 p-3">
          <label htmlFor={reasonId} className="block text-caption text-muted-foreground">{t(($) => $.supervision.reason)}</label>
          <Input id={reasonId} value={reason} onChange={(e) => setReason(e.target.value)} />
          <p className="text-caption text-muted-foreground">{t(($) => $.supervision.reason_hint)}</p>
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" disabled={update.isPending} onClick={() => update.mutate({ status: "in_progress" })}>{t(($) => $.supervision.mark_processing)}</Button>
            <Button variant="outline" disabled={update.isPending} onClick={() => update.mutate({ status: "resolved" })}>{t(($) => $.supervision.mark_resolved)}</Button>
            <Button variant="ghost" disabled={update.isPending || !reason.trim()} onClick={() => update.mutate({ status: "ignored" })}>{t(($) => $.supervision.ignore)}</Button>
            <Button variant="ghost" disabled={update.isPending} onClick={() => update.mutate({ status: "open" })}>{t(($) => $.supervision.reopen)}</Button>
            <Button variant="ghost" disabled={update.isPending} onClick={() => update.mutate({ status: finding.status, snoozed_until: new Date(Date.now() + 3600000).toISOString() })}>
              {t(($) => $.supervision.snooze)}
            </Button>
          </div>
        </div>
      )}
      <details className="mt-3">
        <summary className="cursor-pointer text-caption underline">{t(($) => $.supervision.evidence_and_impact)}</summary>
        <div className="mt-2 space-y-2">
          <p className="whitespace-pre-wrap break-words text-body">
            <span className="mr-1 text-caption text-muted-foreground">{t(($) => $.supervision.impact_label)}</span>
            {finding.impact}
          </p>
          <p className="whitespace-pre-wrap break-words text-body">
            <span className="mr-1 text-caption text-muted-foreground">{t(($) => $.supervision.recommendation_label)}</span>
            {finding.recommendation}
          </p>
          <div className="flex flex-wrap gap-2">
            {finding.evidence.length === 0 ? (
              <p className="text-caption text-muted-foreground">{t(($) => $.supervision.no_evidence)}</p>
            ) : finding.evidence.map((item) => (
              <Button
                key={item.kind + ":" + item.id}
                variant="outline"
                size="sm"
                onClick={() => openEvidence({ kind: item.kind, id: item.id, batch: item.batch_id, run: item.run_id })}
              >
                {t(($) => $.supervision.evidence_open)} · {item.kind} {item.id.slice(0, 8)}
              </Button>
            ))}
          </div>
        </div>
      </details>
      <SupervisionFeedbackDialog
        open={feedback}
        close={() => setFeedback(false)}
        finding={finding}
        issueId={issueId}
        assigneeType={assigneeType}
        assigneeId={assigneeId}
      />
    </article>
  );
}

function SupervisionRunTranscript({ batchId }: { batchId: string }) {
  const { t } = useT("run-reviews");
  const wsId = useWorkspaceId();
  const [open, setOpen] = useState(false);
  const task = useQuery({
    queryKey: [...supervisionKeys.all(wsId), "batch-task", batchId],
    queryFn: () => api.getSupervisionBatchTask(batchId),
    enabled: open,
    refetchInterval: (query) => open && query.state.data && isActiveAgentTaskStatus(query.state.data.status) ? 5000 : false,
  });
  const messages = useQuery({
    ...taskMessagesOptions(task.data?.id ?? ""),
    enabled: open && Boolean(task.data),
    refetchInterval: open && task.data && isActiveAgentTaskStatus(task.data.status) ? 3000 : false,
  });
  const items = useMemo(() => buildTimeline(messages.data ?? []), [messages.data]);
  const failed = task.error ?? messages.error;
  return (
    <>
      <Button variant="link" size="sm" onClick={() => setOpen(true)} disabled={open && (task.isPending || messages.isPending)}>
        {t(($) => $.supervision.view_run)}
      </Button>
      {open && failed && (
        <p role="alert" className="text-destructive">
          {errorText(failed)}
          <Button variant="ghost" onClick={() => { void task.refetch(); void messages.refetch(); }}>{t(($) => $.supervision.retry)}</Button>
        </p>
      )}
      {task.data && messages.data && (
        <AgentTranscriptDialog
          task={task.data}
          agentName={t(($) => $.supervision.agent)}
          items={items}
          open={open}
          onOpenChange={setOpen}
          isLive={isActiveAgentTaskStatus(task.data.status)}
        />
      )}
    </>
  );
}
