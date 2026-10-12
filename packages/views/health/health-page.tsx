"use client";

import { useEffect, useRef } from "react";
import { ChevronRight, Pencil, Plus, Upload } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { useChatStore } from "@multica/core/chat";
import {
  EMPTY_HEALTH_VIEW,
  currentDisplayMetrics,
  isHealthAbnormal,
  useHealth,
  useHealthReportHistory,
  useHealthViewStore,
  type HealthObservation,
  type HealthReport,
} from "@multica/core/health";
import { useWorkspaceId, useWorkspacePaths } from "@multica/core/paths";
import { agentListOptions } from "@multica/core/workspace/queries";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { PageHeader } from "../layout/page-header";
import { AppLink, useNavigation } from "../navigation";
import { RichContent } from "../rich-content/rich-content";
import { useT } from "../i18n";
import { MutationError, ObservationEditor, ProfileEditor } from "./health-editors";
import { BMIDetailSheet, MetricDetailSheet } from "./health-detail";
import { RevisionCards, RevisionDiff } from "./health-versions";

type Tab = "overview" | "reports" | "revisions";
type ReportTab = "analysis" | "metrics" | "original";
const tabs: Tab[] = ["overview", "reports", "revisions"];
const reportTabs: ReportTab[] = ["analysis", "metrics", "original"];
const priority = ["一般检查", "内科", "血常规", "尿常规", "生化检测", "超声", "心电图"];
const daily = [
  { label: "height", codes: ["height"], preset: "height", recordable: false },
  { label: "weight", codes: ["weight"], preset: "weight", recordable: true },
  { label: "bmi", codes: ["bmi"], preset: "bmi", recordable: false },
  { label: "pulse", codes: ["脉搏", "resting_heart_rate"], preset: "heart_rate", recordable: true },
] as const;

