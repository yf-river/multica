"use client";

import { healthMetricKey, isHealthAbnormal, type ComputedBMI, type HealthObservation, type HealthReport } from "@multica/core/health";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@multica/ui/components/ui/sheet";
import { AppLink } from "../navigation";
import { useT } from "../i18n";
import { Trend } from "./health-editors";

interface PressureRow { key: string; observedAt: string; systolic?: HealthObservation; diastolic?: HealthObservation }
interface Props {
  open: boolean;
  detail?: HealthObservation;
  pressureDetail: boolean;
  systolic?: HealthObservation;
  diastolic?: HealthObservation;
  pressureRows: PressureRow[];
  observations: HealthObservation[];
  current: HealthObservation[];
  reports: HealthReport[];
  onClose: () => void;
  onRecord: (preset?: string, observation?: HealthObservation) => void;
  onCorrection: (observation: HealthObservation, preset?: string) => void;
}

export function MetricDetailSheet({ open, detail, pressureDetail, systolic, diastolic, pressureRows, observations, current, reports, onClose, onRecord, onCorrection }: Props) {
  const { t } = useT("health");
  const history = detail ? observations.filter((item) => healthMetricKey(item) === healthMetricKey(detail)).sort((a, b) => b.observedAt.localeCompare(a.observedAt)) : [];
  const selected = pressureDetail ? systolic || diastolic : detail;
  const title = pressureDetail ? t(($) => $.overview.pressure) : detail?.name;
  const flagged = pressureDetail ? [systolic, diastolic].some((item) => item && isHealthAbnormal(item)) : Boolean(detail && isHealthAbnormal(detail));
  const method = detail && [detail.method, detail.specimen, detail.site].filter(Boolean).join(" · ");
  const validPoints = history.filter((item) => /^[-+]?\d+(\.\d+)?$/.test(item.value) && Number.isFinite(Date.parse(item.observedAt)));
  const previous = detail && validPoints.find((item) => item.id !== detail.id && item.observedAt < detail.observedAt && item.unit === detail.unit);
  const delta = previous && detail && /^[-+]?\d+(\.\d+)?$/.test(detail.value) ? Number(detail.value) - Number(previous.value) : null;
  const preset = detail?.code === "weight" ? "weight" : detail?.code === "height" ? "height" : detail && ["脉搏", "resting_heart_rate"].includes(detail.code) ? "heart_rate" : undefined;

  return <Sheet open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
    <SheetContent side="right" className="gap-0 overflow-hidden data-[side=right]:w-full data-[side=right]:sm:max-w-xl [&_[data-slot=sheet-close]]:size-11">
      <SheetHeader className="shrink-0 border-b px-6 py-5 pr-16">
        <SheetTitle className="text-title font-semibold">{title}</SheetTitle>
        <SheetDescription className="mt-1 text-caption">{selected?.category} · {selected?.observedAt ? formatDate(selected.observedAt) : t(($) => $.page.unrecorded)}</SheetDescription>
      </SheetHeader>
      <div className="min-h-0 flex-1 space-y-7 overflow-y-auto overscroll-contain px-6 py-6">
        <section className="rounded-xl border bg-surface px-5 py-5" aria-label={t(($) => $.detail.result)}>
          <div className="flex items-center justify-between gap-3 text-caption"><span className="text-muted-foreground">{t(($) => $.detail.result)}</span>{flagged && <span className="rounded-md bg-destructive/10 px-2 py-1 font-medium text-destructive">{t(($) => $.overview.abnormal_value)}</span>}</div>
          <p className={`mt-3 flex flex-wrap items-baseline gap-2 font-semibold tabular-nums ${flagged ? "text-destructive" : ""}`}><span className="break-words text-display leading-tight">{pressureDetail ? `${systolic?.value || "—"} / ${diastolic?.value || "—"}` : detail?.value}</span><span className="text-body font-normal text-muted-foreground">{pressureDetail ? t(($) => $.overview.unit_mmhg) : detail?.unit}</span></p>
          <p className="mt-2 text-caption text-muted-foreground">{formatDate(selected?.observedAt)} · {selected?.source === "manual" ? t(($) => $.record.manual) : t(($) => $.record.report_source)}</p>
          {(detail?.referenceRange || method || delta !== null) && <dl className="mt-4 space-y-2 border-t pt-4 text-body">
            {detail?.referenceRange && <div className="flex gap-4"><dt className="shrink-0 text-muted-foreground">{t(($) => $.record.range)}</dt><dd className="min-w-0 break-words tabular-nums">{detail.referenceRange}</dd></div>}
            {method && <div className="flex gap-4"><dt className="shrink-0 text-muted-foreground">{t(($) => $.record.method)}</dt><dd className="min-w-0 break-words">{method}</dd></div>}
            {delta !== null && previous && <p className="text-caption text-muted-foreground">{t(($) => $.record.compared_previous, { date: formatDate(previous.observedAt), delta: `${delta > 0 ? "+" : ""}${Number(delta.toFixed(2))} ${detail?.unit}` })}</p>}
          </dl>}
        </section>

        {!pressureDetail && validPoints.length >= 2 && <section><SectionHeading title={t(($) => $.record.trend)} /><div className="mt-3 rounded-xl border bg-surface p-3"><Trend observations={history} /></div></section>}
        {pressureDetail && ["systolic_pressure", "diastolic_pressure"].map((code) => {
          const points = observations.filter((item) => item.code === code && /^[-+]?\d+(\.\d+)?$/.test(item.value) && Number.isFinite(Date.parse(item.observedAt)));
          return points.length >= 2 ? <section key={code}><SectionHeading title={code === "systolic_pressure" ? t(($) => $.record.systolic) : t(($) => $.record.diastolic)} /><Trend observations={points} /></section> : null;
        })}

        <section><SectionHeading title={t(($) => $.record.history_list)} count={pressureDetail ? pressureRows.length : history.length} />
          <div className="mt-3 overflow-hidden rounded-xl border bg-surface">
            {pressureDetail ? pressureRows.map((row) => <div key={row.key} className="flex items-center gap-3 border-b px-4 py-3 last:border-0"><div className="min-w-0 flex-1"><p className="font-medium tabular-nums">{row.systolic?.value || "—"} / {row.diastolic?.value || "—"} <span className="text-caption font-normal text-muted-foreground">{t(($) => $.overview.unit_mmhg)}</span></p><p className="mt-1 text-caption text-muted-foreground">{formatDate(row.observedAt)}</p></div>{row.systolic && row.diastolic && <Button variant="ghost" className="min-h-11" onClick={() => onCorrection(row.systolic!, "blood_pressure")}>{t(($) => $.record.correct)}</Button>}</div>) : history.map((item) => <div key={item.id} className="flex items-center gap-3 border-b px-4 py-3 last:border-0"><div className="min-w-0 flex-1"><div className="flex flex-wrap items-center gap-2"><p className="break-words font-medium tabular-nums">{item.value} <span className="text-caption font-normal text-muted-foreground">{item.unit}</span></p>{current.some((active) => active.id === item.id) && <span className="rounded-md bg-muted px-2 py-0.5 text-caption text-muted-foreground">{t(($) => $.detail.current)}</span>}</div><p className="mt-1 text-caption text-muted-foreground">{formatDate(item.observedAt)} · {item.source === "manual" ? t(($) => $.record.manual) : t(($) => $.record.report_source)}</p></div><Button variant="ghost" className="min-h-11" onClick={() => onCorrection(item, preset)}>{t(($) => $.record.correct)}</Button></div>)}
          </div>
        </section>
        {(pressureDetail ? [systolic, diastolic].filter((item): item is HealthObservation => Boolean(item)) : detail ? [detail] : []).map((item) => <SourceCard key={item.id} observation={item} report={reports.find((report) => report.id === item.reportId)} />)}
      </div>
      {(pressureDetail || preset) && <footer className="shrink-0 border-t bg-surface px-6 py-4 pb-[max(1rem,env(safe-area-inset-bottom))]"><Button className="h-auto min-h-11 w-full whitespace-normal break-words py-3" onClick={() => onRecord(pressureDetail ? "blood_pressure" : preset, selected)}>{t(($) => $.record.record_metric, { name: title || "" })}</Button></footer>}
    </SheetContent>
  </Sheet>;
}

