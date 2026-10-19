"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Loader2, Search, Settings2 } from "lucide-react";
import { toast } from "sonner";
import { api, ApiError } from "@multica/core/api";
import { createSafeId } from "@multica/core/utils";
import { useWorkspaceId, useWorkspacePaths } from "@multica/core/paths";
import { supervisionSnapshotOptions, supervisionListOptions, supervisionDetailOptions, supervisionKeys } from "@multica/core/supervision/queries";
import type { SupervisionConfig, SupervisionFinding } from "@multica/core/supervision/schema";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from "@multica/ui/components/ui/sheet";
import { Dialog, DialogContent, DialogTitle, DialogDescription } from "@multica/ui/components/ui/dialog";
import { useNavigation, AppLink } from "../../navigation";
import { useT } from "../../i18n";
import { AgentTranscriptDialog, buildTimeline } from "../../common/task-transcript";
import { taskMessagesOptions } from "@multica/core/chat/queries";
import { isActiveAgentTaskStatus } from "@multica/core/agents";
import { blockedReasonLabel } from "../../issues/blocked-trigger-copy";
import { formatDateTime, statusLabel } from "./run-review-format";

function date(value?: string | null) { return value ? formatDateTime(value) : "—"; }
function errorText(error: unknown) { return error instanceof Error ? error.message : String(error); }