export function HealthPage() {
  const { t } = useT("health");
  const wsId = useWorkspaceId();
  const userId = useAuthStore((state) => state.user?.id) || "";
  const key = `${wsId}:${userId}`;
  const view = useHealthViewStore((state) => state.views[key] || EMPTY_HEALTH_VIEW);
  const update = useHealthViewStore((state) => state.update);
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const fileInput = useRef<HTMLInputElement>(null);
  const reportId = navigation.searchParams.get("report");
  const rawTab = navigation.searchParams.get("tab");
  const tab: Tab = reportId ? "reports" : tabs.find((item) => item === rawTab) || "overview";
  const h = useHealth(wsId, { revisionsEnabled: tab === "revisions" });
  const data = h.overview.data;
  const reports = h.reports.data?.items || [];
  const selectedReport = reports.find((report) => report.id === reportId);
  const history = useHealthReportHistory(wsId, selectedReport?.id || "");
  const selectedAgentId = useChatStore((state) => state.selectedAgentId);
  const agents = useQuery(agentListOptions(wsId));
  const availableAgents = (agents.data || []).filter((agent) => !agent.archived_at && agent.runtime_id);
  const agentId = availableAgents.find((agent) => agent.id === selectedAgentId)?.id || availableAgents[0]?.id || "";
  const selectedExtraction = history.data?.items.find((item) => item.attempt === view.extractionAttempt);
  const result = selectedExtraction?.result || selectedReport?.result;

  useEffect(() => {
    update(key, { extractionAttempt: null });
  }, [key, selectedReport?.id, update]);

  const all = currentDisplayMetrics(data?.current || [], data?.computedBMI || null);
  const categories = [...new Set(all.map((observation) => observation.category))].sort((a, b) => {
    const first = priority.indexOf(a);
    const second = priority.indexOf(b);
    return (first < 0 ? 100 : first) - (second < 0 ? 100 : second) || a.localeCompare(b);
  });
  const categoryParam = navigation.searchParams.get("category");
  const defaultCategory = categories.includes("一般检查") ? "一般检查" : categories[0] || "";
  const category = categoryParam === "all" ? "" : categories.includes(categoryParam || "") ? categoryParam || "" : defaultCategory;
  const search = navigation.searchParams.get("q") || "";
  const onlyAbnormal = navigation.searchParams.get("abnormal") === "1";
  const filtered = all.filter((observation) =>
    (!category || observation.category === category)
    && (!onlyAbnormal || isHealthAbnormal(observation))
    && [observation.name, observation.code, observation.category].join(" ").toLowerCase().includes(search.toLowerCase()),
  );
  const metricId = navigation.searchParams.get("metric");
  const pressureDetail = metricId === "blood_pressure";
  const detail = pressureDetail || metricId === "computed-bmi" ? undefined : data?.observations.find((observation) => observation.id === metricId) || result?.observations.find((observation) => observation.id === metricId);
  const editingObservation = data?.observations.find((observation) => observation.id === view.editorId);
  const editing: Partial<HealthObservation> = view.editorId === "new" ? {} : editingObservation || {};
  const systolic = data?.current.find((observation) => observation.code === "systolic_pressure");
  const diastolic = data?.current.find((observation) => observation.code === "diastolic_pressure");
  const pressureRows = pairBloodPressure(data?.observations || []);
  const editingPair = view.editorPreset === "blood_pressure"
    ? view.editorMode === "correct" && editingObservation ? findPressurePair(editingObservation, data?.observations || []) : diastolic
    : undefined;
  const target = h.revisions.data?.items.find((revision) => revision.revision === view.targetRevision);
  const recentImport = reports[0]?.createdAt;
  const person = [data?.profile.name, data?.profile.sex, data?.age == null ? "" : t(($) => $.overview.age, { age: data.age })].filter(Boolean).join(" · ");
  const reportTabParam = navigation.searchParams.get("reportTab");
  const reportTab: ReportTab = reportTabs.find((item) => item === reportTabParam) || "analysis";
  const queryError = h.overview.error || h.reports.error || (tab === "revisions" ? h.revisions.error : null);
  const mutationError = h.profile.error || h.observation.error || h.observations.error || h.upload.error || h.process.error || h.restore.error;

  const href = (patch: Record<string, string | null>, clear: string[] = []) => {
    const params = new URLSearchParams(navigation.searchParams);
    for (const name of clear) params.delete(name);
    for (const [name, value] of Object.entries(patch)) {
      if (value) params.set(name, value);
      else params.delete(name);
    }
    const query = params.toString();
    return `${paths.health()}${query ? `?${query}` : ""}`;
  };
  const overviewHref = href({ tab: "overview" }, ["report", "reportTab", "metric"]);
  const reportsHref = href({ tab: "reports" }, ["report", "reportTab", "metric", "q", "category", "abnormal"]);
  const revisionsHref = href({ tab: "revisions" }, ["report", "reportTab", "metric", "q", "category", "abnormal"]);
  const closeMetric = () => navigation.replace(href({ metric: null }));
  const openMetric = (id: string) => navigation.push(href({ metric: id }));
  const openRecord = (preset?: string, observation?: HealthObservation) => {
    closeMetric();
    update(key, { editorId: observation?.id || "new", editorPreset: preset || null, editorMode: preset || observation ? "update" : "new", editorRevision: data?.revision || 0 });
  };
  const openCorrection = (observation: HealthObservation, preset?: string) => {
    closeMetric();
    update(key, { editorId: observation.id, editorPreset: preset || null, editorMode: "correct", editorRevision: data?.revision || 0 });
  };
  const upload = async (file: File) => {
    const saved = await h.upload.mutateAsync(file);
    navigation.push(paths.healthReport(saved.id));
    if (agentId) await h.process.mutateAsync({ id: saved.id, agentId });
  };

  return <div className="flex h-full min-h-0 flex-col">
    <PageHeader><h1 className="text-body font-medium">{t(($) => $.page.title)}</h1></PageHeader>
    <main className="min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto max-w-6xl space-y-8 px-4 py-6 md:px-8 md:py-8">
        <header className="flex flex-wrap items-end justify-between gap-4">
          <div className="min-w-0">
            <h2 className="text-balance text-display-sm font-semibold tracking-tight">{t(($) => $.page.title)}</h2>
            <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-caption">
              <span className="font-medium text-foreground">{person || t(($) => $.profile.incomplete)}</span>
              <Button variant="ghost" className="min-h-10 px-1 text-muted-foreground max-sm:min-h-11" onClick={() => update(key, { profileOpen: true, profileRevision: data?.revision || 0 })}><Pencil aria-hidden="true" />{t(($) => $.profile.edit)}</Button>
              <span className="text-muted-foreground">{recentImport ? `${t(($) => $.overview.imported)} ${formatDate(recentImport)}` : t(($) => $.overview.no_import)}</span>
              <AppLink href={reportsHref} className="text-muted-foreground underline-offset-4 hover:text-foreground hover:underline">{t(($) => $.overview.reports, { count: reports.length })}</AppLink>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button className="min-h-10 max-sm:min-h-11" onClick={() => openRecord()}><Plus aria-hidden="true" />{t(($) => $.record.add)}</Button>
            <Button variant="outline" className="min-h-10 max-sm:min-h-11" disabled={h.upload.isPending} onClick={() => fileInput.current?.click()}><Upload aria-hidden="true" />{h.upload.isPending ? t(($) => $.report.uploading) : t(($) => $.report.upload)}</Button>
            <input ref={fileInput} className="sr-only" aria-label={t(($) => $.report.upload)} type="file" accept="application/pdf,.pdf" disabled={h.upload.isPending} onChange={(event) => { const file = event.target.files?.[0]; event.target.value = ""; if (file) void upload(file).catch(() => {}); }} />
          </div>
        </header>

        <nav className="flex gap-6 overflow-x-auto border-b" aria-label={t(($) => $.page.title)}>
          {tabs.map((item) => {
            const targetHref = item === "overview" ? overviewHref : item === "reports" ? reportsHref : revisionsHref;
            return <AppLink key={item} href={targetHref} aria-current={tab === item ? "page" : undefined} className={`min-h-11 whitespace-nowrap border-b-2 py-3 text-body focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring ${tab === item ? "border-primary font-semibold" : "border-transparent text-muted-foreground hover:text-foreground"}`}>{t(($) => $.page[item])}</AppLink>;
          })}
        </nav>

        {queryError && <QueryError onRetry={() => void h.invalidate()} />}
        {mutationError && <MutationError error={mutationError} />}
        {h.overview.isPending && <HealthSkeleton />}
        {data?.available === false && <QueryError onRetry={() => void h.invalidate()} />}

        {data?.available === true && tab === "overview" && (all.length === 0
          ? <EmptyHealth onRecord={() => openRecord()} onUpload={() => fileInput.current?.click()} />
          : <>
            <section aria-label={t(($) => $.overview.daily)} className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-5 [&>div:last-child:nth-child(odd)]:max-md:col-span-2">
              {daily.map((spec) => {
                const observation = data.current.find((item) => (spec.codes as readonly string[]).includes(item.code));
                if (spec.label === "bmi") {
                  const calculated = all.find((item) => item.id === "computed-bmi");
                  if (calculated) return <DailyMetricCard key="bmi" label={t(($) => $.overview.bmi)} observation={calculated} recordable={false} onDetail={() => openMetric(calculated.id)} onRecord={() => {}} />;
                  if (data.current.some((item) => item.code === "height" || item.code === "weight")) return <button key="bmi" type="button" aria-label={`${t(($) => $.overview.bmi)} · ${t(($) => $.record.detail)}`} className="min-h-36 rounded-xl border bg-surface px-4 py-5 text-left hover:bg-surface-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring" onClick={() => openMetric("computed-bmi")}><span className="text-caption text-muted-foreground">{t(($) => $.overview.bmi)}</span><p className="mt-4 text-body text-muted-foreground">{t(($) => $.bmi.missing)}</p></button>;
                  return null;
                }
                return observation ? <DailyMetricCard key={spec.label} label={t(($) => $.overview[spec.label])} observation={observation} recordable={spec.recordable} onDetail={() => openMetric(observation.id)} onRecord={() => openRecord(spec.preset, observation)} /> : null;
              })}
              {(systolic || diastolic) && <DailyMetricCard label={t(($) => $.overview.pressure)} observation={systolic || diastolic!} value={`${systolic?.value || "—"} / ${diastolic?.value || "—"}`} unit={systolic?.unit || diastolic?.unit} abnormal={Boolean((systolic && isHealthAbnormal(systolic)) || (diastolic && isHealthAbnormal(diastolic)))} abnormalLabel={[systolic?.abnormal, diastolic?.abnormal].filter((value) => value && !isNormalMarker(value)).join(" / ")} recordable onDetail={() => openMetric("blood_pressure")} onRecord={() => openRecord("blood_pressure", systolic)} />}
            </section>

            <section className="space-y-4" aria-labelledby="health-metrics-heading">
              <div className="flex flex-wrap items-end justify-between gap-3">
                <div><h2 id="health-metrics-heading" className="text-title font-semibold">{category || t(($) => $.overview.all)}</h2><p className="mt-1 text-caption text-muted-foreground">{t(($) => $.record.items, { count: filtered.length })}</p></div>
                <div className="flex w-full flex-wrap gap-2 sm:w-auto sm:flex-nowrap">
                  <Input aria-label={t(($) => $.overview.search)} placeholder={t(($) => $.overview.search_placeholder)} value={search} onChange={(event) => navigation.replace(href({ q: event.target.value || null }))} className="min-h-11 min-w-0 w-full flex-none text-title-sm sm:min-h-10 sm:w-56 sm:flex-1 sm:text-body" />
                  <Button className="min-h-11 sm:min-h-10" variant={onlyAbnormal ? "default" : "outline"} aria-pressed={onlyAbnormal} onClick={() => navigation.replace(href({ abnormal: onlyAbnormal ? null : "1" }))}>{t(($) => $.overview.abnormal)}</Button>
                </div>
              </div>

              <label className="block text-body md:hidden"><span className="mb-1.5 block text-caption text-muted-foreground">{t(($) => $.record.category)}</span><select className="min-h-11 w-full rounded-lg border bg-background px-3 text-foreground" value={category || "all"} onChange={(event) => navigation.replace(href({ category: event.target.value }))}><option value="all">{t(($) => $.overview.all)} · {all.length}</option>{categories.map((item) => <option key={item} value={item}>{item} · {all.filter((observation) => observation.category === item).length}</option>)}</select></label>

              <div className="grid items-start gap-5 md:grid-cols-[164px_minmax(0,1fr)]">
                <nav className="hidden gap-1 md:sticky md:top-4 md:flex md:flex-col" aria-label={t(($) => $.record.category)}>{["", ...categories].map((item) => <AppLink key={item} href={href({ category: item || "all" })} aria-current={category === item ? "true" : undefined} className={`flex min-h-10 items-center justify-between gap-4 rounded-md px-3 py-2 text-left text-body focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring ${category === item ? "bg-surface-selected font-semibold" : "hover:bg-surface-hover"}`}><span>{item || t(($) => $.overview.all)}</span><span className="text-caption font-normal text-muted-foreground">{all.filter((observation) => !item || observation.category === item).length}</span></AppLink>)}</nav>
                <MetricCollection items={filtered} onDetail={openMetric} />
              </div>
            </section>

            {reports[0] && <section className="flex flex-wrap items-center justify-between gap-4 rounded-xl border bg-surface px-5 py-5"><div className="min-w-0"><p className="text-caption text-muted-foreground">{t(($) => $.overview.recent_report)}</p><h3 className="mt-1 break-words text-body font-medium">{reports[0].filename}</h3></div><AppLink href={paths.healthReport(reports[0].id)} className="shrink-0 text-body font-medium underline underline-offset-4">{t(($) => $.overview.read_analysis)}</AppLink></section>}
          </>
        )}

        {data?.available === true && tab === "reports" && <ReportsView reports={reports} selectedReport={selectedReport} result={result} reportTab={reportTab} extractionAttempt={view.extractionAttempt} extractionHistory={history.data?.items || []} availableAgentCount={availableAgents.length} agentId={agentId} wsId={wsId} revision={data.revision} paths={paths} processPending={h.process.isPending} reportsPending={h.reports.isPending} onExtractionChange={(attempt) => update(key, { extractionAttempt: attempt })} onProcess={(report, reprocess) => h.process.mutate({ id: report.id, agentId, reprocess })} onMetric={openMetric} href={href} />}

        {data?.available === true && tab === "revisions" && <section aria-labelledby="health-revisions-heading"><div className="flex flex-wrap items-start justify-between gap-4"><div><h2 id="health-revisions-heading" className="text-title font-semibold">{t(($) => $.page.revisions)}</h2><p className="mt-2 max-w-2xl text-body leading-relaxed text-muted-foreground">{t(($) => $.revision.description)}</p></div><span className="rounded-lg border bg-surface px-3 py-2 text-caption font-medium tabular-nums">{t(($) => $.revision.current, { revision: data.revision })}</span></div>{h.revisions.isPending ? <HealthSkeleton compact /> : (h.revisions.data?.items || []).length === 0 ? <p className="mt-6 rounded-xl border bg-surface p-6 text-body text-muted-foreground">{t(($) => $.revision.empty)}</p> : <RevisionCards items={h.revisions.data?.items || []} current={data.current} profile={data.profile} onSelect={(revision) => update(key, { targetRevision: revision })} />}</section>}
      </div>
    </main>

    {view.profileOpen && data && <ProfileEditor open value={data.profile} revision={view.profileRevision} h={h} onClose={() => update(key, { profileOpen: false })} />}
    {view.editorId && <ObservationEditor value={editing || {}} pairedValue={editingPair} recent={data?.current} preset={view.editorPreset} mode={view.editorMode} revision={view.editorRevision} h={h} onClose={() => update(key, { editorId: null, editorPreset: null })} />}
    <MetricDetailSheet open={Boolean(detail || pressureDetail)} detail={detail} pressureDetail={pressureDetail} systolic={systolic} diastolic={diastolic} pressureRows={pressureRows} observations={data?.observations || []} current={data?.current || []} reports={reports} onClose={closeMetric} onRecord={openRecord} onCorrection={openCorrection} />

    <BMIDetailSheet open={metricId === "computed-bmi"} bmi={data?.computedBMI || null} onClose={closeMetric} onRecord={() => openRecord("weight", data?.current.find((item) => item.code === "weight"))} />

    <Dialog open={Boolean(target)} onOpenChange={(open) => { if (!open) update(key, { targetRevision: null }); }}><DialogContent className="max-h-[85dvh] overflow-y-auto overscroll-contain sm:max-w-xl [&_[data-slot=dialog-close]]:size-11"><DialogHeader><DialogTitle>{t(($) => $.revision.confirm, { target: target?.revision || 0 })}</DialogTitle><DialogDescription>{t(($) => $.revision.confirm_description)}</DialogDescription></DialogHeader>{target && data && <><RevisionDiff current={data.current} profile={data.profile} target={target} /><MutationError error={h.restore.error} /><Button className="min-h-11" disabled={h.restore.isPending} onClick={() => void h.restore.mutateAsync({ target: target.revision, revision: data.revision }).then(() => update(key, { targetRevision: null })).catch(() => {})}>{t(($) => $.revision.restore_now)}</Button></>}</DialogContent></Dialog>
  </div>;
}