export function BMIDetailSheet({ open, bmi, onClose, onRecord }: { open: boolean; bmi: ComputedBMI | null; onClose: () => void; onRecord: () => void }) {
  const { t } = useT("health");
  const paths = useWorkspacePaths();
  return <Sheet open={open} onOpenChange={(next) => { if (!next) onClose(); }}><SheetContent side="right" className="gap-0 overflow-hidden data-[side=right]:w-full data-[side=right]:sm:max-w-xl [&_[data-slot=sheet-close]]:size-11"><SheetHeader className="shrink-0 border-b px-6 py-5 pr-16"><SheetTitle>{t(($) => $.overview.bmi)}</SheetTitle><SheetDescription>{t(($) => $.bmi.automatic)}</SheetDescription></SheetHeader><div className="min-h-0 flex-1 space-y-6 overflow-y-auto overscroll-contain p-6"><section className="rounded-xl border bg-surface p-5"><p className="text-display font-semibold tabular-nums">{bmi?.value.toFixed(1) || "—"} <span className="text-body font-normal text-muted-foreground">{bmi?.unit}</span></p><p className="mt-3 text-body leading-relaxed text-muted-foreground">{bmi ? t(($) => $.bmi.description) : t(($) => $.bmi.missing)}</p></section>{bmi && <section><h3 className="text-title-sm font-semibold">{t(($) => $.bmi.sources)}</h3><div className="mt-3 divide-y overflow-hidden rounded-xl border">{[bmi.height, bmi.weight].map((item) => <AppLink key={item.id} href={`${paths.health()}?metric=${encodeURIComponent(item.id)}`} className="flex min-h-20 items-center justify-between gap-3 px-4 py-3 hover:bg-surface-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"><div><p className="text-body font-medium">{item.name}</p><p className="mt-1 text-caption text-muted-foreground">{formatDate(item.observedAt)}</p></div><span className="text-body font-medium tabular-nums">{item.value} {item.unit}</span></AppLink>)}</div></section>}<p className="text-caption leading-relaxed text-muted-foreground">{t(($) => $.bmi.history)}</p></div><footer className="shrink-0 border-t p-6"><Button className="min-h-11 w-full" onClick={onRecord}>{t(($) => $.record.record_metric, { name: t(($) => $.overview.weight) })}</Button></footer></SheetContent></Sheet>;
}

