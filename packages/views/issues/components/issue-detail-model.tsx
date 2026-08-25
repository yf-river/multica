import type { Issue, TimelineEntry } from "@multica/core/types";
import { Skeleton } from "@multica/ui/components/ui/skeleton";

export const EMPTY_REPLIES: TimelineEntry[] = [];

// ---------------------------------------------------------------------------
// Sidebar progressive disclosure
// ---------------------------------------------------------------------------
//
// Properties shown in the sidebar split into two groups:
//   - core: always rendered (status / assignee / project)
//   - optional: rendered only when the issue has a value for that field OR
//     the user explicitly added it via "+ Add property" in this session
//     (priority / due_date / labels)
//
// Parent is not in either group — it has its own standalone section below
// the Properties block, rendered only when the issue actually has a parent.
//
// `OPTIONAL_PROP_KEYS` is the open set — adding a new optional field
// means appending here, wiring its row in the JSX switch below, and
// adding a locale key. The picker, visibility rules, and add-property
// menu all flow from this one list.
export const OPTIONAL_PROP_KEYS = ["priority", "start_date", "due_date", "labels"] as const;
export type OptionalPropKey = (typeof OPTIONAL_PROP_KEYS)[number];

export function isOptionalPropSet(
  issue: Issue,
  key: OptionalPropKey,
  attachedLabelsCount: number,
): boolean {
  switch (key) {
    case "priority":
      return issue.priority !== "none";
    case "start_date":
      return !!issue.start_date;
    case "due_date":
      return !!issue.due_date;
    case "labels":
      return attachedLabelsCount > 0;
  }
}

// Shallow array equality by element identity. Used to reuse the previous
// render's per-thread reply slice when nothing in *this* thread changed,
// even if the surrounding `timeline` array was rebuilt by a WS event in
// some unrelated thread.
export function shallowEqualEntries(a: TimelineEntry[], b: TimelineEntry[]): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false;
  }
  return true;
}

// Flat per-item shape consumed by <Virtuoso>. Virtuoso needs a flat array
// where each entry is one rendered row; we keep the grouping logic from
// `timelineView.groups` (consecutive same-actor activities still collapse
// into one activity-group row) but project it into a discriminated union
// the itemContent dispatcher can switch on.
export type TimelineItem =
  | { kind: "comment"; id: string; entry: TimelineEntry }
  | { kind: "resolved-bar"; id: string; entry: TimelineEntry }
  | { kind: "activity-group"; id: string; entries: TimelineEntry[] };

type RawTimelineGroup = {
  type: "comment" | "activities";
  entries: TimelineEntry[];
};

export function flattenGroups(
  groups: ReadonlyArray<RawTimelineGroup>,
  expandedResolved: ReadonlySet<string>,
): TimelineItem[] {
  const out: TimelineItem[] = [];
  for (const group of groups) {
    if (group.type === "comment") {
      const entry = group.entries[0]!;
      const isResolved = !!entry.resolved_at;
      const isExpanded = expandedResolved.has(entry.id);
      out.push(
        isResolved && !isExpanded
          ? { kind: "resolved-bar", id: entry.id, entry }
          : { kind: "comment", id: entry.id, entry },
      );
    } else {
      out.push({
        kind: "activity-group",
        id: group.entries[0]!.id,
        entries: group.entries,
      });
    }
  }
  return out;
}

export function TimelineSkeleton() {
  return (
    <div className="mt-4 flex flex-col gap-3">
      {[0, 1].map((i) => (
        <div key={i} className="flex gap-3 p-4">
          <Skeleton className="h-10 w-10 shrink-0 rounded-full" />
          <div className="flex-1 space-y-2">
            <Skeleton className="h-4 w-32" />
            <Skeleton className="h-4 w-full" />
            <Skeleton className="h-4 w-4/5" />
          </div>
        </div>
      ))}
    </div>
  );
}
