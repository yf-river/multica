"use client";
import { useEffect } from "react";
import { Plus, Upload, Pencil } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { useChatStore } from "@multica/core/chat";
import { useHealth, useHealthReportHistory, useHealthViewStore, EMPTY_HEALTH_VIEW, healthMetricKey, isHealthAbnormal, type HealthObservation, type HealthReport } from "@multica/core/health";
import { useWorkspaceId, useWorkspacePaths } from "@multica/core/paths";
import { agentListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from "@multica/ui/components/ui/dialog";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@multica/ui/components/ui/sheet";
import { PageHeader } from "../layout/page-header";
import { AppLink, useNavigation } from "../navigation";
import { RichContent } from "../rich-content/rich-content";
import { useT } from "../i18n";
import { ProfileEditor, ObservationEditor, Trend, MutationError, revisionDifferences } from "./health-editors";

type Tab = "overview" | "reports" | "revisions";
const tabs: Tab[] = ["overview", "reports", "revisions"];
const priority = ["一般检查", "内科", "血常规", "尿常规", "生化检测", "超声", "心电图"];
const daily = [
  { label: "height", codes: ["height"], preset: "height", updateable: true },
  { label: "weight", codes: ["weight"], preset: "weight", updateable: true },
  { label: "bmi", codes: ["bmi"], preset: "bmi", updateable: false },
  { label: "pulse", codes: ["脉搏", "resting_heart_rate"], preset: "heart_rate", updateable: true },
] as const;

export function HealthPage() {
  const { t } = useT("health");
  const wsId = useWorkspaceId();
  const userId = useAuthStore((s) => s.user?.id) || "";
  const key = `${wsId}:${userId}`;
  const view = useHealthViewStore((s) => s.views[key] || EMPTY_HEALTH_VIEW);
  const update = useHealthViewStore((s) => s.update);
  const h = useHealth(wsId);
  const data = h.overview.data;
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const reportId = navigation.searchParams.get("report");
  const rawTab = navigation.searchParams.get("tab");
  const tab: Tab = reportId ? "reports" : tabs.find((item) => item === rawTab) || "overview";
  const reports = h.reports.data?.items || [];
  const selectedReport = reports.find((r) => r.id === reportId);
  const agents = useQuery(agentListOptions(wsId));
  const selectedAgentId = useChatStore((s) => s.selectedAgentId);
  const available = (agents.data || []).filter((a) => !a.archived_at && a.runtime_id);
  const agentId = available.find((a) => a.id === selectedAgentId)?.id || available[0]?.id || "";
  const history = useHealthReportHistory(wsId, selectedReport?.id || "");
  const result = history.data?.items.find((r) => r.attempt === view.extractionAttempt)?.result || selectedReport?.result;
  useEffect(() => { update(key, { extractionAttempt: null }); }, [key, selectedReport?.id, update]);
  const all = data?.current || [];
  const categories = [...new Set(all.map((o) => o.category))].sort((a,b) => {
    const aa = priority.indexOf(a), bb = priority.indexOf(b);
    return (aa < 0 ? 100 : aa) - (bb < 0 ? 100 : bb) || a.localeCompare(b);
  });
  const category = categories.includes(view.category) ? view.category : "";
  const filtered = all.filter((o) => (!category || o.category === category) && (!view.onlyAbnormal || isHealthAbnormal(o)) && [o.name, o.code, o.category].join(" ").toLowerCase().includes(view.search.toLowerCase()));
  const pressureDetail = view.detailId === "blood_pressure";
  const detail = pressureDetail ? undefined : data?.observations.find((o) => o.id === view.detailId) || result?.observations.find((o) => o.id === view.detailId);
  const editing = view.editorId === "new" ? {} : data?.observations.find((o) => o.id === view.editorId);
  const target = h.revisions.data?.items.find((r) => r.revision === view.targetRevision);
  const error = h.profile.error || h.observation.error || h.observations.error || h.upload.error || h.process.error || h.restore.error;
  const openRecord = (preset?: string, o?: HealthObservation) => update(key, { editorId: o?.id || "new", editorPreset: preset || null, editorMode: preset || o ? "update" : "new", editorRevision: data?.revision || 0, detailId: null });
  const openCorrection = (o: HealthObservation, preset?: string) => update(key, { editorId: o.id, editorPreset: preset || null, editorMode: "correct", editorRevision: data?.revision || 0, detailId: null });
  const openReport = (id: string) => navigation.push(paths.healthReport(id));
  const upload = async (file: File) => {
    const saved = await h.upload.mutateAsync(file); openReport(saved.id);
    if (agentId) await h.process.mutateAsync({ id: saved.id, agentId });
  };
  const systolic = data?.current.find((o) => o.code === "systolic_pressure");
  const diastolic = data?.current.find((o) => o.code === "diastolic_pressure");
  const bloodPressure = systolic || diastolic;
  const pressureRows = pairBloodPressure(data?.observations || []);
  const recentImport = reports[0]?.createdAt;
  const person = [data?.profile.name, data?.profile.sex, data?.age == null ? "" : t(($) => $.overview.age, { age: data.age })].filter(Boolean).join(" · ");

  return <div className="flex h-full min-h-0 flex-col">
    <PageHeader><h1 className="text-body font-medium">{t(($) => $.page.title)}</h1></PageHeader>
    <main className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto max-w-6xl space-y-6 px-4 py-6 md:px-8">
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div><h2 className="text-display-sm font-semibold tracking-tight">{t(($) => $.page.title)}</h2>
            <div className="mt-2 flex flex-col gap-1 text-caption sm:flex-row sm:flex-wrap sm:gap-x-5">
              <span className="font-medium text-foreground">{person || t(($) => $.profile.incomplete)}</span>
              <div className="flex flex-wrap gap-x-5 gap-y-1 text-muted-foreground">
                {recentImport && <span>{t(($) => $.overview.imported)} {formatDate(recentImport)}</span>}
                <span>{t(($) => $.overview.reports, { count: reports.length })}</span><span>{t(($) => $.page.revision, { revision: data?.revision || 0 })}</span>
              </div>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="ghost" size="sm" onClick={() => update(key, { profileOpen: true, profileRevision: data?.revision || 0 })}><Pencil />{t(($) => $.profile.edit)}</Button>
            <Button variant="outline" size="sm" onClick={() => openRecord()}><Plus />{t(($) => $.record.add)}</Button>
            <label className="inline-flex cursor-pointer items-center gap-2 rounded-md bg-primary px-3 py-2 text-body font-medium text-primary-foreground"><Upload className="size-4" />{h.upload.isPending ? t(($) => $.report.uploading) : t(($) => $.report.upload)}
              <input className="sr-only" aria-label={t(($) => $.report.upload)} type="file" accept="application/pdf,.pdf" disabled={h.upload.isPending} onChange={(e) => { const file=e.target.files?.[0]; e.target.value=""; if(file) void upload(file).catch(() => {}); }} />
            </label>
          </div>
        </div>
        <nav className="flex gap-6 overflow-x-auto border-b" aria-label={t(($) => $.page.title)}>{tabs.map((item) => <button key={item} type="button" aria-current={tab === item ? "page" : undefined} onClick={() => navigation.push(`${paths.health()}?tab=${item}`)} className={`whitespace-nowrap border-b-2 pb-3 text-body focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring ${tab === item ? "border-primary font-semibold" : "border-transparent text-muted-foreground hover:text-foreground"}`}>{t(($) => $.page[item])}</button>)}</nav>
        {error && <MutationError error={error} />}
        {h.overview.isPending && <p className="text-body text-muted-foreground">{t(($) => $.page.loading)}</p>}
        {(h.overview.isError || data?.available === false) && <p role="alert" className="text-body text-destructive">{t(($) => $.page.failed)} <Button variant="outline" onClick={() => void h.invalidate()}>{t(($) => $.page.retry)}</Button></p>}
        {data?.available === true && <>
          {tab === "overview" && <>
            <section aria-label={t(($) => $.overview.daily)} className="grid grid-cols-2 overflow-hidden rounded-lg border bg-surface md:grid-cols-3 xl:grid-cols-5">
              {daily.map((spec) => { const o = data.current.find((item) => (spec.codes as readonly string[]).includes(item.code)); return <DailyMetricCard key={spec.label} label={t(($) => $.overview[spec.label])} observation={o} updateable={spec.updateable} onDetail={() => o && update(key, { detailId: o.id })} onUpdate={() => openRecord(spec.preset, o)} />; })}
              <DailyMetricCard label={t(($) => $.overview.pressure)} observation={bloodPressure} value={bloodPressure ? `${systolic?.value || "—"}/${diastolic?.value || "—"}` : undefined} unit={systolic?.unit || diastolic?.unit} abnormal={!!((systolic && isHealthAbnormal(systolic)) || (diastolic && isHealthAbnormal(diastolic)))} updateable onDetail={() => bloodPressure && update(key, { detailId: "blood_pressure" })} onUpdate={() => openRecord("blood_pressure", systolic)} />
            </section>
            <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="text-title font-semibold">{t(($) => $.overview.all)} <span className="ml-2 text-caption font-normal text-muted-foreground">{t(($) => $.record.items, { count: all.length })}</span></h2>
              <div className="flex flex-wrap gap-2"><Input aria-label={t(($) => $.overview.search)} placeholder={t(($) => $.overview.search_placeholder)} value={view.search} onChange={(e) => update(key, { search: e.target.value })} className="w-56 max-w-full text-title-sm md:text-body" /><Button variant={view.onlyAbnormal ? "default" : "outline"} aria-pressed={view.onlyAbnormal} onClick={() => update(key, { onlyAbnormal: !view.onlyAbnormal })}>{t(($) => $.overview.abnormal)}</Button></div>
            </div>
            <div className="grid items-start gap-5 md:grid-cols-[164px_minmax(0,1fr)]">
              <nav className="flex gap-1 overflow-x-auto md:sticky md:top-4 md:flex-col" aria-label={t(($) => $.record.category)}>{["", ...categories].map((item) => <button key={item} type="button" aria-pressed={category === item} onClick={() => update(key, { category: item })} className={`flex shrink-0 items-center justify-between gap-4 rounded-md px-3 py-2 text-left text-body focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring ${category === item ? "bg-surface-selected font-semibold" : "hover:bg-surface-hover"}`}><span>{item || t(($) => $.overview.all)}</span><span className="text-caption font-normal text-muted-foreground">{all.filter((o) => !item || o.category === item).length}</span></button>)}</nav>
              <MetricTable items={filtered} onDetail={(o) => update(key, { detailId: o.id })} />
            </div>
            {tab === "overview" && reports[0] && <section className="flex items-center justify-between gap-4 rounded-lg bg-muted px-5 py-4"><div className="min-w-0"><p className="text-caption text-muted-foreground">{t(($) => $.overview.recent_report)}</p><h3 className="mt-1 break-words text-body font-medium">{reports[0].filename}</h3></div><AppLink href={paths.healthReport(reports[0].id)} className="shrink-0 text-body font-medium underline underline-offset-4">{t(($) => $.overview.read_analysis)}</AppLink></section>}
          </>}
          {tab === "reports" && <>
            {available.length === 0 && <p className="text-body text-muted-foreground">{t(($) => $.report.no_agent)} <AppLink href={paths.newAgent()} className="underline">{t(($) => $.report.create_assistant)}</AppLink></p>}
            {reports.length === 0 ? <p className="text-body text-muted-foreground">{t(($) => $.report.empty)}</p> : !selectedReport ? <nav className="divide-y overflow-hidden rounded-lg border bg-surface" aria-label={t(($) => $.page.reports)}>{reports.map((r) => <AppLink key={r.id} href={paths.healthReport(r.id)} className="flex flex-wrap items-center justify-between gap-3 p-4 hover:bg-surface-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring md:p-5"><div className="min-w-0"><h3 className="break-words text-body font-medium">{r.filename}</h3><p className="mt-1 text-caption text-muted-foreground">{formatDate(r.result?.examDate || r.createdAt)} · {t(($) => $.report.pages, { count: r.pageCount })}</p></div><p className="text-caption text-muted-foreground">{reportStatus(r, t)}</p></AppLink>)}</nav> :
              <section className="min-w-0 space-y-5 rounded-lg border bg-surface p-5 md:p-6">
                <AppLink href={`${paths.health()}?tab=reports`} className="inline-block text-caption text-muted-foreground underline underline-offset-4">{t(($) => $.report.back)}</AppLink>
                <div className="flex flex-wrap justify-between gap-3"><div className="min-w-0"><p className="text-caption text-muted-foreground">{result?.institution}</p><h2 className="mt-1 break-words text-title font-semibold">{selectedReport.filename}</h2></div><div className="flex flex-wrap gap-2">
                  <Button variant="outline" disabled={!agentId || h.process.isPending || ["running","queued","claimed","deferred"].includes(selectedReport.taskStatus)} onClick={() => h.process.mutate({ id: selectedReport.id, agentId, reprocess: !!selectedReport.result })}>{selectedReport.result ? t(($) => $.report.reprocess) : t(($) => $.report.process)}</Button>
                </div></div>
                {history.data?.items.length ? <label className="flex flex-wrap items-center gap-3 text-caption text-muted-foreground">{t(($) => $.report.extraction_history)}<select className="max-w-full rounded-md border bg-background p-2 text-body text-foreground" value={view.extractionAttempt || selectedReport.attempt} onChange={(e) => update(key, { extractionAttempt: Number(e.target.value) })}>{history.data.items.map((r) => <option key={r.attempt} value={r.attempt}>{t(($) => $.report.extraction_version, { attempt: r.attempt, revision: r.revision })}</option>)}</select></label> : null}
                <nav className="flex gap-5 overflow-x-auto border-b" aria-label={t(($) => $.report.detail)}>{(["analysis", "metrics", "original"] as const).map((item) => <button key={item} type="button" onClick={() => update(key, { reportTab: item })} aria-current={view.reportTab === item ? "page" : undefined} className={`whitespace-nowrap border-b-2 pb-3 text-body ${view.reportTab === item ? "border-primary font-medium" : "border-transparent text-muted-foreground"}`}>{t(($) => $.report[item])}</button>)}</nav>
                {view.reportTab === "analysis" && <>
                  <h3 className="text-title-sm font-medium">{t(($) => $.report.analysis)}</h3>{result?.analysis ? <RichContent content={result.analysis} /> : <p className="text-body text-muted-foreground">{t(($) => $.report.analysis_pending)}</p>}
                  {result && <p className="text-caption text-muted-foreground">{t(($) => $.report.analysis_version, { revision: result.contextRevision })}{result.contextRevision !== data.revision ? ` · ${t(($) => $.report.stale)}` : ""}</p>}
                  {result?.conclusions && <details className="border-t pt-4"><summary className="cursor-pointer text-body">{t(($) => $.report.conclusions)}</summary><p className="mt-3 whitespace-pre-wrap text-body">{result.conclusions}</p></details>}
                </>}
                {view.reportTab === "metrics" && <MetricTable items={result?.observations || []} onDetail={(o) => update(key, { detailId: o.id })} />}
                {view.reportTab === "original" && <div className="flex flex-wrap gap-4"><a className="text-body underline" target="_blank" rel="noopener noreferrer" href={reportFile(selectedReport.id, wsId)}>{t(($) => $.report.original)}</a><a className="text-body underline" href={`${reportFile(selectedReport.id, wsId)}&download=1`}>{t(($) => $.report.download)}</a></div>}
                {result?.pages.length ? <details className="border-t pt-4"><summary className="cursor-pointer text-body text-muted-foreground">{t(($) => $.report.coverage)} · {t(($) => $.report.pages, { count: result.pages.length })}</summary><div className="mt-3 space-y-3">{result.pages.map((page) => <p key={page.page} className="text-caption text-muted-foreground"><a className="underline" target="_blank" rel="noopener noreferrer" href={`${reportFile(selectedReport.id, wsId)}#page=${page.page}`}>{t(($) => $.report.page, { page: page.page })}</a> · {page.note}</p>)}</div></details> : null}
                {selectedReport.issueId && <AppLink href={paths.issueDetail(selectedReport.issueId)} className="inline-block text-caption underline">{t(($) => $.report.task)}</AppLink>}
              </section>}
          </>}
          {tab === "revisions" && <section><h2 className="text-title font-semibold">{t(($) => $.page.revisions)}</h2><p className="mt-2 text-body text-muted-foreground">{t(($) => $.revision.description)}</p>
            <div className="mt-5 divide-y rounded-lg border bg-surface">{(h.revisions.data?.items || []).map((r) => <div key={r.revision} className="flex flex-wrap items-center justify-between gap-4 p-5"><div><h3 className="text-body font-medium">{t(($) => $.revision.version, { revision: r.revision })} · {r.reason}</h3><p className="mt-1 text-caption text-muted-foreground">{formatDate(r.createdAt)} · {t(($) => $.overview.records, { count: r.state.currentObservationIds.length })}</p></div><Button variant="outline" onClick={() => update(key, { targetRevision: r.revision })}>{t(($) => $.revision.view)}</Button></div>)}</div>
          </section>}
        </>}
      </div>
    </main>
    {view.profileOpen && data && <ProfileEditor open value={data.profile} revision={view.profileRevision} h={h} onClose={() => update(key, { profileOpen: false })} />}
    {view.editorId && <ObservationEditor value={editing || {}} pairedValue={view.editorPreset === "blood_pressure" ? diastolic : undefined} preset={view.editorPreset} mode={view.editorMode} revision={view.editorRevision} h={h} onClose={() => update(key, { editorId: null, editorPreset: null })} />}
    <Sheet open={!!detail || pressureDetail} onOpenChange={(open) => { if(!open) update(key, { detailId:null }); }}><SheetContent className="w-full overflow-y-auto data-[side=right]:sm:max-w-xl"><SheetHeader><SheetTitle>{pressureDetail ? t(($) => $.overview.pressure) : detail?.name}</SheetTitle><SheetDescription>{pressureDetail ? `${t(($) => $.record.history_list)} · ${pressureRows.length}` : detail && `${detail.category} · ${formatDate(detail.observedAt)}`}</SheetDescription></SheetHeader><div className="space-y-5 px-4 pb-6">
      {pressureDetail && <><p className={`whitespace-nowrap text-display font-semibold tabular-nums ${(systolic && isHealthAbnormal(systolic)) || (diastolic && isHealthAbnormal(diastolic)) ? "text-destructive" : ""}`}>{systolic?.value || "—"}/{diastolic?.value || "—"} <span className="text-body font-normal text-muted-foreground">{t(($) => $.overview.unit_mmhg)}</span></p><div className="grid gap-5 sm:grid-cols-2"><div><h3 className="text-title-sm font-medium">{t(($) => $.record.systolic)}</h3><Trend observations={(data?.observations || []).filter((o) => o.code === "systolic_pressure")} /></div><div><h3 className="text-title-sm font-medium">{t(($) => $.record.diastolic)}</h3><Trend observations={(data?.observations || []).filter((o) => o.code === "diastolic_pressure")} /></div></div><div><h3 className="text-title-sm font-medium">{t(($) => $.record.history_list)}</h3><div className="mt-2 divide-y rounded-md border">{pressureRows.map((row) => <div key={row.key} className="flex items-center justify-between gap-4 px-3 py-3 text-body"><span className="font-medium tabular-nums">{row.systolic?.value || "—"}/{row.diastolic?.value || "—"} <span className="text-caption font-normal text-muted-foreground">{t(($) => $.overview.unit_mmhg)}</span></span><span className="text-caption text-muted-foreground">{formatDate(row.observedAt)}</span></div>)}</div></div><div className="sticky bottom-0 flex flex-wrap gap-2 border-t bg-background py-3"><Button onClick={() => openRecord("blood_pressure", systolic)}>{t(($) => $.record.update_metric, { name: t(($) => $.overview.pressure) })}</Button>{systolic && diastolic && <Button variant="outline" onClick={() => openCorrection(systolic, "blood_pressure")}>{t(($) => $.record.correct_metric)}</Button>}</div></>}
      {detail && <><p className={`text-display font-semibold tabular-nums ${isHealthAbnormal(detail) ? "text-destructive" : ""}`}>{detail.value} <span className="text-body font-normal text-muted-foreground">{detail.unit}</span></p><dl className="grid grid-cols-[84px_minmax(0,1fr)] gap-3 text-body"><dt className="text-muted-foreground">{t(($) => $.record.range)}</dt><dd>{detail.referenceRange || t(($) => $.page.unrecorded)}</dd><dt className="text-muted-foreground">{t(($) => $.record.method)}</dt><dd>{[detail.method,detail.specimen,detail.site].filter(Boolean).join(" / ") || t(($) => $.page.unrecorded)}</dd></dl><h3 className="text-title-sm font-medium">{t(($) => $.record.trend)}</h3><Trend observations={(data?.observations || []).filter((o) => healthMetricKey(o) === healthMetricKey(detail))} /><div><h3 className="text-title-sm font-medium">{t(($) => $.record.history_list)}</h3><div className="mt-2 divide-y rounded-md border">{(data?.observations || []).filter((o) => healthMetricKey(o) === healthMetricKey(detail)).sort((a,b) => b.observedAt.localeCompare(a.observedAt)).map((o) => <div key={o.id} className="flex items-center justify-between gap-4 px-3 py-3 text-body"><span className="font-medium tabular-nums">{o.value} <span className="text-caption font-normal text-muted-foreground">{o.unit}</span></span><span className="text-caption text-muted-foreground">{formatDate(o.observedAt)}</span></div>)}</div></div>{detail.uncertainty && <div className="border-t pt-4"><h3 className="text-body font-medium">{t(($) => $.record.source_note)}</h3><p className="mt-2 text-caption text-muted-foreground">{detail.uncertainty}</p></div>}{detail.evidence && <details className="border-t pt-4"><summary className="cursor-pointer text-body">{t(($) => $.record.evidence)}</summary><p className="mt-3 whitespace-pre-wrap break-words text-caption text-muted-foreground">{detail.evidence}</p></details>}<div className="sticky bottom-0 flex flex-wrap gap-2 border-t bg-background py-3"><Button onClick={() => openRecord(presetFor(detail), detail)}>{t(($) => $.record.update_metric, { name: detail.name })}</Button>{data?.observations.some((o) => o.id === detail.id) && <Button variant="outline" onClick={() => openCorrection(detail)}>{t(($) => $.record.correct_metric)}</Button>}{detail.reportId && <AppLink className="self-center text-caption underline" href={paths.healthReport(detail.reportId)}>{t(($) => $.record.report, { page: detail.page })}</AppLink>}</div></>}
    </div></SheetContent></Sheet>
    <Dialog open={!!target} onOpenChange={(open) => { if(!open) update(key, { targetRevision:null }); }}><DialogContent className="max-h-[85dvh] overflow-y-auto"><DialogHeader><DialogTitle>{t(($) => $.revision.confirm, { target: target?.revision || 0 })}</DialogTitle><DialogDescription>{t(($) => $.revision.confirm_description)}</DialogDescription></DialogHeader>
      {target && <><p className="text-body">{t(($) => $.revision.changes, { current: data?.current.length || 0, target: target.state.currentObservationIds.length })}</p><div className="max-h-56 space-y-2 overflow-y-auto">{revisionDifferences(data?.current || [], target.observations.filter((o) => target.state.currentObservationIds.includes(o.id))).map((change) => <p key={change.id} className="break-words text-body">{change.label}</p>)}</div><MutationError error={h.restore.error} /><Button disabled={h.restore.isPending} onClick={() => void h.restore.mutateAsync({ target:target.revision, revision:data?.revision || 0 }).then(() => update(key, { targetRevision:null })).catch(() => {})}>{t(($) => $.revision.restore_now)}</Button></>}
    </DialogContent></Dialog>
  </div>;
}

function DailyMetricCard({ label, observation, value, unit, abnormal = false, updateable, onDetail, onUpdate }: { label: string; observation?: HealthObservation; value?: string; unit?: string; abnormal?: boolean; updateable: boolean; onDetail: () => void; onUpdate: () => void }) {
  const { t } = useT("health");
  return <div className="relative min-w-0 border-b border-r bg-surface">
    <button type="button" disabled={!observation} onClick={onDetail} aria-label={`${label} · ${t(($) => $.record.detail)}`} className="min-h-40 w-full min-w-0 px-4 py-5 text-left hover:bg-surface-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring disabled:cursor-default">
      <span className="block pr-12 text-caption text-muted-foreground">{label}</span>
      <p className={`mt-2 flex min-w-0 items-baseline gap-1 whitespace-nowrap font-semibold tracking-tight tabular-nums ${abnormal || (observation && isHealthAbnormal(observation)) ? "text-destructive" : ""}`}><span className="text-[clamp(1.75rem,2.2vw,2.25rem)] leading-none">{value || observation?.value || "—"}</span><span className="text-caption font-normal text-muted-foreground">{unit || observation?.unit}</span></p>
      <p className="mt-3 text-caption text-muted-foreground">{observation?.observedAt ? formatDate(observation.observedAt) : t(($) => $.page.unrecorded)}</p>
    </button>
    {updateable && <button type="button" onClick={onUpdate} className="absolute right-2 top-2 min-h-11 rounded-md px-2 text-caption font-medium text-muted-foreground hover:bg-surface-hover hover:text-foreground focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring">{t(($) => $.record.update)}</button>}
  </div>;
}

function presetFor(observation: HealthObservation) {
  if (observation.code === "weight") return "weight";
  if (observation.code === "height") return "height";
  if (observation.code === "bmi") return "bmi";
  if (["脉搏", "resting_heart_rate"].includes(observation.code)) return "heart_rate";
  return undefined;
}

function pairBloodPressure(observations: HealthObservation[]) {
  const groups = new Map<string, { key: string; observedAt: string; systolic?: HealthObservation; diastolic?: HealthObservation }>();
  for (const observation of observations) {
    if (observation.code !== "systolic_pressure" && observation.code !== "diastolic_pressure") continue;
    const key = observation.measurementGroupId || (observation.reportId ? `${observation.reportId}:${observation.page}:${observation.observedAt}` : observation.id);
    const row = groups.get(key) || { key, observedAt: observation.observedAt };
    if (observation.code === "systolic_pressure") row.systolic = observation;
    else row.diastolic = observation;
    groups.set(key, row);
  }
  return [...groups.values()].sort((a,b) => b.observedAt.localeCompare(a.observedAt));
}

function MetricTable({ items, onDetail }: { items: HealthObservation[]; onDetail:(o:HealthObservation)=>void }) {
  const { t } = useT("health");
  if (!items.length) return <p className="py-8 text-body text-muted-foreground">{t(($) => $.overview.no_match)}</p>;
  return <div className="min-w-0 overflow-x-auto rounded-lg border bg-surface"><table className="w-full table-fixed text-left text-body"><thead className="bg-muted text-caption font-normal text-muted-foreground"><tr><th className="w-1/4 px-3 py-3 font-normal">{t(($) => $.record.name)}</th><th className="w-1/4 px-3 py-3 font-normal">{t(($) => $.record.value)}</th><th className="w-[14%] px-3 py-3 font-normal">{t(($) => $.record.unit)}</th><th className="hidden w-[22%] px-3 py-3 font-normal md:table-cell">{t(($) => $.record.range)}</th><th className="w-1/5 px-3 py-3 font-normal">{t(($) => $.record.date)}</th></tr></thead><tbody className="divide-y">{items.map((o) => <tr key={o.id} className="hover:bg-surface-hover"><td className="break-words px-3 py-3"><button type="button" onClick={() => onDetail(o)} className="min-h-8 text-left font-medium underline-offset-4 hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring">{o.name}</button></td><td className={`break-words px-3 py-3 font-medium tabular-nums ${isHealthAbnormal(o) ? "text-destructive" : ""}`}>{o.value}{isHealthAbnormal(o) && <span className="ml-1 text-caption">{o.abnormal}</span>}</td><td className="break-words px-3 py-3 text-caption text-muted-foreground">{o.unit || "—"}</td><td className="hidden break-words px-3 py-3 text-caption text-muted-foreground md:table-cell">{o.referenceRange || "—"}</td><td className="break-words px-3 py-3 text-caption text-muted-foreground">{formatDate(o.observedAt)}</td></tr>)}</tbody></table></div>;
}

function formatDate(value?: string) {
  if(!value) return "—";
  const parsed = new Date(value.length === 10 ? `${value}T12:00:00` : value);
  return Number.isNaN(parsed.getTime()) ? value : new Intl.DateTimeFormat("zh-CN", { year:"numeric",month:"2-digit",day:"2-digit" }).format(parsed);
}
function reportFile(id:string, wsId:string) { return `/api/health/reports/${encodeURIComponent(id)}/file?workspace_id=${encodeURIComponent(wsId)}`; }
function reportStatus(report:HealthReport,t:ReturnType<typeof useT<"health">>["t"]) {
  if(["running","queued","claimed","deferred"].includes(report.taskStatus)) return t(($) => $.report.processing);
  if(report.taskStatus === "failed") return t(($) => $.report.failed);
  if(report.result && !report.result.analysis) return t(($) => $.report.analysis_pending);
  return report.result ? t(($) => $.report.completed) : t(($) => $.report.uploaded);
}
