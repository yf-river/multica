// 待吸收页签：按结论/范围/关键词筛选台账，勾选后只改标记。
"use client";

import { useEffect, useMemo, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { ChevronRight } from "lucide-react";
import { upstreamItemsOptions, type UpstreamItem } from "@multica/core/upstream/queries";
import { useCreateUpstreamAbsorptionTask, useMarkUpstreamItems } from "@multica/core/upstream/mutations";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useT } from "../../i18n";

const STATUS_FILTERS = ["open", "adopted", "ignored"] as const;
const VERDICT_FILTERS = ["", "absorb", "review", "skip"] as const;

export function PendingTab({ wsId }: { wsId: string }) {
  const { t } = useT("upstream");
  const [status, setStatus] = useState<(typeof STATUS_FILTERS)[number]>("open");
  const [verdict, setVerdict] = useState("");
  const [q, setQ] = useState("");
  const [queryInput, setQueryInput] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [expanded, setExpanded] = useState<string | null>(null);

  const { data, isLoading, error, hasNextPage, fetchNextPage, isFetchingNextPage } = useInfiniteQuery(upstreamItemsOptions(wsId, { status, verdict, q }));
  const mark = useMarkUpstreamItems();
  const createTask = useCreateUpstreamAbsorptionTask();
  const items = useMemo(() => data?.pages.flatMap((page) => page.items) ?? [], [data]);

  // 1. 搜索按 300ms 防抖后再改查询键，避免逐键请求互相打断。
  useEffect(() => {
    const timer = setTimeout(() => setQ(queryInput.trim()), 300);
    return () => clearTimeout(timer);
  }, [queryInput]);

  const toggle = (id: string) => {
    setSelected((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));
  };

  const apply = (next: "adopted" | "ignored" | "open") => {
    if (selected.length === 0) return;
    mark.mutate({ ids: selected, status: next }, { onSuccess: () => setSelected([]) });
  };

  const verdictLabel = (v: string) => {
    switch (v) {
      case "absorb":
        return t(($) => $.page.item.absorb);
      case "skip":
        return t(($) => $.page.item.skip);
      default:
        return t(($) => $.page.item.review);
    }
  };

  const presenceLabel = (p: string) => {
    switch (p) {
      case "absent":
        return t(($) => $.page.item.absent);
      case "partial":
        return t(($) => $.page.item.partial);
      default:
        return t(($) => $.page.item.local);
    }
  };

  const statusLabel = (s: string) => {
    switch (s) {
      case "adopted":
        return t(($) => $.page.filters.status_adopted);
      case "ignored":
        return t(($) => $.page.filters.status_ignored);
      default:
        return t(($) => $.page.filters.status_open);
    }
  };

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        {STATUS_FILTERS.map((value) => (
          <Button
            key={value}
            size="sm"
            variant={status === value ? "secondary" : "ghost"}
            onClick={() => {
              setStatus(value);
              setSelected([]);
            }}
          >
            {statusLabel(value)}
          </Button>
        ))}
        <select
          className="h-8 rounded-md border border-input bg-background px-2 text-caption"
          aria-label={t(($) => $.page.filters.any_verdict)}
          value={verdict}
          onChange={(e) => { setVerdict(e.target.value); setSelected([]); }}
        >
          {VERDICT_FILTERS.map((value) => (
            <option key={value || "any"} value={value}>
              {value === "" ? t(($) => $.page.filters.any_verdict) : verdictLabel(value)}
            </option>
          ))}
        </select>
        <Input
          className="h-8 w-56"
          value={queryInput}
          placeholder={t(($) => $.page.filters.search)}
          aria-label={t(($) => $.page.filters.search)}
          onChange={(e) => { setQueryInput(e.target.value); setSelected([]); }}
        />
      </div>

      {error ? <p role="alert" className="text-caption text-danger">{error.message}</p> : null}
      {mark.isError ? <p role="alert" className="text-caption text-danger">{mark.error.message}</p> : null}
      {isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-20 w-full" />
          <Skeleton className="h-20 w-full" />
        </div>
      ) : items.length === 0 ? (
        <div className="rounded-lg border border-dashed border-surface-border px-4 py-10 text-center">
          <p className="text-body font-medium">{t(($) => $.page.pending.empty)}</p>
          <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.page.pending.empty_hint)}</p>
        </div>
      ) : (
        <ul className="divide-y divide-surface-border rounded-lg border border-surface-border">
          {items.map((item) => (
            <ItemRow
              key={item.id}
              item={item}
              checked={selected.includes(item.id)}
              expanded={expanded === item.id}
              onToggle={() => toggle(item.id)}
              onExpand={() => setExpanded(expanded === item.id ? null : item.id)}
              verdictLabel={verdictLabel(item.verdict)}
              presenceLabel={presenceLabel(item.presence)}
              commitsLabel={t(($) => $.page.item.commits, { count: item.commit_count })}
              rationaleLabel={t(($) => $.page.item.rationale)}
              pathsLabel={t(($) => $.page.item.paths)}
              adoptedLabel={statusLabel("adopted")}
              restoreLabel={t(($) => $.page.bulk.restore)}
              onRestore={() => mark.mutate({ ids: [item.id], status: "open" })}
              createTaskLabel={t(($) => $.page.bulk.create_task)}
              taskCreatedLabel={t(($) => $.page.bulk.task_created)}
              onCreateTask={() => createTask.mutate(item.id)}
            />
          ))}
        </ul>
      )}

      {hasNextPage ? <Button size="sm" variant="ghost" disabled={isFetchingNextPage} onClick={() => void fetchNextPage()}>{t(($) => $.page.pending.load_more)}</Button> : null}
      {selected.length > 0 ? (
        <div className="sticky bottom-3 flex flex-wrap items-center justify-between gap-2 rounded-lg border border-surface-border bg-background px-3 py-2 shadow-sm">
          <span className="text-caption text-muted-foreground">
            {t(($) => $.page.bulk.selected, { count: selected.length })}
          </span>
          <div className="flex items-center gap-2">
            <Button size="sm" disabled={mark.isPending} onClick={() => apply("adopted")}>
              {t(($) => $.page.bulk.adopt)}
            </Button>
            <Button size="sm" variant="ghost" disabled={mark.isPending} onClick={() => apply("ignored")}>
              {t(($) => $.page.bulk.ignore)}
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  );
}