function DailyMetricCard({ label, observation, value, unit, abnormal = false, abnormalLabel, recordable, onDetail, onRecord }: { label: string; observation: HealthObservation; value?: string; unit?: string; abnormal?: boolean; abnormalLabel?: string; recordable: boolean; onDetail: () => void; onRecord: () => void }) {
  const { t } = useT("health");
  const flagged = abnormal || isHealthAbnormal(observation);
  const marker = abnormalLabel || (flagged ? observation.abnormal : "");
  return <div className="relative min-w-0 overflow-hidden rounded-xl border bg-surface"><button type="button" onClick={onDetail} aria-label={`${label} · ${t(($) => $.record.detail)}`} className="min-h-36 w-full min-w-0 px-4 py-5 text-left transition-colors hover:bg-surface-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"><span className="block pr-14 text-caption text-muted-foreground">{label}</span><p className={`mt-2 flex min-w-0 items-baseline gap-1 whitespace-nowrap font-semibold tracking-tight tabular-nums ${flagged ? "text-destructive" : ""}`}><span className="text-[clamp(1.75rem,2.2vw,2.25rem)] leading-none">{value || observation.value}</span><span className="text-caption font-normal text-muted-foreground">{unit || observation.unit}</span></p><div className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-caption text-muted-foreground"><span>{observation.source === "calculated" ? t(($) => $.bmi.automatic) : formatDate(observation.observedAt)}</span>{flagged && <span className="font-medium text-destructive">{marker || t(($) => $.overview.abnormal_value)}</span>}</div></button>{recordable && <button type="button" onClick={onRecord} aria-label={t(($) => $.record.record_metric, { name: label })} className="absolute right-1 top-1 flex size-11 items-center justify-center gap-1 rounded-md text-caption font-medium text-muted-foreground transition-colors hover:bg-surface-hover hover:text-foreground focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"><Plus className="size-3.5" aria-hidden="true" /><span className="sr-only">{t(($) => $.record.record)}</span></button>}</div>;
}