export function QualitySupervision() {
  const { t } = useT("run-reviews");
  const wsId = useWorkspaceId();
  const nav = useNavigation();
  const queryClient = useQueryClient();
  const params = nav.searchParams;
  const search = params.get("search") ?? "";
  const filter = params.get("filter") ?? "all";
  const project = params.get("project") ?? "";
  const offset = Math.max(0, Number(params.get("offset")) || 0);
  const selected = params.get("selected") ?? "";
  const [settings, setSettings] = useState(false);
  const snapshot = useQuery(supervisionSnapshotOptions(wsId));
  const list = useQuery(supervisionListOptions(wsId, search, filter, offset, project));
  const projects = useQuery({ queryKey: [...supervisionKeys.all(wsId), "projects"], queryFn: () => api.listProjects() });
  const data = snapshot.data;
  const invalidate = () => queryClient.invalidateQueries({ queryKey: supervisionKeys.all(wsId) });
  const check = useMutation({ mutationFn: (body: Parameters<typeof api.checkSupervision>[0]) => api.checkSupervision(body), onSuccess: (_, variables) => { void invalidate(); toast.success(variables.stop ? t(($) => $.supervision.stopped) : t(($) => $.supervision.scheduled)); }, onError: (e) => toast.error(errorText(e)) });
  const route = (change: Record<string, string | null>) => {
    const next = new URLSearchParams(params);
    for (const [key, value] of Object.entries(change)) { if (value) next.set(key, value); else next.delete(key); }
    nav.replace(`${nav.pathname}?${next}`);
  };
  if (snapshot.isPending) return <div className="p-6 text-muted-foreground" role="status">{t(($) => $.supervision.loading)}</div>;
  if (snapshot.isError || !data) return <div role="alert" className="space-y-3 p-6"><p>{t(($) => $.supervision.load_failed)}</p><p className="text-muted-foreground">{errorText(snapshot.error)}</p><Button onClick={() => void snapshot.refetch()}>{t(($) => $.supervision.retry)}</Button></div>;
  const config = data.config;
  const summary = data.summary;
  return <div className="min-h-0 flex-1 overflow-y-auto p-4 sm:p-6">
    <div className="mb-5 flex flex-wrap items-center justify-between gap-3">
      <div className="space-y-1">
        <div className="flex items-center gap-2 text-body font-medium"><span className={`h-2 w-2 rounded-full ${config.error ? "bg-destructive" : config.enabled ? "bg-info" : "bg-muted-foreground"}`} />{config.error ? t(($) => $.supervision.interrupted) : config.enabled ? t(($) => $.supervision.active) : t(($) => $.supervision.paused)}</div>
        <p className="text-caption text-muted-foreground">{t(($) => $.supervision.last_success)} {date(config.last_success_at)}</p>
      </div>
      {data.can_manage && <div className="flex flex-wrap gap-2"><Button variant="outline" disabled={!config.enabled || check.isPending} onClick={() => check.mutate({})}>{check.isPending && <Loader2 className="size-4 animate-spin" />}{t(($) => $.supervision.check_new)}</Button>{summary.running > 0 && <Button variant="outline" disabled={check.isPending} onClick={() => check.mutate({ stop: true })}>{t(($) => $.supervision.stop_check)}</Button>}<Button variant="outline" onClick={() => setSettings(true)}><Settings2 className="size-4" />{t(($) => $.supervision.settings)}</Button></div>}
    </div>
    {config.error && <div role="alert" className="mb-4 rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-body"><AlertTriangle className="mr-2 inline size-4" />{config.error}</div>}
    {!config.agent_id ? <div className="rounded-lg border p-8 text-center"><h2 className="text-lg font-semibold">{t(($) => $.supervision.welcome)}</h2><p className="mx-auto mt-2 max-w-lg text-body text-muted-foreground">{t(($) => $.supervision.welcome_description)}</p>{data.can_manage && <Button className="mt-5" onClick={() => setSettings(true)}>{t(($) => $.supervision.configure)}</Button>}</div> : <>
      <section aria-label={t(($) => $.supervision.coverage)} className="mb-5 rounded-lg border p-4">
        <div className="flex flex-wrap gap-x-7 gap-y-3 text-body tabular-nums"><strong>{t(($) => $.supervision.total, { count: summary.total })}</strong><span>{t(($) => $.supervision.covered, { count: summary.covered })}</span><span>{t(($) => $.supervision.pending, { count: summary.pending })}</span><span>{t(($) => $.supervision.running, { count: summary.running })}</span><span>{t(($) => $.supervision.no_runs, { count: summary.no_runs })}</span></div>
        <div className="my-3 h-1.5 overflow-hidden rounded-full bg-muted" role="progressbar" aria-valuenow={summary.covered} aria-valuemax={Math.max(1, summary.total)} aria-valuemin={0} aria-label={t(($) => $.supervision.coverage)}><div className="h-full bg-info transition-[width] motion-reduce:transition-none" style={{ width: `${summary.total ? Math.min(100, summary.covered / summary.total * 100) : 0}%` }} /></div>
        <p className="text-caption text-muted-foreground">{config.inventory_complete ? t(($) => $.supervision.coverage_hint) : t(($) => $.supervision.registering)} {t(($) => $.supervision.findings_summary, { count: summary.open_findings, tasks: summary.issues_with_findings, gaps: summary.incomplete })}</p>
        {summary.oldest_pending_at && <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.supervision.oldest_pending)} {date(summary.oldest_pending_at)}</p>}
      </section>
      <div className="mb-4 flex flex-wrap items-center gap-2"><div className="relative min-w-48 flex-1 sm:max-w-sm"><Search className="pointer-events-none absolute left-3 top-3 size-4 text-muted-foreground" /><Input aria-label={t(($) => $.supervision.search)} placeholder={t(($) => $.supervision.search)} className="pl-9" value={search} onChange={(e) => route({ search: e.target.value, offset: null })} /></div>
        <select aria-label={t(($) => $.supervision.project)} className="h-10 max-w-full rounded-md border bg-background px-3 text-body" value={project} onChange={(e) => route({ project: e.target.value, offset: null })}><option value="">{t(($) => $.supervision.all_projects)}</option>{projects.data?.projects.map((p) => <option key={p.id} value={p.id}>{p.title}</option>)}</select>
        <select aria-label={t(($) => $.supervision.filter)} className="h-10 rounded-md border bg-background px-3 text-body" value={filter} onChange={(e) => route({ filter: e.target.value, offset: null })}><option value="all">{t(($) => $.supervision.all)}</option><option value="problems">{t(($) => $.supervision.problems)}</option><option value="incomplete">{t(($) => $.supervision.incomplete)}</option><option value="pending">{t(($) => $.supervision.pending_filter)}</option></select>
      </div>
      {list.isError ? <div role="alert" className="p-4">{errorText(list.error)} <Button variant="outline" onClick={() => void list.refetch()}>{t(($) => $.supervision.retry)}</Button></div> : <div className="overflow-hidden rounded-lg border">
        <div className="hidden grid-cols-[minmax(0,1fr)_170px_150px_120px] gap-4 border-b bg-muted/30 px-4 py-3 text-caption text-muted-foreground md:grid"><span>{t(($) => $.supervision.task)}</span><span>{t(($) => $.supervision.progress)}</span><span>{t(($) => $.supervision.conclusion)}</span><span>{t(($) => $.supervision.last_check)}</span></div>
        {list.isPending ? <div className="p-6" role="status">{t(($) => $.supervision.loading)}</div> : list.data?.items.length === 0 ? <div className="p-8 text-center text-muted-foreground">{t(($) => $.supervision.empty)}<Button variant="ghost" onClick={() => route({ search: null, filter: null, project: null, offset: null })}>{t(($) => $.supervision.clear_filters)}</Button></div> : list.data?.items.map((item) => <button key={item.issue_id} type="button" onClick={() => route({ selected: item.issue_id })} className="grid min-h-16 w-full gap-2 border-b px-4 py-3 text-left text-body last:border-b-0 hover:bg-accent/50 focus-visible:outline-2 focus-visible:outline-ring md:grid-cols-[minmax(0,1fr)_170px_150px_120px] md:items-center md:gap-4">
          <span className="min-w-0"><span className="block truncate font-medium">{item.identifier} {item.title}</span><span className="text-caption text-muted-foreground">{statusLabel(item.business_status)}</span></span>
          <span className="text-caption">{item.run_count === 0 ? t(($) => $.supervision.status_no_runs) : item.running ? t(($) => $.supervision.status_running) : item.stopped ? t(($) => $.supervision.status_stopped) : item.failures >= 3 ? t(($) => $.supervision.status_failed) : item.desired_version > item.reviewed_version ? item.last_checked_at ? t(($) => $.supervision.status_changed) : t(($) => $.supervision.status_pending) : t(($) => $.supervision.status_covered)}</span>
          <span className={`text-caption ${item.open_findings ? "text-warning" : "text-muted-foreground"}`}>{item.open_findings ? t(($) => $.supervision.problem_count, { count: item.open_findings }) : item.gaps.length ? t(($) => $.supervision.incomplete) : item.last_checked_at ? t(($) => $.supervision.no_findings) : t(($) => $.supervision.no_conclusion)}</span><span className="text-caption tabular-nums text-muted-foreground">{date(item.last_checked_at)}</span>
        </button>)}
      </div>}
      <div className="mt-3 flex justify-end gap-2"><Button variant="outline" disabled={offset === 0} onClick={() => route({ offset: String(Math.max(0, offset - 50)) })}>{t(($) => $.supervision.previous)}</Button><Button variant="outline" disabled={!list.data?.has_more} onClick={() => route({ offset: String(offset + 50) })}>{t(($) => $.supervision.next)}</Button></div>
    </>}
    <SupervisionSettings open={settings} close={() => setSettings(false)} config={config} />
    <SupervisionDetail issueId={selected} canManage={data.can_manage} close={() => route({ selected: null })} />
  </div>;
}

