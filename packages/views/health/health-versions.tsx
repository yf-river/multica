"use client";

import { healthMetricKey, type HealthObservation, type HealthProfile, type HealthRevision } from "@multica/core/health";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../i18n";

export function versionChanges(current: HealthObservation[], target: HealthObservation[]) {
  const now = new Map(current.map((item) => [healthMetricKey(item), item]));
  const then = new Map(target.map((item) => [healthMetricKey(item), item]));
  return [...new Set([...now.keys(), ...then.keys()])].flatMap((key) => {
    const before = now.get(key);
    const after = then.get(key);
    if (before?.id === after?.id) return [];
    return [{ key, kind: !before ? "added" as const : !after ? "removed" as const : "changed" as const, before, after }];
  });
}

export function RevisionCards({ items, current, profile, onSelect }: { items: HealthRevision[]; current: HealthObservation[]; profile: HealthProfile; onSelect: (revision: number) => void }) {
  const { t } = useT("health");
  return <div className="mt-6 grid gap-4 md:grid-cols-2">{items.map((item) => {
    const selected = item.observations.filter((observation) => item.state.currentObservationIds.includes(observation.id));
    const changes = versionChanges(current, selected);
    const profileCount = (["name", "sex", "birthDate"] as const).filter((field) => profile[field] !== item.state.profile[field]).length;
    return <article key={item.revision} className="flex min-w-0 flex-col rounded-xl border bg-surface p-5">
      <div className="flex items-start justify-between gap-3"><div><h3 className="text-title-sm font-semibold">{t(($) => $.revision.version, { revision: item.revision })}</h3><time className="mt-1 block text-caption tabular-nums text-muted-foreground">{formatTime(item.createdAt)}</time></div><span className="rounded-md bg-muted px-2 py-1 text-caption tabular-nums text-muted-foreground">{t(($) => $.record.items, { count: selected.length })}</span></div>
      <p className="mt-4 text-caption text-muted-foreground">{t(($) => $.revision.saved_before)}</p><p className="mt-1 break-words text-body">{item.reason}</p>
      <div className="mt-5 flex flex-wrap gap-x-4 gap-y-1 border-t pt-4 text-caption text-muted-foreground"><span>{t(($) => $.revision.change_count, { count: changes.length + profileCount })}</span>{["added", "changed", "removed"].map((kind) => { const count = changes.filter((change) => change.kind === kind).length; return count ? <span key={kind}>{t(($) => $.revision[kind as "added" | "changed" | "removed"])} {count}</span> : null; })}</div>
      <Button variant="outline" className="mt-4 min-h-11 w-full" onClick={() => onSelect(item.revision)}>{t(($) => $.revision.view)}</Button>
    </article>;
  })}</div>;
}

export function RevisionDiff({ current, profile, target }: { current: HealthObservation[]; profile: HealthProfile; target: HealthRevision }) {
  const { t } = useT("health");
  const selected = target.observations.filter((item) => target.state.currentObservationIds.includes(item.id));
  const changes = versionChanges(current, selected);
  const fields = (["name", "sex", "birthDate"] as const).filter((field) => profile[field] !== target.state.profile[field]);
  const labels = { name: t(($) => $.profile.name), sex: t(($) => $.profile.sex), birthDate: t(($) => $.profile.birth_date) };
  return <div className="space-y-5">
    <div className="grid grid-cols-2 gap-3 rounded-lg bg-muted p-3 text-caption"><span>{t(($) => $.revision.from_current)}</span><span>{t(($) => $.revision.to_target, { revision: target.revision })}</span></div>
    {fields.length > 0 && <section><h3 className="mb-2 text-body font-semibold">{t(($) => $.profile.title)}</h3><div className="divide-y rounded-lg border">{fields.map((field) => <div key={field} className="p-3"><p className="text-caption text-muted-foreground">{labels[field]}</p><div className="mt-1 grid grid-cols-2 gap-3 break-words text-body"><span>{profile[field] || "—"}</span><span className="font-medium">{target.state.profile[field] || "—"}</span></div></div>)}</div></section>}
    {(["added", "changed", "removed"] as const).map((kind) => {
      const group = changes.filter((change) => change.kind === kind);
      return group.length ? <section key={kind}><h3 className="mb-2 text-body font-semibold">{t(($) => $.revision[kind])} <span className="ml-1 font-normal text-muted-foreground">{group.length}</span></h3><div className="divide-y overflow-hidden rounded-lg border">{group.map((change) => <div key={change.key} className="p-3"><p className="break-words text-body font-medium">{change.after?.name || change.before?.name}</p><div className="mt-2 grid grid-cols-2 gap-3">{[change.before, change.after].map((item, index) => <div key={index} className="min-w-0"><p className={`break-words text-body tabular-nums ${index ? "font-medium" : "text-muted-foreground"}`}>{item ? `${item.value} ${item.unit}` : "—"}</p>{item && <p className="mt-1 text-caption text-muted-foreground">{item.observedAt || "—"}</p>}</div>)}</div></div>)}</div></section> : null;
    })}
    {!changes.length && !fields.length && <p className="rounded-lg border p-4 text-body text-muted-foreground">{t(($) => $.revision.no_changes)}</p>}
  </div>;
}

function formatTime(value: string) { const date = new Date(value); return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }).format(date); }
