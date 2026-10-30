import { mergeCostUsage } from "@multica/context-insights/cost";
import type {
  ContextTimeline,
  ContextActivity,
  ActivityDay,
  TimingTotals,
} from "@multica/context-insights/types";
import type { InsightSession } from "../api/schemas";

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object"
    ? (value as Record<string, unknown>)
    : null;
}
export function insightTimeline(
  session: InsightSession,
): ContextTimeline | null {
  const value = record(session.projections.contextTimeline);
  return value?.ok === true && record(value.current) !== null
    ? (value as unknown as ContextTimeline)
    : null;
}
function addNumbers<T extends object>(values: (T | undefined)[]): T {
  const out: Record<string, unknown> = {};
  for (const value of values)
    for (const [key, item] of Object.entries(value ?? {})) {
      if (typeof item === "number")
        out[key] = (typeof out[key] === "number" ? out[key] : 0) + item;
      else if (item !== null && typeof item === "object")
        out[key] = addNumbers([record(out[key]) ?? {}, item]);
    }
  return out as T;
}

export function aggregateInsightSessions(sessions: InsightSession[]) {
  const sorted = sessions.toSorted(
    (a, b) =>
      new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime(),
  );
  const latest = sorted.find((s) => !s.parent_session_id) ?? sorted[0];
  if (!latest) return null;
  const current = insightTimeline(latest);
  if (!current) return null;
  const timelines = sorted
    .map(insightTimeline)
    .filter((v): v is ContextTimeline => v !== null);
  const days: Record<string, ActivityDay> = {};
  for (const session of sorted) {
    const activity = record(session.projections.contextActivity);
    const source = record(activity?.days);
    for (const [day, value] of Object.entries(source ?? {})) {
      const next = value as ActivityDay;
      const previous = days[day];
      const skills = { ...previous?.skills };
      for (const [name, skill] of Object.entries(next.skills ?? {})) {
        const old = skills[name];
        skills[name] = {
          n: (old?.n ?? 0) + skill.n,
          last: Math.max(old?.last ?? 0, skill.last),
        };
      }
      days[day] = {
        ...addNumbers([previous, next]),
        cost: mergeCostUsage(previous?.cost, next.cost) ?? undefined,
        skills,
      };
    }
  }
  const cost = mergeCostUsage(...timelines.map((t) => t.cost));
  const timeline: ContextTimeline = {
    ...current,
    ...(cost ? { cost } : {}),
    timing: addNumbers<TimingTotals>(timelines.map((t) => t.timing)),
    humanInputs: timelines.reduce((sum, t) => sum + (t.humanInputs ?? 0), 0),
    answers: timelines.reduce((sum, t) => sum + (t.answers ?? 0), 0),
  };
  return { latest, timeline, activity: { days } satisfies ContextActivity };
}