function SupervisionSettings({ open, close, config }: { open: boolean; close: () => void; config: SupervisionConfig }) {
  const { t } = useT("run-reviews"); const wsId = useWorkspaceId(); const queryClient = useQueryClient();
  const [agentId, setAgentId] = useState<string | null>(null); const [interval, setInterval] = useState<number | null>(null);
  const [investigation, setInvestigation] = useState<boolean | null>(null);
  const agents = useQuery({ queryKey: [...supervisionKeys.all(wsId), "agents"], queryFn: () => api.listAgents(), enabled: open });
  const save = useMutation({ mutationFn: (enabled: boolean) => api.updateSupervision({ agent_id: agentId ?? config.agent_id, enabled, interval_seconds: interval ?? config.interval_seconds, investigation_enabled: investigation ?? (config.agent_id ? config.investigation_enabled : true) }), onSuccess: () => { void queryClient.invalidateQueries({ queryKey: supervisionKeys.all(wsId) }); close(); }, onError: (e) => toast.error(errorText(e)) });
  return <Sheet open={open} onOpenChange={(value) => { if (!value) close(); }}><SheetContent className="w-full sm:max-w-lg"><SheetHeader><SheetTitle>{t(($) => $.supervision.settings)}</SheetTitle><SheetDescription>{t(($) => $.supervision.settings_description)}</SheetDescription></SheetHeader><div className="space-y-5 p-4">
    <label className="block space-y-2 text-body"><span>{t(($) => $.supervision.agent)}</span><select aria-label={t(($) => $.supervision.agent)} className="h-10 w-full rounded-md border bg-background px-3" value={agentId ?? config.agent_id ?? ""} onChange={(e) => setAgentId(e.target.value)}><option value="">{t(($) => $.supervision.select_agent)}</option>{agents.data?.filter((a) => !a.archived_at).map((a) => <option key={a.id} value={a.id}>{a.name}</option>)}</select></label>
    {agents.isError && <p role="alert">{errorText(agents.error)}</p>}
    <div className="text-body"><p className="font-medium">{t(($) => $.supervision.scope)}</p><p className="mt-1 text-muted-foreground">{t(($) => $.supervision.scope_description)}</p></div>
    <label className="block space-y-2 text-body"><span>{t(($) => $.supervision.interval)}</span><select aria-label={t(($) => $.supervision.interval)} className="h-10 w-full rounded-md border bg-background px-3" value={interval ?? config.interval_seconds} onChange={(e) => setInterval(Number(e.target.value))}><option value={60}>{t(($) => $.supervision.interval_short)}</option><option value={300}>{t(($) => $.supervision.minute_five)}</option><option value={900}>{t(($) => $.supervision.minute_fifteen)}</option></select></label>
    <label className="flex items-start gap-3 rounded-lg border p-3 text-body"><input type="checkbox" className="mt-1 size-4 shrink-0" checked={investigation ?? (config.agent_id ? config.investigation_enabled : true)} onChange={(e) => setInvestigation(e.target.checked)} /><span><span className="block font-medium">{t(($) => $.supervision.investigation_enable)}</span><span className="mt-1 block text-caption text-muted-foreground">{t(($) => $.supervision.investigation_hint)}</span></span></label>
    <p className="text-caption text-muted-foreground">{t(($) => $.supervision.interval_hint)}</p><p className="text-caption text-muted-foreground">{t(($) => $.supervision.feedback_manual)}</p>
    <div className="flex flex-wrap gap-2"><Button disabled={save.isPending || !(agentId ?? config.agent_id)} onClick={() => save.mutate(true)}>{save.isPending ? t(($) => $.supervision.saving) : t(($) => $.supervision.save_enable)}</Button>{config.enabled && <Button variant="outline" disabled={save.isPending} onClick={() => save.mutate(false)}>{t(($) => $.supervision.pause)}</Button>}</div>
  </div></SheetContent></Sheet>;
}