function SectionHeading({ title, count }: { title: string; count?: number }) {
  return <div className="flex items-baseline justify-between gap-3"><h3 className="text-title-sm font-semibold">{title}</h3>{count !== undefined && <span className="text-caption tabular-nums text-muted-foreground">{count}</span>}</div>;
}

function SourceCard({ observation, report }: { observation: HealthObservation; report?: HealthReport }) {
  const { t } = useT("health");
  const paths = useWorkspacePaths();
  if (!observation.reportId && !observation.evidence && !observation.uncertainty) return null;
  const evidence = observation.evidence;
  return <section aria-label={t(($) => $.detail.source)}><SectionHeading title={t(($) => $.detail.source)} />
    <div className="mt-3 overflow-hidden rounded-xl border bg-surface">
      <div className="border-b bg-muted/40 px-4 py-4"><div className="flex items-start justify-between gap-3"><h4 className="min-w-0 break-words text-body font-medium">{report?.filename || t(($) => $.record.report_source)}</h4>{observation.page > 0 && <span className="shrink-0 rounded-md border bg-surface px-2 py-1 text-caption tabular-nums">{t(($) => $.report.page, { page: observation.page })}</span>}</div><p className="mt-2 text-caption text-muted-foreground">{[report?.result?.institution, observation.observedAt ? formatDate(observation.observedAt) : ""].filter(Boolean).join(" · ")}</p></div>
      {evidence && <div className="px-4 py-4"><p className="mb-2 text-caption text-muted-foreground">{t(($) => $.record.evidence)}</p>{evidence.length <= 300 ? <blockquote className="whitespace-pre-wrap break-words text-body leading-relaxed">{evidence}</blockquote> : <details className="group"><summary className="cursor-pointer list-none"><blockquote className="line-clamp-4 whitespace-pre-wrap break-words text-body leading-relaxed group-open:hidden">{evidence}</blockquote><span className="mt-3 inline-flex min-h-10 items-center text-caption font-medium underline underline-offset-4 group-open:hidden">{t(($) => $.detail.expand)}</span><span className="hidden min-h-10 items-center text-caption font-medium underline underline-offset-4 group-open:inline-flex">{t(($) => $.detail.collapse)}</span></summary><blockquote className="mt-2 whitespace-pre-wrap break-words text-body leading-relaxed">{evidence}</blockquote></details>}</div>}
      {observation.uncertainty && <div className="mx-4 mb-4 rounded-lg bg-muted px-3 py-3"><p className="text-caption font-medium">{t(($) => $.record.source_note)}</p><p className="mt-1 break-words text-caption leading-relaxed text-muted-foreground">{observation.uncertainty}</p></div>}
      {observation.reportId && <AppLink href={paths.healthReport(observation.reportId)} className="flex min-h-11 items-center justify-between gap-3 border-t px-4 py-3 text-caption font-medium hover:bg-surface-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring">{t(($) => $.detail.view_report)}<span aria-hidden="true">→</span></AppLink>}
    </div>
  </section>;
}

function formatDate(value?: string) { if (!value) return "—"; const date = new Date(value.length === 10 ? `${value}T12:00:00` : value); return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit" }).format(date); }
