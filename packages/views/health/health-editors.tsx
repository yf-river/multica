"use client";
import { useState, type FormEvent } from "react";
import { useHealth, type HealthObservation, type HealthProfile } from "@multica/core/health";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@multica/ui/components/ui/dialog";
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
    onClose();
  };
  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) onClose();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t(($) => $.profile.edit)}</DialogTitle>
        </DialogHeader>
        <form
          className="space-y-4"
          onSubmit={(e) => void save(e).catch(() => {})}
        >
          <label className="block text-body">
            {t(($) => $.profile.name)}
            <Input name="name" defaultValue={value?.name} maxLength={100} />
          </label>
          <label className="block text-body">
            {t(($) => $.profile.sex)}
            <select
              name="sex"
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
              defaultValue={value?.birthDate}
            />
          </label>
          <p className="text-caption text-muted-foreground">
            {t(($) => $.report.original_age)}
          </p>
          <Button disabled={h.profile.isPending} type="submit">
            {t(($) => $.profile.save)}
          </Button>
          <MutationError error={h.profile.error} />
        </form>
      </DialogContent>
    </Dialog>
  );
}
export function ObservationEditor({
  value,
  pairedValue,
  preset,
  mode,
  onClose,
  revision,
  h,
}: {
  value: Partial<HealthObservation>;
  pairedValue?: Partial<HealthObservation>;
  preset?: string | null;
  mode: "new" | "update" | "correct";
  onClose: () => void;
  revision: number;
  h: HealthQueries;
}) {
  const { t } = useT("health");
  const [selected, setSelected] = useState(preset || (mode === "new" ? "" : "custom"));
  const presets = [
    { id: "weight", name: t(($) => $.overview.weight), code: "weight", unit: "kg" },
    { id: "blood_pressure", name: t(($) => $.overview.pressure), code: "blood_pressure", unit: "mmHg" },
    { id: "heart_rate", name: "心率", code: "resting_heart_rate", unit: "次/分" },
    { id: "height", name: t(($) => $.overview.height), code: "height", unit: "cm" },
    { id: "bmi", name: "BMI", code: "bmi", unit: "kg/m²" },
  ] as const;
  const spec = presets.find((item) => item.id === selected);
  const isPressure = selected === "blood_pressure";
  const today = new Date().toLocaleDateString("en-CA");
  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const get = (key: string) => String(form.get(key) ?? "");
    const observedAt = get("observedAt");
    const extra = {
      category: get("category") || value.category || "一般检查",
      observedAt,
      method: get("method"),
      specimen: get("specimen"),
      site: get("site"),
      referenceRange: get("referenceRange"),
    };
    if (isPressure) {
      await h.observations.mutateAsync({
        values: [
          { ...extra, name: t(($) => $.record.systolic), code: value.code === "systolic_pressure" ? value.code : "systolic_pressure", value: get("systolic"), unit: "mmHg", supersedes: mode === "correct" ? value.id : undefined },
          { ...extra, name: t(($) => $.record.diastolic), code: pairedValue?.code === "diastolic_pressure" ? pairedValue.code : "diastolic_pressure", value: get("diastolic"), unit: "mmHg", supersedes: mode === "correct" ? pairedValue?.id : undefined },
        ],
        revision,
      });
    } else {
      await h.observation.mutateAsync({
        value: {
          ...extra,
          name: spec?.name || get("name"),
          value: get("value"),
          unit: spec?.unit || get("unit"),
          code: mode === "update" ? value.code || spec?.code : mode === "correct" ? value.code : spec?.code,
          supersedes: mode === "correct" ? value.id : undefined,
        },
        revision,
      });
    }
    onClose();
  };
  const defaultDate = mode === "correct" ? value.observedAt || today : today;
  const pending = h.observation.isPending || h.observations.isPending;
  return (
    <Dialog
      open
      onOpenChange={(v) => {
        if (!v) onClose();
      }}
    >
      <DialogContent className="max-h-[85dvh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>
            {mode === "correct" ? t(($) => $.record.edit) : spec ? t(($) => $.record.update_metric, { name: spec.name }) : t(($) => $.record.add)}
          </DialogTitle>
        </DialogHeader>
        {value.uncertainty && (
          <p className="text-body text-destructive">{value.uncertainty}</p>
        )}
        {!selected && <div><p className="mb-3 text-caption text-muted-foreground">{t(($) => $.record.presets)}</p><div className="grid grid-cols-2 gap-2 sm:grid-cols-3">{presets.map((item) => <Button key={item.id} type="button" variant="outline" className="min-h-11 justify-start" onClick={() => setSelected(item.id)}>{item.name}</Button>)}<Button type="button" variant="outline" className="min-h-11 justify-start" onClick={() => setSelected("custom")}>{t(($) => $.record.other)}</Button></div></div>}
        {selected && <form className="space-y-4" onSubmit={(e) => void save(e).catch(() => {})}>
          {mode !== "correct" && <p className="text-caption text-muted-foreground">{t(($) => $.record.quick_hint)}</p>}
          {selected === "custom" && <div className="grid gap-4 sm:grid-cols-2"><label className="text-body">{t(($) => $.record.name)}<Input name="name" autoComplete="off" defaultValue={value.name ?? ""} required /></label><label className="text-body">{t(($) => $.record.unit)}<Input name="unit" autoComplete="off" defaultValue={value.unit ?? ""} /></label></div>}
          {isPressure ? <div className="grid grid-cols-2 gap-4"><label className="text-body">{t(($) => $.record.systolic)}<div className="relative mt-1"><Input name="systolic" inputMode="decimal" autoComplete="off" className="pr-14 tabular-nums" defaultValue={value.value ?? ""} required /><span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-caption text-muted-foreground">{t(($) => $.overview.unit_mmhg)}</span></div></label><label className="text-body">{t(($) => $.record.diastolic)}<div className="relative mt-1"><Input name="diastolic" inputMode="decimal" autoComplete="off" className="pr-14 tabular-nums" defaultValue={pairedValue?.value ?? ""} required /><span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-caption text-muted-foreground">{t(($) => $.overview.unit_mmhg)}</span></div></label></div> : <label className="block text-body">{t(($) => $.record.value)}<div className="relative mt-1"><Input name="value" inputMode="decimal" autoComplete="off" className={spec?.unit ? "pr-16 tabular-nums" : "tabular-nums"} defaultValue={value.value ?? ""} required />{spec?.unit && <span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-caption text-muted-foreground">{spec.unit}</span>}</div></label>}
          <label className="block text-body">{t(($) => $.record.date)}<Input name="observedAt" type="date" autoComplete="off" defaultValue={defaultDate} required /></label>
          <details className="rounded-md border px-3 py-2"><summary className="min-h-8 cursor-pointer text-body font-medium">{t(($) => $.record.more)}</summary><div className="mt-3 grid gap-4 sm:grid-cols-2"><label className="text-body">{t(($) => $.record.category)}<Input name="category" autoComplete="off" defaultValue={value.category || "一般检查"} /></label>{[["method", t(($) => $.record.method), value.method], ["specimen", t(($) => $.record.specimen), value.specimen], ["site", t(($) => $.record.site), value.site], ["referenceRange", t(($) => $.record.range), value.referenceRange]].map(([key,label,defaultValue]) => <label key={key} className="text-body">{label}<Input name={key} autoComplete="off" defaultValue={defaultValue ?? ""} /></label>)}</div></details>
          {value.evidence && (
            <p className="whitespace-pre-wrap break-words text-caption text-muted-foreground">
              {value.evidence}
            </p>
          )}
          <Button
            className="w-full"
            disabled={pending}
            type="submit"
          >
            {t(($) => $.record.save)}
          </Button>
          <div aria-live="polite">
            <MutationError error={h.observation.error} />
            <MutationError error={h.observations.error} />
          </div>
        </form>}
      </DialogContent>
    </Dialog>
  );
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