function SupervisionDetail({ issueId, canManage, close }: { issueId: string; canManage: boolean; close: () => void }) {
  const { t } = useT("run-reviews"); const wsId = useWorkspaceId(); const paths = useWorkspacePaths(); const nav = useNavigation(); const queryClient = useQueryClient();
  const detail = useQuery(supervisionDetailOptions(wsId, issueId));
  const recheck = useMutation({ mutationFn: () => api.checkSupervision({ issue_id: issueId, full: true }), onSuccess: () => { void queryClient.invalidateQueries({ queryKey: supervisionKeys.all(wsId) }); toast.success(t(($) => $.supervision.scheduled)); }, onError: (e) => toast.error(errorText(e)) });
  const data = detail.data;
  const assessment = (value: string) => value === "clear" ? t(($) => $.supervision.no_findings) : value === "problem" ? t(($) => $.supervision.problems) : t(($) => $.supervision.no_conclusion);
  const evidenceHref = (runId?: string, sourceId?: string, kind?: string, batch?: string) => { const next = new URLSearchParams(nav.searchParams); next.set("tab", "records"); next.set("issue", issueId); next.set("return_supervision", issueId); if (runId) next.set("task", runId); if (sourceId && kind) { next.set("evidence_id", sourceId); next.set("evidence_kind", kind); } if (batch) next.set("evidence_batch", batch); else next.delete("evidence_batch"); return `${paths.runReviews()}?${next}`; };
  return <Sheet open={Boolean(issueId)} onOpenChange={(open) => { if (!open) close(); }}><SheetContent className="w-full overflow-y-auto sm:max-w-[640px]"><SheetHeader><SheetTitle>{data?.title ?? t(($) => $.supervision.task_detail)}</SheetTitle><SheetDescription>{t(($) => $.supervision.detail_hint)}</SheetDescription></SheetHeader>
    {detail.isPending ? <p className="p-5" role="status">{t(($) => $.supervision.loading)}</p> : detail.isError ? <div role="alert" className="p-5">{errorText(detail.error)}<Button onClick={() => void detail.refetch()}>{t(($) => $.supervision.retry)}</Button></div> : data && <div className="space-y-6 p-5">
      <div className="flex flex-wrap gap-2"><AppLink href={paths.issueDetail(issueId)} className="rounded border px-3 py-2 text-body hover:bg-accent">{t(($) => $.supervision.open_task)}</AppLink>{canManage && <Button variant="outline" disabled={recheck.isPending} onClick={() => recheck.mutate()}>{t(($) => $.supervision.recheck)}</Button>}</div>
      <section><h3 className="font-semibold">{t(($) => $.supervision.current_conclusion)}</h3><p className="mt-2 whitespace-pre-wrap text-body">{data.subject.summary || t(($) => $.supervision.no_conclusion)}</p><dl className="mt-3 grid grid-cols-2 gap-3 text-caption"><div><dt className="text-muted-foreground">{t(($) => $.supervision.efficiency)}</dt><dd className="mt-1">{assessment(data.subject.efficiency)}</dd></div><div><dt className="text-muted-foreground">{t(($) => $.supervision.correctness)}</dt><dd className="mt-1">{assessment(data.subject.correctness)}</dd></div></dl></section>
      <section className="rounded-lg border p-3 text-caption"><h3 className="font-medium">{t(($) => $.supervision.coverage)}</h3><p className="mt-1 text-muted-foreground">{t(($) => $.supervision.material_coverage, { runs: data.coverage.runs, covered: data.coverage.covered, total: data.coverage.materials })}</p><p className="mt-1">{t(($) => $.supervision.last_check)} {date(data.subject.last_checked_at)} {data.subject.desired_version > data.subject.reviewed_version && t(($) => $.supervision.status_changed)}</p>{data.subject.stopped && <p className="mt-2">{t(($) => $.supervision.stopped)}</p>}{data.subject.error && <p role="alert" className="mt-2 text-destructive">{data.subject.error}</p>}</section>
      {data.notes.length > 0 && <section><h3 className="font-semibold">{t(($) => $.supervision.user_notes)}</h3><div className="mt-2 space-y-2">{data.notes.map((n) => <blockquote key={n.id} className="whitespace-pre-wrap border-l-2 border-info pl-3 text-body"><p>{n.content}</p><footer className="mt-1 text-caption text-muted-foreground">{date(n.created_at)}</footer></blockquote>)}</div></section>}
      <details className="rounded-lg border p-3"><summary className="cursor-pointer font-medium">{t(($) => $.supervision.requirement)}</summary><p className="mt-3 whitespace-pre-wrap break-words text-body">{data.description}</p></details>
      <section><h3 className="mb-3 font-semibold">{t(($) => $.supervision.findings)}</h3>{data.findings.length === 0 ? <p className="text-body text-muted-foreground">{data.subject.last_checked_at ? t(($) => $.supervision.no_findings_scope) : t(($) => $.supervision.no_conclusion)}</p> : <div className="space-y-3">{data.findings.map((f) => <FindingCard key={f.id} finding={f} canManage={canManage} evidenceHref={evidenceHref} issueId={issueId} assigneeType={data.assignee_type} assigneeId={data.assignee_id} />)}</div>}</section>
      {data.subject.gaps.length > 0 && <section><h3 className="font-semibold">{t(($) => $.supervision.gaps)}</h3><ul className="mt-2 list-disc space-y-1 pl-5 text-body">{data.subject.gaps.map((gap, i) => <li key={i}>{gap}</li>)}</ul></section>}
      <details className="rounded-lg border p-3"><summary className="cursor-pointer font-medium">{t(($) => $.supervision.investigation_history)}</summary><p className="mt-2 text-caption text-muted-foreground">{t(($) => $.supervision.investigation_history_hint)}</p>{data.investigations.length === 0 ? <p className="mt-3 text-body text-muted-foreground">{t(($) => $.supervision.investigation_empty)}</p> : <div className="mt-3 space-y-3">{data.investigations.map((entry) => {
        const index = entry.result.tool === "supervision_material" && entry.result.path === "index";
        const read = entry.result.tool === "supervision_read" || entry.result.tool === "supervision_material";
        const action = index ? t(($) => $.supervision.investigation_index) : entry.result.tool === "supervision_search" ? t(($) => $.supervision.investigation_search) : entry.result.tool === "supervision_git" ? t(($) => $.supervision.investigation_git) : t(($) => $.supervision.investigation_read);
        const range = { start: entry.result.offset, end: entry.result.end, total: entry.result.length };
        return <article key={entry.id} className="rounded-md border p-3 text-caption">
          <p className="text-muted-foreground">{date(entry.created_at)} · {action}</p>
          <p className="mt-1 whitespace-pre-wrap break-words text-body">{entry.request.reason}</p>
          {entry.result.source_label && <p className="mt-1 break-words text-muted-foreground">{entry.result.source_label}</p>}
          <p className="mt-1 break-all font-mono">{entry.result.path || entry.request.path || entry.result.tool}</p>
          <p className="mt-1 tabular-nums">{index ? t(($) => $.supervision.investigation_index_range, range) : read ? t(($) => $.supervision.investigation_read_range, range) : t(($) => $.supervision.investigation_result_range, range)}</p>
          {entry.result.error ? <p className="mt-1 whitespace-pre-wrap break-words text-destructive">{entry.result.error}</p> : entry.result.truncated ? <p className="mt-1 text-warning">{t(($) => $.supervision.investigation_partial)}</p> : null}
          <AppLink href={evidenceHref(undefined, entry.id, "investigation")} className="mt-2 inline-block rounded text-info underline underline-offset-4 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4">{t(($) => $.supervision.investigation_evidence)}</AppLink>
        </article>;
      })}</div>}</details>
      <details className="rounded-lg border p-3"><summary className="cursor-pointer font-medium">{t(($) => $.supervision.actions)}</summary><div className="mt-3 space-y-3">{data.actions.map((a) => <div key={a.id} className="text-caption"><p>{date(a.created_at)} · {a.action === "auto_resolved" ? t(($) => $.supervision.auto_resolved) : a.action === "feedback" ? t(($) => $.supervision.feedback) : a.action === "resolved" ? t(($) => $.supervision.resolved) : a.action === "ignored" ? t(($) => $.supervision.ignored) : a.action === "in_progress" ? t(($) => $.supervision.in_progress) : t(($) => $.supervision.reopen)}</p><p className="mt-1 whitespace-pre-wrap break-words text-muted-foreground">{a.reason}</p>{a.receipt?.evidence?.map((e) => <AppLink key={`${e.kind}:${e.id}`} href={evidenceHref(e.run_id, e.id, e.kind, e.batch_id)} className="mr-2 underline">{e.kind} · {e.id.slice(0, 8)}</AppLink>)}</div>)}</div></details>
      <details className="rounded-lg border p-3"><summary className="cursor-pointer font-medium">{t(($) => $.supervision.history)}</summary><div className="mt-3 space-y-3">{data.batches.map((b) => <div key={b.id} className="text-caption"><p>{date(b.created_at)} · {statusLabel(b.status)} · {b.rules_version}</p>{b.error && <p className="mt-1 break-words text-destructive">{b.error}</p>}{b.task_id && <SupervisionRunTranscript batchId={b.id} />}</div>)}</div></details>
    </div>}
  </SheetContent></Sheet>;
}

