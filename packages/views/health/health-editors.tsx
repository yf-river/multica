"use client";
import { useState, type FormEvent } from "react";
import { ArrowLeft, LoaderCircle } from "lucide-react";
import { toast } from "sonner";
import { calculateBMI, useHealth, type HealthObservation, type HealthProfile } from "@multica/core/health";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
import { useT } from "../i18n";
type HealthQueries = ReturnType<typeof useHealth>;
export function ProfileEditor({
  open,
  onClose,
  value,
  revision,
  h,
}: {
  open: boolean;
  onClose: () => void;
  value?: HealthProfile;
  revision: number;
  h: HealthQueries;
}) {
  const { t } = useT("health");
  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    await h.profile.mutateAsync({
      value: {
        name: String(form.get("name") ?? ""),
        sex: String(form.get("sex") ?? ""),
        birthDate: String(form.get("birthDate") ?? ""),
      },
      revision,
    });
    toast.success(t(($) => $.profile.saved));
    onClose();
  };
  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) onClose();
      }}
    >
      <DialogContent className="[&_[data-slot=dialog-close]]:size-11">
        <DialogHeader>
          <DialogTitle>{t(($) => $.profile.edit)}</DialogTitle>
        </DialogHeader>
        <form
          className="space-y-4"
          onSubmit={(e) => void save(e).catch(() => {})}
        >
          <label className="block text-body">
            {t(($) => $.profile.name)}
            <Input name="name" autoComplete="off" defaultValue={value?.name} maxLength={100} />
          </label>
          <label className="block text-body">
            {t(($) => $.profile.sex)}
            <select
              name="sex"
              autoComplete="off"
              className="mt-1 w-full rounded-md border bg-background p-2"
              defaultValue={value?.sex}
            >
              <option value="">{t(($) => $.page.unrecorded)}</option>
              {[
                t(($) => $.profile.male),
                t(($) => $.profile.female),
                t(($) => $.profile.other),
              ].map((v) => (
                <option key={v}>{v}</option>
              ))}
            </select>
          </label>
          <label className="block text-body">
            {t(($) => $.profile.birth_date)}
            <Input
              name="birthDate"
              type="date"
              autoComplete="off"
              defaultValue={value?.birthDate}
            />
          </label>
          <p className="text-caption text-muted-foreground">
            {t(($) => $.report.original_age)}
          </p>
          <Button className="min-h-11 sm:min-h-10" disabled={h.profile.isPending} type="submit">
            {h.profile.isPending && <LoaderCircle className="animate-spin" aria-hidden="true" />}
            {h.profile.isPending ? t(($) => $.profile.saving) : t(($) => $.profile.save)}
          </Button>
          <MutationError error={h.profile.error} />
        </form>
      </DialogContent>
    </Dialog>
  );
}
export function ObservationEditor({
  value, pairedValue, recent, preset, mode, onClose, revision, h,
}: {
  value: Partial<HealthObservation>;
  pairedValue?: Partial<HealthObservation>;
  recent?: HealthObservation[];
  preset?: string | null;
  mode: "new" | "update" | "correct";
  onClose: () => void;
  revision: number;
  h: HealthQueries;
}) {
  const { t } = useT("health");
  const [selected, setSelected] = useState(preset || (mode === "correct" ? "existing" : ""));
  const [inputValue, setInputValue] = useState(mode === "correct" ? value.value || "" : "");
  const specs = [
    { id: "weight", name: t(($) => $.overview.weight), code: "weight", unit: "kg" },
    { id: "blood_pressure", name: t(($) => $.overview.pressure), code: "systolic_pressure", unit: "mmHg" },
    { id: "heart_rate", name: t(($) => $.overview.heart_rate), code: "resting_heart_rate", unit: "次/分" },
    { id: "height", name: t(($) => $.overview.height), code: "height", unit: "cm" },
  ];
  const spec = specs.find((item) => item.id === selected);
  const previous = value.value ? value : recent?.find((item) => item.code === spec?.code);
  const pressure = selected === "blood_pressure";
  const height = recent?.find((item) => item.code === "height");
  const weight = recent?.find((item) => item.code === "weight");
  const candidate = spec && { ...(selected === "height" ? height : weight), code: spec.code, value: inputValue, unit: spec.unit } as HealthObservation;
  const preview = selected === "weight" ? calculateBMI(height, candidate) : selected === "height" ? calculateBMI(candidate, weight) : null;
  const today = new Date().toLocaleDateString("en-CA");
  const pending = h.observation.isPending || h.observations.isPending;
  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const extra = {
      category: previous?.category || value.category || "一般检查",
      observedAt: String(form.get("observedAt") || ""),
      method: value.method || previous?.method || "",
      specimen: value.specimen || previous?.specimen || "",
      site: value.site || previous?.site || "",
      referenceRange: mode === "correct" ? value.referenceRange || "" : "",
    };
    if (pressure) {
      await h.observations.mutateAsync({ values: [
        { ...extra, name: t(($) => $.record.systolic), code: "systolic_pressure", value: String(form.get("systolic") || ""), unit: "mmHg", supersedes: mode === "correct" ? value.id : undefined },
        { ...extra, name: t(($) => $.record.diastolic), code: "diastolic_pressure", value: String(form.get("diastolic") || ""), unit: "mmHg", supersedes: mode === "correct" ? pairedValue?.id : undefined },
      ], revision });
    } else {
      await h.observation.mutateAsync({ value: {
        ...extra, name: mode === "correct" ? value.name : spec?.name,
        code: mode === "correct" ? value.code : spec?.code,
        value: inputValue, unit: mode === "correct" ? value.unit : spec?.unit,
        supersedes: mode === "correct" ? value.id : undefined,
      }, revision });
    }
    toast.success(t(($) => $.record.saved));
    onClose();
  };
  return <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
    <DialogContent className="max-h-[90dvh] overflow-y-auto overscroll-contain [&_[data-slot=dialog-close]]:size-11 max-sm:top-auto max-sm:bottom-0 max-sm:-translate-y-0 max-sm:rounded-b-none sm:max-h-[85dvh]">
      <DialogHeader><DialogTitle>{mode === "correct" ? t(($) => $.record.edit) : spec ? t(($) => $.record.record_metric, { name: spec.name }) : t(($) => $.record.add)}</DialogTitle>
        <DialogDescription>{mode === "correct" ? value.name : previous?.value ? t(($) => $.record.previous, { value: `${previous.value} ${previous.unit || ""}`.trim(), date: previous.observedAt || t(($) => $.page.unrecorded) }) : t(($) => $.record.choose_daily)}</DialogDescription>
      </DialogHeader>
      {!selected ? <div className="grid grid-cols-2 gap-3">{specs.map((item) => <Button key={item.id} variant="outline" className="min-h-12" onClick={() => { setSelected(item.id); setInputValue(""); }}>{item.name}</Button>)}</div> : <form className="space-y-4" onSubmit={(event) => void save(event).catch(() => {})}>
        {!preset && mode === "new" && <Button variant="ghost" type="button" className="min-h-10 px-0" onClick={() => setSelected("")}><ArrowLeft aria-hidden="true" />{t(($) => $.record.change_metric)}</Button>}
        {mode === "correct" && <dl className="rounded-lg bg-muted p-3 text-caption text-muted-foreground">{[value.category, value.method, value.specimen, value.site].filter(Boolean).join(" · ")}{value.unit && <p className="mt-1">{t(($) => $.record.unit)} · {value.unit}</p>}</dl>}
        {pressure ? <div className="grid grid-cols-2 gap-3">{[["systolic", t(($) => $.record.systolic), value.value], ["diastolic", t(($) => $.record.diastolic), pairedValue?.value]].map(([name, label, oldValue]) => <label key={name} className="block text-body">{label}<div className="relative mt-1"><Input name={name} type="number" min="0.000001" step="any" inputMode="decimal" autoComplete="off" className="h-11 pr-14 text-title-sm tabular-nums" defaultValue={mode === "correct" ? oldValue || "" : ""} required /><span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-caption text-muted-foreground">{t(($) => $.overview.unit_mmhg)}</span></div></label>)}</div> : <label className="block text-body">{t(($) => $.record.value)}<div className="relative mt-1"><Input name="value" type={spec ? "number" : "text"} min={spec ? "0.000001" : undefined} step={spec ? "any" : undefined} inputMode={spec ? "decimal" : "text"} autoComplete="off" className="h-11 pr-16 text-title-sm tabular-nums" value={inputValue} onChange={(event) => setInputValue(event.target.value)} required /><span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-caption text-muted-foreground">{mode === "correct" ? value.unit : spec?.unit}</span></div></label>}
        <label className="block text-body">{t(($) => $.record.date)}<Input name="observedAt" type="date" autoComplete="off" className="h-11" defaultValue={mode === "correct" ? value.observedAt || today : today} required /></label>
        {(selected === "weight" || selected === "height") && mode !== "correct" && <div aria-live="polite" className="rounded-lg bg-muted px-3 py-3"><div className="flex items-baseline justify-between gap-3"><span className="text-caption text-muted-foreground">{t(($) => $.bmi.preview)}</span><span className="text-title-sm font-semibold tabular-nums">{preview ? preview.value.toFixed(1) : "—"}</span></div><p className="mt-1 text-caption text-muted-foreground">{preview ? t(($) => $.bmi.preview_hint) : selected === "weight" && !calculateBMI(height, { value: "1", unit: "kg" } as HealthObservation) ? t(($) => $.bmi.need_height) : selected === "height" && !calculateBMI({ value: "1", unit: "m" } as HealthObservation, weight) ? t(($) => $.bmi.need_weight) : t(($) => $.bmi.enter_value)}</p></div>}
        <Button type="submit" className="min-h-11 w-full" disabled={pending}>{pending && <LoaderCircle aria-hidden="true" className="animate-spin" />}{pending ? t(($) => $.record.saving) : mode === "correct" ? t(($) => $.record.save_correction) : t(($) => $.record.save)}</Button>
        <div aria-live="polite"><MutationError error={h.observation.error} /><MutationError error={h.observations.error} /></div>
      </form>}
    </DialogContent>
  </Dialog>;
}

export function Trend({ observations }: { observations: HealthObservation[] }) {
  const { t } = useT("health");
  const points = observations
    .filter((o) => /^[-+]?\d+(\.\d+)?$/.test(o.value) && Number.isFinite(Date.parse(o.observedAt)))
    .sort((a, b) => a.observedAt.localeCompare(b.observedAt));
  if (points.length < 2)
    return (
      <p className="text-body text-muted-foreground">
        {t(($) => $.record.trend_empty)}
      </p>
    );
  const values = points.map((o) => Number(o.value));
  const firstDate = Date.parse(points[0]!.observedAt);
  const dateSpan = Date.parse(points.at(-1)!.observedAt) - firstDate || 1;
  const min = Math.min(...values);
  const max = Math.max(...values);
  const span = max - min || 1;
  const coordinates = points.map((o) => ({
    x: 35 + ((Date.parse(o.observedAt) - firstDate) / dateSpan) * 530,
    y: 165 - ((Number(o.value) - min) / span) * 135,
    o,
  }));
  return (
    <>
      <svg
        role="img"
        aria-label={t(($) => $.record.trend)}
        viewBox="0 0 600 210"
        className="w-full text-primary"
      >
        <polyline
          points={coordinates.map((p) => `${p.x},${p.y}`).join(" ")}
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
        />
        {coordinates.map((p) => (
          <g key={p.o.id}>
            <circle cx={p.x} cy={p.y} r="4" fill="currentColor">
              <title>
                {p.o.observedAt}: {p.o.value} {p.o.unit}
              </title>
            </circle>
            <text
              x={p.x}
              y={p.y - 10}
              textAnchor="middle"
              fill="currentColor"
              className="text-caption"
            >
              {p.o.value}
            </text>
          </g>
        ))}
      </svg>
      <p className="text-caption text-muted-foreground">
        {points[0]?.observedAt} — {points.at(-1)?.observedAt}
      </p>
    </>
  );
}

export function MutationError({ error }: { error: unknown }) {
  const { t } = useT("health");
  return error ? (
    <p role="alert" className="text-body text-destructive">
      {t(($) => $.record.error, {
        message: error instanceof Error ? error.message : String(error),
      })}
    </p>
  ) : null;
}

export function revisionDifferences(
  current: HealthObservation[],
  target: HealthObservation[],
) {
  const now = new Map(current.map((o) => [o.id, o]));
  const then = new Map(target.map((o) => [o.id, o]));
  return [...new Set([...now.keys(), ...then.keys()])]
    .filter((id) => !now.has(id) || !then.has(id))
    .map((id) => {
      const a = now.get(id);
      const b = then.get(id);
      const o = a || b!;
      return {
        id,
        label: `${o.name} · ${o.observedAt}: ${a ? `${a.value} ${a.unit}` : "—"} → ${b ? `${b.value} ${b.unit}` : "—"}`,
      };
    });
}