function MetricCollection({ items, onDetail }: { items: HealthObservation[]; onDetail: (id: string) => void }) {
  const { t } = useT("health");
  if (!items.length) return <p className="rounded-lg border bg-surface px-4 py-8 text-body text-muted-foreground">{t(($) => $.overview.no_match)}</p>;
  return <><div className="divide-y overflow-hidden rounded-lg border bg-surface md:hidden">{items.map((observation) => { const abnormal = isHealthAbnormal(observation); return <button key={observation.id} type="button" onClick={() => onDetail(observation.id)} className="flex min-h-16 w-full items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-surface-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"><div className="min-w-0 flex-1"><div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1"><span className="break-words font-medium">{observation.name}</span><span className={`shrink-0 font-medium tabular-nums ${abnormal ? "text-destructive" : ""}`}>{observation.value} <span className="text-caption font-normal text-muted-foreground">{observation.unit}</span>{abnormal && <span className="ml-1 text-caption">{observation.abnormal}</span>}</span></div><div className="mt-1 flex flex-wrap justify-between gap-x-3 text-caption text-muted-foreground"><span>{formatDate(observation.observedAt)}</span><span className="break-words text-right">{observation.referenceRange ? `${t(($) => $.record.range)} ${observation.referenceRange}` : t(($) => $.record.no_range)}</span></div></div><ChevronRight className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" /></button>; })}</div><MetricTable items={items} onDetail={onDetail} /></>;
}