function FindingCard({ finding: f, canManage, evidenceHref, issueId, assigneeType, assigneeId }: { finding: SupervisionFinding; canManage: boolean; evidenceHref: (run?: string, source?: string, kind?: string, batch?: string) => string; issueId: string; assigneeType: string | null; assigneeId: string | null }) {
  const { t } = useT("run-reviews"); const wsId = useWorkspaceId(); const queryClient = useQueryClient(); const [feedback, setFeedback] = useState(false);
  const [reason, setReason] = useState("");
  const update = useMutation({ mutationFn: ({ status, snoozed_until }: { status: string; snoozed_until?: string }) => api.updateSupervisionFinding(f.id, { status, reason, snoozed_until, operation_id: createSafeId() }), onSuccess: () => { void queryClient.invalidateQueries({ queryKey: supervisionKeys.all(wsId) }); }, onError: (e) => toast.error(errorText(e)) });
  const status = f.status === "open" ? t(($) => $.supervision.open) : f.status === "in_progress" ? t(($) => $.supervision.in_progress) : f.status === "resolved" ? t(($) => $.supervision.resolved) : f.status === "ignored" ? t(($) => $.supervision.ignored) : f.status;
  return <article className="rounded-lg border p-3"><div className="flex flex-wrap items-start justify-between gap-2"><h4 className="min-w-0 break-words text-body font-medium">{f.title}</h4><span className="text-caption text-muted-foreground">{status}</span></div><p className="mt-1 text-caption text-muted-foreground">{f.confidence === "confirmed" ? t(($) => $.supervision.confirmed) : t(($) => $.supervision.risk)}{f.human_resolution && ` · ${t(($) => $.supervision.human_resolved)}`}</p><p className="mt-2 whitespace-pre-wrap text-body">{f.description}</p>{f.snoozed_until && new Date(f.snoozed_until).getTime() > Date.now() && <p className="mt-2 text-caption text-muted-foreground">{t(($) => $.supervision.snoozed_until)} {date(f.snoozed_until)}</p>}
    <details className="mt-3"><summary className="cursor-pointer text-caption underline">{t(($) => $.supervision.evidence_and_impact)}</summary><p className="mt-2 text-body">{f.impact}</p><p className="mt-2 text-body">{f.recommendation}</p><div className="mt-2 flex flex-wrap gap-2">{f.evidence.map((e) => <AppLink key={`${e.kind}:${e.id}`} href={evidenceHref(e.run_id, e.id, e.kind, e.batch_id)} className="rounded border px-2 py-1 text-caption underline">{e.kind} · {e.id.slice(0, 8)}</AppLink>)}</div></details>
    {canManage && <div className="mt-3 space-y-2"><Input aria-label={t(($) => $.supervision.reason)} placeholder={t(($) => $.supervision.reason)} value={reason} onChange={(e) => setReason(e.target.value)} /><div className="flex flex-wrap gap-2"><Button variant="outline" size="sm" disabled={update.isPending} onClick={() => update.mutate({ status: "in_progress" })}>{t(($) => $.supervision.mark_processing)}</Button><Button variant="outline" size="sm" disabled={update.isPending} onClick={() => update.mutate({ status: "resolved" })}>{t(($) => $.supervision.mark_resolved)}</Button><Button variant="ghost" size="sm" disabled={update.isPending || !reason.trim()} onClick={() => update.mutate({ status: "ignored" })}>{t(($) => $.supervision.ignore)}</Button><Button variant="ghost" size="sm" disabled={update.isPending} onClick={() => update.mutate({ status: "open" })}>{t(($) => $.supervision.reopen)}</Button><Button variant="ghost" size="sm" disabled={update.isPending} onClick={() => update.mutate({ status: f.status, snoozed_until: new Date(Date.now() + 3600000).toISOString() })}>{t(($) => $.supervision.snooze)}</Button><Button variant="outline" size="sm" onClick={() => setFeedback(true)}>{t(($) => $.supervision.feedback)}</Button></div></div>}
    <FeedbackDialog open={feedback} close={() => setFeedback(false)} finding={f} issueId={issueId} assigneeType={assigneeType} assigneeId={assigneeId} />
  </article>;
}