function ItemRow({
  item,
  checked,
  expanded,
  onToggle,
  onExpand,
  verdictLabel,
  presenceLabel,
  commitsLabel,
  rationaleLabel,
  pathsLabel,
  adoptedLabel,
  restoreLabel,
  onRestore,
  createTaskLabel,
  taskCreatedLabel,
  onCreateTask,
}: {
  item: UpstreamItem;
  checked: boolean;
  expanded: boolean;
  onToggle: () => void;
  onExpand: () => void;
  verdictLabel: string;
  presenceLabel: string;
  commitsLabel: string;
  rationaleLabel: string;
  pathsLabel: string;
  adoptedLabel: string;
  restoreLabel: string;
  onRestore: () => void;
  createTaskLabel: string;
  taskCreatedLabel: string;
  onCreateTask: () => void;
}) {
  const verdictVariant = item.verdict === "absorb" ? "default" : item.verdict === "skip" ? "secondary" : "outline";
  return (
    <li className="px-3 py-3">
      <div className="flex items-start gap-3">
        <Checkbox className="mt-1" checked={checked} onCheckedChange={onToggle} aria-label={item.title} />
        <div className="min-w-0 flex-1">
          <button type="button" className="flex w-full items-start gap-1.5 break-words text-left" aria-expanded={expanded} onClick={onExpand}>
            <ChevronRight className={`mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform ${expanded ? "rotate-90" : ""}`} />
            <span className="min-w-0">
              <span className="block text-body font-medium">{item.title}</span>
              {item.summary ? (
                <span className="mt-0.5 block text-caption text-muted-foreground">{item.summary}</span>
              ) : null}
            </span>
          </button>
          <div className="mt-1.5 flex flex-wrap items-center gap-1.5 pl-5">
            <Badge variant={verdictVariant}>{verdictLabel}</Badge>
            <Badge variant="outline" className={item.presence === "absent" ? "border-dashed text-muted-foreground" : ""}>
              {presenceLabel}
            </Badge>
            <span className="text-caption text-faint-foreground">{commitsLabel}</span>
            {item.status === "adopted" ? (
              <span className="inline-flex items-center gap-1 text-caption text-muted-foreground">
                {adoptedLabel}
                <button type="button" className="underline" onClick={onRestore}>
                  {restoreLabel}
                </button>
              </span>
            ) : null}
            {item.absorption_issue_id ? (
              <span className="text-caption text-muted-foreground">{taskCreatedLabel}</span>
            ) : (
              <Button size="sm" variant="ghost" className="h-6 px-2 text-caption" onClick={onCreateTask}>
                {createTaskLabel}
              </Button>
            )}
          </div>
          {expanded ? (
            <div className="mt-2 space-y-2 rounded-md bg-muted/30 px-3 py-2 pl-5">
              {item.rationale ? (
                <div>
                  <div className="text-caption font-medium text-muted-foreground">{rationaleLabel}</div>
                  <p className="mt-0.5 break-words text-caption">{item.rationale}</p>
                </div>
              ) : null}
              {item.paths.length > 0 ? (
                <div>
                  <div className="text-caption font-medium text-muted-foreground">{pathsLabel}</div>
                  <ul className="mt-0.5 space-y-0.5">
                    {item.paths.map((p) => (
                      <li key={p} className="break-all font-mono text-caption text-muted-foreground">
                        {p}
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}
              {item.commit_shas.length > 0 ? (
                <div className="flex flex-wrap gap-1">
                  {item.commit_shas.map((sha) => (
                    <code key={sha} className="rounded bg-muted px-1 py-0.5 font-mono text-caption">
                      {sha.slice(0, 8)}
                    </code>
                  ))}
                </div>
              ) : null}
            </div>
          ) : null}
        </div>
      </div>
    </li>
  );
}