function MetricTable({ items, onDetail }: { items: HealthObservation[]; onDetail: (id: string) => void }) {
  const { t } = useT("health");
  return <div className="hidden min-w-0 overflow-x-auto rounded-xl border bg-surface md:block"><table className="w-full table-fixed text-left text-body"><thead className="bg-muted text-caption font-normal text-muted-foreground"><tr><th className="w-[24%] px-4 py-3 font-normal">{t(($) => $.record.name)}</th><th className="w-[22%] px-4 py-3 font-normal">{t(($) => $.record.value)}</th><th className="w-[12%] px-3 py-3 font-normal">{t(($) => $.record.unit)}</th><th className="w-[22%] px-4 py-3 font-normal">{t(($) => $.record.range)}</th><th className="w-1/5 px-4 py-3 font-normal">{t(($) => $.record.date)}</th></tr></thead><tbody className="divide-y">{items.map((observation) => { const abnormal = isHealthAbnormal(observation); return <tr key={observation.id} className="transition-colors hover:bg-surface-hover"><td className="break-words px-4 py-2"><button type="button" onClick={() => onDetail(observation.id)} className="min-h-10 text-left font-medium underline-offset-4 hover:underline focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring">{observation.name}</button></td><td className={`break-words px-4 py-3 font-medium tabular-nums ${abnormal ? "text-destructive" : ""}`}>{observation.value}{abnormal && <span className="ml-1 text-caption">{observation.abnormal}</span>}</td><td className="break-words px-4 py-3 text-caption text-muted-foreground">{observation.unit || "—"}</td><td className="break-words px-4 py-3 text-caption text-muted-foreground">{observation.referenceRange || "—"}</td><td className="break-words px-4 py-3 text-caption text-muted-foreground">{observation.source === "calculated" ? t(($) => $.bmi.automatic) : formatDate(observation.observedAt)}</td></tr>; })}</tbody></table></div>;
}