function FeedbackDialog({ open, close, finding, issueId, assigneeType, assigneeId }: { open: boolean; close: () => void; finding: SupervisionFinding; issueId: string; assigneeType: string | null; assigneeId: string | null }) {
  const { t } = useT("run-reviews");
  const { t: issuesT } = useT("issues");
  const paths = useWorkspacePaths();
  const mention = assigneeId && (assigneeType === "agent" || assigneeType === "squad") ? `[@负责人](mention://${assigneeType}/${assigneeId})\n\n` : "";
  const [content, setContent] = useState(() => {
    const references = finding.evidence.map((e) => { const query = new URLSearchParams({ tab: "records", issue: issueId, evidence_kind: e.kind, evidence_id: e.id }); if (e.run_id) query.set("task", e.run_id); if (e.batch_id) query.set("evidence_batch", e.batch_id); return `[${e.kind} · ${e.id.slice(0, 8)}](${paths.runReviews()}?${query})`; }).join("\n");
    return `${mention}${finding.title}\n\n${finding.description}\n\n证据：\n${references}\n\n影响：${finding.impact}\n\n预期结果：${finding.recommendation}`;
  });
  const [operation, setOperation] = useState(createSafeId);
  const [previewedContent, setPreviewedContent] = useState("");
  const preview = useMutation({ mutationFn: () => api.previewCommentTriggers(issueId, content), onSuccess: () => setPreviewedContent(content), onError: (e) => toast.error(errorText(e)) });
  const send = useMutation({ mutationFn: () => api.sendSupervisionFeedback(finding.id, { content, operation_id: operation }), onError: (e) => toast.error(errorText(e)) });
  return <Dialog open={open} onOpenChange={(value) => { if (!value && !send.isPending) close(); }}><DialogContent className="max-h-[90dvh] overflow-y-auto"><DialogTitle>{t(($) => $.supervision.feedback)}</DialogTitle><DialogDescription>{t(($) => $.supervision.feedback_hint)}</DialogDescription>
    <label className="space-y-2 text-body"><span>{t(($) => $.supervision.feedback_content)}</span><textarea className="min-h-52 w-full rounded-md border bg-background p-3" value={content} disabled={send.isPending || send.isSuccess || (send.isError && !(send.error instanceof ApiError && send.error.status >= 400 && send.error.status < 500))} onChange={(e) => { setContent(e.target.value); if (send.isError) { send.reset(); setOperation(createSafeId()); } }} /></label>
    {preview.data && previewedContent === content && <div className="text-caption"><p>{t(($) => $.supervision.feedback_targets, { count: preview.data.agents.length })}</p><ul className="mt-1 space-y-1">{preview.data.agents.map((agent) => <li key={agent.id}>{agent.name}</li>)}</ul>{preview.data.blocked && preview.data.blocked.length > 0 && <p className="text-warning">{t(($) => $.supervision.trigger_blocked)}</p>}</div>}
    {send.data && <div role="status" className="text-body"><p>{send.data.pending ? t(($) => $.supervision.feedback_pending) : send.data.reconciled ? t(($) => $.supervision.feedback_unknown) : t(($) => $.supervision.feedback_sent)}</p>{send.data.trigger_outcomes?.map((outcome) => <p key={outcome.target_id} className="mt-1 text-caption">{outcome.status === "queued" ? t(($) => $.supervision.feedback_queued) : outcome.status === "coalesced" ? t(($) => $.supervision.feedback_coalesced) : outcome.status === "deferred" ? t(($) => $.supervision.feedback_deferred) : outcome.status === "blocked" ? blockedReasonLabel(outcome.reason_code, issuesT) : t(($) => $.supervision.feedback_unknown)}</p>)}</div>}
    {send.isError && <p role="alert" className="text-destructive">{errorText(send.error)}</p>}
    <div className="flex flex-wrap justify-end gap-2"><Button variant="ghost" disabled={send.isPending} onClick={close}>{t(($) => $.supervision.cancel)}</Button><Button variant="outline" disabled={preview.isPending || send.isPending || send.isSuccess} onClick={() => preview.mutate()}>{t(($) => $.supervision.preview_feedback)}</Button><Button disabled={!content.trim() || previewedContent !== content || send.isPending || (send.isSuccess && !send.data.pending)} onClick={() => send.mutate()}>{send.data?.pending ? t(($) => $.supervision.query_receipt) : t(($) => $.supervision.send_feedback)}</Button></div>
  </DialogContent></Dialog>;
}

