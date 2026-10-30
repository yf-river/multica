// 待吸收页签：按状态/结论/范围/关键词筛选台账，勾选后只改标记。
"use client";

import { useEffect, useMemo, useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { ArrowUpRight, ChevronRight } from "lucide-react";
import { useWorkspacePaths } from "@multica/core/paths";
import { upstreamItemsOptions, type UpstreamItem, type UpstreamSnapshot } from "@multica/core/upstream/queries";
import { useCreateUpstreamAbsorptionTask, useMarkUpstreamItems } from "@multica/core/upstream/mutations";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button, buttonVariants } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { SegmentedToggle } from "../../common/segmented-toggle";
import { AppLink } from "../../navigation/app-link";
import { useNavigation } from "../../navigation/context";
import { useT } from "../../i18n";

const STATUS_FILTERS = ["open", "adopted", "ignored", "all"] as const;
const ANY = "any";
const VERDICT_FILTERS = [ANY, "absorb", "review", "skip"] as const;
const PRESENCE_FILTERS = [ANY, "local", "partial", "absent"] as const;

function formatDay(value: string | null): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleDateString();
}

export function PendingTab({
  wsId,
  projectId = "multica",
  snapshot,
  onOpenSettings,
}: {
  wsId: string;
  projectId?: string;
  snapshot?: UpstreamSnapshot;
  onOpenSettings: () => void;
}) {
  const { t } = useT("upstream");
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const [status, setStatus] = useState<(typeof STATUS_FILTERS)[number]>("open");
  // 修改 1. 下拉选「全部结论」时用 any 占位，空串会被 Select 当成未选择、显示成占位灰字。
  const [verdict, setVerdict] = useState<(typeof VERDICT_FILTERS)[number]>(ANY);
  const [presence, setPresence] = useState<(typeof PRESENCE_FILTERS)[number]>(ANY);
  const [q, setQ] = useState("");
  const [queryInput, setQueryInput] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [expanded, setExpanded] = useState<string | null>(null);

  const { data, isLoading, error, hasNextPage, fetchNextPage, isFetchingNextPage } = useInfiniteQuery(
    upstreamItemsOptions(wsId, {
      project_id: projectId,
      status,
      verdict: verdict === ANY ? "" : verdict,
      presence: presence === ANY ? "" : presence,
      q,
    }),
  );
  const mark = useMarkUpstreamItems();
  const createTask = useCreateUpstreamAbsorptionTask(projectId);
  const items = useMemo(() => data?.pages.flatMap((page) => page.items) ?? [], [data]);
  const canWrite = snapshot?.can_write ?? false;

  // 1. 搜索按 300ms 防抖后再改查询键，避免逐键请求互相打断。
  useEffect(() => {
    const timer = setTimeout(() => setQ(queryInput.trim()), 300);
    return () => clearTimeout(timer);
  }, [queryInput]);

  const toggle = (id: string) => {
    setSelected((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));
  };
  const allSelected = items.length > 0 && items.every((item) => selected.includes(item.id));
  const toggleAll = () => {
    setSelected(allSelected ? [] : items.map((item) => item.id));
  };

  const apply = (next: "adopted" | "ignored" | "open") => {
    if (selected.length === 0) return;
    mark.mutate({ ids: selected, status: next, project_id: projectId }, { onSuccess: () => setSelected([]) });
  };

  // 修改 5. 每行都有一个「创建吸收任务」，点一下就在当前工作区真建一个任务；先确认，
  //    成功后直接打开这个任务，省掉回列表再找一遍。
  const createTaskFor = (itemId: string) => {
    if (!window.confirm(t(($) => $.page.bulk.create_task_confirm))) return;
    createTask.mutate(itemId, {
      onSuccess: (result) => navigation.push(paths.issueDetail(result.issue_id)),
    });
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
      case "all":
        return t(($) => $.page.filters.status_all);
      default:
        return t(($) => $.page.filters.status_open);
    }
  };

  const verdictItems = VERDICT_FILTERS.map((value) => ({
    value,
    label: value === ANY ? t(($) => $.page.filters.any_verdict) : verdictLabel(value),
  }));
  const presenceItems = PRESENCE_FILTERS.map((value) => ({
    value,
    label: value === ANY ? t(($) => $.page.filters.any_presence) : presenceLabel(value),
  }));

  // 空态分三种：没配置执行体、配置了还没跑、跑完没有匹配建议。
  const emptyState = (() => {
    if (!snapshot?.config.agent_id || snapshot.executor_state === "none") {
      return { title: t(($) => $.page.pending.empty_unconfigured), hint: t(($) => $.page.pending.empty_unconfigured_hint), action: true };
    }
    if (!snapshot.last_run) {
      return { title: t(($) => $.page.pending.empty_not_run), hint: t(($) => $.page.pending.empty_not_run_hint), action: false };
    }
    return { title: t(($) => $.page.pending.empty), hint: t(($) => $.page.pending.empty_hint), action: false };
  })();

  return (
    <div className="space-y-3">
      {/* 修改 2. 原来四个状态是四个独立按钮、两个筛选是浏览器原生下拉，跟页面其它控件不像一套；
          改成同宽的分段控件 + 统一的下拉，并把「全选本页」推到行尾。
          窄屏下分段控件独占一行、两个下拉平分第二行，避免一个下拉被挤到行首。 */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="w-full sm:w-auto">
          <SegmentedToggle
            value={status}
            options={STATUS_FILTERS.map((value) => [value, statusLabel(value)] as const)}
            onChange={(next) => {
              setStatus(next);
              setSelected([]);
            }}
            buttonClassName="px-3 py-1.5 text-caption"
          />
        </div>
        <div className="flex w-full gap-2 sm:w-auto">
          <Select
            items={verdictItems}
            value={verdict}
            onValueChange={(value) => {
              setVerdict((value ?? ANY) as (typeof VERDICT_FILTERS)[number]);
              setSelected([]);
            }}
          >
            <SelectTrigger size="sm" className="h-8 flex-1 text-caption sm:flex-none" aria-label={t(($) => $.page.filters.any_verdict)}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {verdictItems.map((item) => (
                <SelectItem key={item.value} value={item.value}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select
            items={presenceItems}
            value={presence}
            onValueChange={(value) => {
              setPresence((value ?? ANY) as (typeof PRESENCE_FILTERS)[number]);
              setSelected([]);
            }}
          >
            <SelectTrigger size="sm" className="h-8 flex-1 text-caption sm:flex-none" aria-label={t(($) => $.page.filters.any_presence)}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {presenceItems.map((item) => (
                <SelectItem key={item.value} value={item.value}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <Input
          className="h-8 w-full sm:w-56"
          value={queryInput}
          placeholder={t(($) => $.page.filters.search)}
          aria-label={t(($) => $.page.filters.search)}
          onChange={(e) => {
            setQueryInput(e.target.value);
            setSelected([]);
          }}
        />
        {items.length > 0 ? (
          <label className="flex items-center gap-1.5 text-caption text-muted-foreground sm:ml-auto">
            <Checkbox checked={allSelected} onCheckedChange={toggleAll} disabled={!canWrite} aria-label={t(($) => $.page.bulk.select_all)} />
            {t(($) => $.page.bulk.select_all)}
          </label>
        ) : null}
      </div>

      {error ? <p role="alert" className="text-caption text-destructive">{error.message}</p> : null}
      {mark.isError ? <p role="alert" className="text-caption text-destructive">{mark.error.message}</p> : null}
      {createTask.isError ? <p role="alert" className="text-caption text-destructive">{createTask.error.message}</p> : null}
      {isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-20 w-full" />
          <Skeleton className="h-20 w-full" />
        </div>
      ) : items.length === 0 ? (
        <div className="rounded-lg border border-dashed border-surface-border px-4 py-10 text-center">
          <p className="text-body font-medium">{emptyState.title}</p>
          <p className="mt-1 text-caption text-muted-foreground">{emptyState.hint}</p>
          {emptyState.action && canWrite ? (
            <Button size="sm" variant="secondary" className="mt-3" onClick={onOpenSettings}>
              {t(($) => $.page.setup.title)}
            </Button>
          ) : null}
        </div>
      ) : (
        <ul className="divide-y divide-surface-border rounded-lg border border-surface-border">
          {items.map((item) => (
            <ItemRow
              key={item.id}
              item={item}
              checked={selected.includes(item.id)}
              expanded={expanded === item.id}
              canWrite={canWrite}
              creatingTask={createTask.isPending}
              issueHref={item.absorption_issue_id ? paths.issueDetail(item.absorption_issue_id) : ""}
              onToggle={() => toggle(item.id)}
              onExpand={() => setExpanded(expanded === item.id ? null : item.id)}
              onRestore={() => mark.mutate({ ids: [item.id], status: "open" })}
              onCreateTask={() => createTaskFor(item.id)}
            />
          ))}
        </ul>
      )}

      {hasNextPage ? <Button size="sm" variant="ghost" disabled={isFetchingNextPage} onClick={() => void fetchNextPage()}>{t(($) => $.page.pending.load_more)}</Button> : null}
      {selected.length > 0 && canWrite ? (
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
            <Button size="sm" variant="ghost" disabled={mark.isPending} onClick={() => apply("open")}>
              {t(($) => $.page.bulk.restore)}
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
  canWrite,
  creatingTask,
  issueHref,
  onToggle,
  onExpand,
  onRestore,
  onCreateTask,
}: {
  item: UpstreamItem;
  checked: boolean;
  expanded: boolean;
  canWrite: boolean;
  creatingTask: boolean;
  issueHref: string;
  onToggle: () => void;
  onExpand: () => void;
  onRestore: () => void;
  onCreateTask: () => void;
}) {
  const { t } = useT("upstream");
  const verdictVariant = item.verdict === "absorb" ? "default" : item.verdict === "skip" ? "secondary" : "outline";
  const verdictLabel =
    item.verdict === "absorb" ? t(($) => $.page.item.absorb) : item.verdict === "skip" ? t(($) => $.page.item.skip) : t(($) => $.page.item.review);
  const presenceLabel =
    item.presence === "absent" ? t(($) => $.page.item.absent) : item.presence === "partial" ? t(($) => $.page.item.partial) : t(($) => $.page.item.local);
  const confidenceLabel =
    item.confidence === "high"
      ? t(($) => $.page.item.confidence_high)
      : item.confidence === "low"
        ? t(($) => $.page.item.confidence_low)
        : t(($) => $.page.item.confidence_medium);
  // 修改 3. 三枚描边徽标 + 三段灰字原来并排成七个等重标签，扫视时找不到重点。
  //    结论留徽标，范围/置信/提交数/最早时间并成一段灰字，读起来是一句补充说明。
  const facts = [
    presenceLabel,
    confidenceLabel,
    t(($) => $.page.item.commits, { count: item.commit_count }),
    item.first_commit_at ? t(($) => $.page.item.first_commit, { date: formatDay(item.first_commit_at) }) : "",
  ].filter(Boolean);

  return (
    <li className="flex flex-wrap items-start gap-3 px-4 py-3">
      <Checkbox className="mt-1" checked={checked} onCheckedChange={onToggle} aria-label={item.title} disabled={!canWrite} />
      <div className="min-w-0 flex-1">
        <button type="button" className="flex w-full items-start gap-1.5 break-words text-left" aria-expanded={expanded} onClick={onExpand}>
          <ChevronRight className={`mt-0.5 size-3.5 shrink-0 text-muted-foreground transition-transform ${expanded ? "rotate-90" : ""}`} />
          <span className="min-w-0">
            <span className="block text-body font-medium">{item.title}</span>
            {item.summary ? <span className="mt-0.5 block text-caption text-muted-foreground">{item.summary}</span> : null}
          </span>
        </button>
        <div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 pl-5">
          <Badge variant={verdictVariant}>{verdictLabel}</Badge>
          {item.status === "adopted" ? <Badge variant="secondary">{t(($) => $.page.filters.status_adopted)}</Badge> : null}
          {item.status === "ignored" ? <Badge variant="outline" className="text-muted-foreground">{t(($) => $.page.filters.status_ignored)}</Badge> : null}
          {item.status === "adopted" && item.has_new_commits ? (
            <Badge variant="outline" className="border-warning/50 text-warning">{t(($) => $.page.item.new_commits)}</Badge>
          ) : null}
          <span className="text-caption text-faint-foreground">{facts.join(" · ")}</span>
        </div>
        {expanded ? (
          <div className="mt-2 space-y-2 rounded-md bg-muted/30 px-3 py-2 pl-5">
            {item.rationale ? (
              <div>
                <div className="text-caption font-medium text-muted-foreground">{t(($) => $.page.item.rationale)}</div>
                <p className="mt-0.5 break-words text-caption">{item.rationale}</p>
              </div>
            ) : null}
            {item.paths.length > 0 ? (
              <div>
                <div className="text-caption font-medium text-muted-foreground">{t(($) => $.page.item.paths)}</div>
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
      {/* 修改 4. 操作原来混在元信息里、且「打开吸收任务 · 已创建吸收任务」重复了一遍；
          现在操作独立成一列，窄屏落到行尾，任务链接只说一次。 */}
      <div className="flex w-full shrink-0 items-center justify-end gap-1 pl-8 sm:w-auto sm:pl-0">
        {item.status === "adopted" && canWrite ? (
          <Button
            size="sm"
            variant="ghost"
            className="h-7 px-2 text-caption text-muted-foreground"
            onClick={onRestore}
          >
            {t(($) => $.page.bulk.restore)}
          </Button>
        ) : null}
        {item.absorption_issue_id ? (
          <AppLink
            href={issueHref}
            className={cn(buttonVariants({ variant: "ghost", size: "sm" }), "h-7 gap-1 px-2 text-caption")}
          >
            {t(($) => $.page.bulk.open_task)}
            <ArrowUpRight aria-hidden="true" className="size-3" />
          </AppLink>
        ) : canWrite ? (
          <Button size="sm" variant="ghost" className="h-7 px-2 text-caption" disabled={creatingTask} onClick={onCreateTask}>
            {t(($) => $.page.bulk.create_task)}
          </Button>
        ) : null}
      </div>
    </li>
  );
}