function ReportsView({ reports, selectedReport, result, reportTab, extractionAttempt, extractionHistory, availableAgentCount, agentId, wsId, revision, paths, processPending, reportsPending, onExtractionChange, onProcess, onMetric, href }: { reports: HealthReport[]; selectedReport?: HealthReport; result?: HealthReport["result"]; reportTab: ReportTab; extractionAttempt: number | null; extractionHistory: Array<{ attempt: number; revision: number; result: NonNullable<HealthReport["result"]>; createdAt: string }>; availableAgentCount: number; agentId: string; wsId: string; revision: number; paths: ReturnType<typeof useWorkspacePaths>; processPending: boolean; reportsPending: boolean; onExtractionChange: (attempt: number) => void; onProcess: (report: HealthReport, reprocess: boolean) => void; onMetric: (id: string) => void; href: (patch: Record<string, string | null>, clear?: string[]) => string }) {
  const { t } = useT("health");
  if (reportsPending) return <HealthSkeleton compact />;
  if (reports.length === 0) return <p className="rounded-lg border bg-surface p-6 text-body text-muted-foreground">{t(($) => $.report.empty)}</p>;
  if (!selectedReport) return <>{availableAgentCount === 0 && <p className="mb-4 text-body text-muted-foreground">{t(($) => $.report.no_agent)} <AppLink href={paths.newAgent()} className="underline">{t(($) => $.report.create_assistant)}</AppLink></p>}<nav className="grid gap-4 md:grid-cols-2" aria-label={t(($) => $.page.reports)}>{reports.map((report) => { const abnormalCount = report.result?.observations.filter(isHealthAbnormal).length || 0; return <AppLink key={report.id} href={paths.healthReport(report.id)} className="flex min-h-36 flex-wrap items-start justify-between gap-4 rounded-xl border bg-surface p-5 transition-colors hover:bg-surface-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring md:p-5"><div className="min-w-0"><h3 className="break-words text-body font-medium">{report.filename}</h3><p className="mt-1 text-caption text-muted-foreground">{[report.result?.institution, formatDate(report.result?.examDate || report.createdAt), t(($) => $.report.pages, { count: report.pageCount })].filter(Boolean).join(" · ")}</p></div><div className="text-right"><p className="text-caption text-muted-foreground">{reportStatus(report, t)}</p>{abnormalCount > 0 && <p className="mt-1 text-caption font-medium text-destructive">{t(($) => $.report.abnormal_count, { count: abnormalCount })}</p>}</div></AppLink>; })}</nav></>;
  return <section className="min-w-0 space-y-6 rounded-xl border bg-surface p-5 md:p-8"><AppLink href={href({ tab: "reports", report: null, reportTab: null })} className="inline-flex min-h-10 items-center text-caption text-muted-foreground underline underline-offset-4">{t(($) => $.report.back)}</AppLink><div className="flex flex-wrap items-start justify-between gap-3"><div className="min-w-0"><p className="text-caption text-muted-foreground">{result?.institution}</p><h2 className="mt-1 break-words text-title font-semibold">{selectedReport.filename}</h2><p className="mt-1 text-caption text-muted-foreground">{formatDate(result?.examDate || selectedReport.createdAt)} · {t(($) => $.report.pages, { count: selectedReport.pageCount })}</p></div><Button className="min-h-11 sm:min-h-10" variant="outline" disabled={!agentId || processPending || ["running", "queued", "claimed", "deferred"].includes(selectedReport.taskStatus)} onClick={() => onProcess(selectedReport, Boolean(selectedReport.result))}>{selectedReport.result ? t(($) => $.report.reprocess) : t(($) => $.report.process)}</Button></div>{extractionHistory.length > 0 && <label className="flex flex-wrap items-center gap-3 text-caption text-muted-foreground">{t(($) => $.report.extraction_history)}<select className="min-h-11 max-w-full rounded-md border bg-background px-3 text-body text-foreground" value={extractionAttempt || selectedReport.attempt} onChange={(event) => onExtractionChange(Number(event.target.value))}>{extractionHistory.map((item) => <option key={item.attempt} value={item.attempt}>{t(($) => $.report.extraction_version, { attempt: item.attempt, revision: item.revision })}</option>)}</select></label>}{result && result.contextRevision !== revision && <div role="status" className="rounded-lg border bg-muted px-4 py-3 text-body"><p className="font-medium">{t(($) => $.report.stale)}</p><p className="mt-1 text-caption text-muted-foreground">{t(($) => $.report.analysis_version, { revision: result.contextRevision })}</p></div>}<nav className="flex gap-5 overflow-x-auto border-b" aria-label={t(($) => $.report.detail)}>{reportTabs.map((item) => <AppLink key={item} href={href({ report: selectedReport.id, reportTab: item })} aria-current={reportTab === item ? "page" : undefined} className={`min-h-11 whitespace-nowrap border-b-2 py-3 text-body focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring ${reportTab === item ? "border-primary font-medium" : "border-transparent text-muted-foreground hover:text-foreground"}`}>{item === "original" ? t(($) => $.report.original_tab) : t(($) => $.report[item])}</AppLink>)}</nav>{reportTab === "analysis" && <><h3 className="text-title-sm font-medium">{t(($) => $.report.analysis)}</h3>{result?.analysis ? <div className="max-w-3xl text-body leading-relaxed [&_h2]:mt-8 [&_h3]:mt-6 [&_p]:leading-relaxed"><RichContent content={result.analysis} /></div> : <p className="text-body text-muted-foreground">{t(($) => $.report.analysis_pending)}</p>}{result?.conclusions && <details className="border-t pt-4"><summary className="min-h-10 cursor-pointer text-body">{t(($) => $.report.conclusions)}</summary><p className="mt-3 whitespace-pre-wrap text-pretty text-body">{result.conclusions}</p></details>}</>}{reportTab === "metrics" && <MetricCollection items={result?.observations || []} onDetail={onMetric} />}{reportTab === "original" && <div className="flex flex-wrap gap-3 rounded-xl border bg-muted/30 p-5"><a className="inline-flex min-h-11 items-center rounded-lg border px-3 text-body hover:bg-muted" target="_blank" rel="noopener noreferrer" href={reportFile(selectedReport.id, wsId)}>{t(($) => $.report.original)}</a><a className="inline-flex min-h-11 items-center rounded-lg border px-3 text-body hover:bg-muted" href={`${reportFile(selectedReport.id, wsId)}&download=1`}>{t(($) => $.report.download)}</a></div>}{result?.pages.length ? <details className="rounded-lg border bg-muted/30 px-4 py-3"><summary className="min-h-10 cursor-pointer text-body text-muted-foreground">{t(($) => $.report.coverage)} · {t(($) => $.report.pages, { count: result.pages.length })}</summary><div className="mt-3 space-y-3">{result.pages.map((page) => <p key={page.page} className="text-caption text-muted-foreground"><a className="underline" target="_blank" rel="noopener noreferrer" href={`${reportFile(selectedReport.id, wsId)}#page=${page.page}`}>{t(($) => $.report.page, { page: page.page })}</a> · {page.note}</p>)}</div></details> : null}{selectedReport.issueId && <AppLink href={paths.issueDetail(selectedReport.issueId)} className="inline-flex min-h-10 items-center text-caption underline">{t(($) => $.report.task)}</AppLink>}</section>;
}