export function SupervisionEvidencePanel() {
  const { t } = useT("run-reviews"); const wsId = useWorkspaceId(); const nav = useNavigation();
  const kind = nav.searchParams.get("evidence_kind") ?? ""; const id = nav.searchParams.get("evidence_id") ?? ""; const batch = nav.searchParams.get("evidence_batch") ?? "";
  const [offset, setOffset] = useState(0);
  const evidence = useQuery({ queryKey: [...supervisionKeys.all(wsId), "evidence", kind, id, batch, offset], queryFn: () => api.getSupervisionEvidence(kind, id, offset, batch), enabled: Boolean(kind && id) });
  if (!kind || !id) return null;
  return <section aria-label={t(($) => $.supervision.evidence_panel)} className="mx-4 my-3 max-h-[45vh] shrink-0 overflow-y-auto rounded-lg border border-info/40 bg-info/5 p-3 sm:mx-6">
    <h2 className="text-body font-medium">{t(($) => $.supervision.evidence_panel)}</h2><p className="mt-1 break-all font-mono text-caption text-muted-foreground">{kind} · {id}{evidence.data?.run_id && ` · ${evidence.data.run_id}`}</p>
    {evidence.isPending ? <p role="status" className="mt-3">{t(($) => $.supervision.loading)}</p> : evidence.isError ? <div role="alert" className="mt-3"><p>{t(($) => $.supervision.evidence_unavailable)}</p><p className="text-caption">{errorText(evidence.error)}</p></div> : evidence.data?.deleted ? <p role="status" className="mt-3">{t(($) => $.supervision.evidence_unavailable)}</p> : <><p className="mt-3 text-caption text-muted-foreground">{evidence.data?.snapshot === true ? t(($) => $.supervision.evidence_snapshot) : kind !== "investigation" ? t(($) => $.supervision.evidence_current) : null}</p><pre className="mt-3 whitespace-pre-wrap break-all text-caption">{evidence.data?.text}</pre><div className="mt-3 flex flex-wrap items-center gap-2 text-caption tabular-nums"><span>{evidence.data?.offset ?? offset}–{evidence.data?.end} / {evidence.data?.length}</span><Button variant="outline" size="sm" disabled={evidence.data?.snapshot === true || offset === 0} onClick={() => setOffset(Math.max(0, offset - 8000))}>{t(($) => $.supervision.previous)}</Button><Button variant="outline" size="sm" disabled={!evidence.data || evidence.data.snapshot === true || evidence.data.end >= evidence.data.length} onClick={() => setOffset(evidence.data?.end ?? offset)}>{t(($) => $.supervision.next)}</Button></div></>}
  </section>;
}

function SupervisionRunTranscript({ batchId }: { batchId: string }) {
  const { t } = useT("run-reviews"); const wsId = useWorkspaceId(); const [open, setOpen] = useState(false);
  const task = useQuery({ queryKey: [...supervisionKeys.all(wsId), "batch-task", batchId], queryFn: () => api.getSupervisionBatchTask(batchId), enabled: open, refetchInterval: (query) => open && query.state.data && isActiveAgentTaskStatus(query.state.data.status) ? 5000 : false });
  const messages = useQuery({ ...taskMessagesOptions(task.data?.id ?? ""), enabled: open && Boolean(task.data), refetchInterval: open && task.data && isActiveAgentTaskStatus(task.data.status) ? 3000 : false });
  const items = useMemo(() => buildTimeline(messages.data ?? []), [messages.data]);
  const failed = task.error ?? messages.error;
  return <><Button variant="link" size="sm" onClick={() => setOpen(true)} disabled={open && (task.isPending || messages.isPending)}>{t(($) => $.supervision.view_run)}</Button>{open && failed && <p role="alert" className="text-destructive">{errorText(failed)}<Button variant="ghost" onClick={() => { void task.refetch(); void messages.refetch(); }}>{t(($) => $.supervision.retry)}</Button></p>}{task.data && messages.data && <AgentTranscriptDialog task={task.data} agentName={t(($) => $.supervision.agent)} items={items} open={open} onOpenChange={setOpen} isLive={isActiveAgentTaskStatus(task.data.status)} />}</>;
}