function EmptyHealth({ onRecord, onUpload }: { onRecord: () => void; onUpload: () => void }) { const { t } = useT("health"); return <section className="rounded-lg border bg-surface px-5 py-10 text-center"><h2 className="text-title font-semibold">{t(($) => $.page.empty_title)}</h2><p className="mx-auto mt-2 max-w-md text-pretty text-body text-muted-foreground">{t(($) => $.page.empty)}</p><div className="mt-5 flex flex-wrap justify-center gap-2"><Button className="min-h-11" onClick={onRecord}><Plus aria-hidden="true" />{t(($) => $.record.add)}</Button><Button className="min-h-11" variant="outline" onClick={onUpload}><Upload aria-hidden="true" />{t(($) => $.report.upload)}</Button></div></section>; }
function QueryError({ onRetry }: { onRetry: () => void }) { const { t } = useT("health"); return <div role="alert" className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-destructive/30 bg-destructive/5 px-4 py-3 text-body text-destructive"><span>{t(($) => $.page.failed)}</span><Button className="min-h-10" variant="outline" onClick={onRetry}>{t(($) => $.page.retry)}</Button></div>; }
function HealthSkeleton({ compact = false }: { compact?: boolean }) { const { t } = useT("health"); return <div aria-label={t(($) => $.page.loading)} className={`grid gap-3 ${compact ? "mt-5" : "grid-cols-2 md:grid-cols-5"}`}>{Array.from({ length: compact ? 3 : 5 }, (_, index) => <div key={index} className="h-24 animate-pulse rounded-lg bg-muted motion-reduce:animate-none" />)}</div>; }

interface PressureRow { key: string; observedAt: string; systolic?: HealthObservation; diastolic?: HealthObservation; }
function pairBloodPressure(observations: HealthObservation[]): PressureRow[] { const groups = new Map<string, PressureRow>(); for (const observation of observations) { if (observation.code !== "systolic_pressure" && observation.code !== "diastolic_pressure") continue; const key = observation.measurementGroupId || (observation.reportId ? `${observation.reportId}:${observation.page}:${observation.observedAt}` : observation.id); const row = groups.get(key) || { key, observedAt: observation.observedAt }; if (observation.code === "systolic_pressure") row.systolic = observation; else row.diastolic = observation; groups.set(key, row); } return [...groups.values()].sort((a, b) => b.observedAt.localeCompare(a.observedAt)); }
function findPressurePair(observation: HealthObservation, observations: HealthObservation[]) { const expectedCode = observation.code === "systolic_pressure" ? "diastolic_pressure" : "systolic_pressure"; return observations.find((candidate) => candidate.code === expectedCode && (observation.measurementGroupId ? candidate.measurementGroupId === observation.measurementGroupId : candidate.reportId === observation.reportId && candidate.page === observation.page && candidate.observedAt === observation.observedAt)); }
function isNormalMarker(value: string) { return ["", "-", "—", "--", "−", "normal", "正常", "未见异常", "无异常", "阴性"].includes(value.trim().toLowerCase()); }
function formatDate(value?: string) { if (!value) return "—"; const parsed = new Date(value.length === 10 ? `${value}T12:00:00` : value); return Number.isNaN(parsed.getTime()) ? value : new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit" }).format(parsed); }
function reportFile(id: string, wsId: string) { return `/api/health/reports/${encodeURIComponent(id)}/file?workspace_id=${encodeURIComponent(wsId)}`; }
function reportStatus(report: HealthReport, t: ReturnType<typeof useT<"health">>["t"]) { if (["running", "queued", "claimed", "deferred"].includes(report.taskStatus)) return t(($) => $.report.processing); if (report.taskStatus === "failed") return t(($) => $.report.failed); if (report.result && !report.result.analysis) return t(($) => $.report.analysis_pending); return report.result ? t(($) => $.report.completed) : t(($) => $.report.uploaded); }
