"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Search, Settings2 } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/paths";
import { supervisionKeys, supervisionListOptions, supervisionSnapshotOptions } from "@multica/core/supervision/queries";
import { cn } from "@multica/ui/lib/utils";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { useNavigation } from "../../../navigation";
import { useT } from "../../../i18n";
import { statusLabel } from "../run-review-format";
import { SupervisionDetail } from "./supervision-detail";
import { SupervisionSettings } from "./supervision-settings";
import { errorText, moment, reviewState, type ReviewState } from "./supervision-shared";

const FILTERS = ["all", "problems", "incomplete", "pending", "failed"] as const;

/** Quality supervision tab: overview, filters, review list, and its two sheets. */
export function QualitySupervision() {
  const { t } = useT("run-reviews");
  const wsId = useWorkspaceId();
  const nav = useNavigation();
  const queryClient = useQueryClient();
  const params = nav.searchParams;
  const search = params.get("search") ?? "";
  const rawFilter = params.get("filter") ?? "all";
  const filter = (FILTERS as readonly string[]).includes(rawFilter) ? rawFilter : "all";
  const project = params.get("project") ?? "";
  const offset = Math.max(0, Number(params.get("offset")) || 0);
  const selected = params.get("selected") ?? "";
  const [settings, setSettings] = useState(false);
  const snapshot = useQuery(supervisionSnapshotOptions(wsId));
  const list = useQuery(supervisionListOptions(wsId, search, filter, offset, project));
  const projects = useQuery({ queryKey: [...supervisionKeys.all(wsId), "projects"], queryFn: () => api.listProjects() });
  const data = snapshot.data;
  const invalidate = () => queryClient.invalidateQueries({ queryKey: supervisionKeys.all(wsId) });
  const check = useMutation({
    mutationFn: (body: Parameters<typeof api.checkSupervision>[0]) => api.checkSupervision(body),
    onSuccess: (_, variables) => {
      void invalidate();
      toast.success(variables.stop ? t(($) => $.supervision.stopped) : t(($) => $.supervision.scheduled));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const route = (change: Record<string, string | null>) => {
    const next = new URLSearchParams(params);
    for (const [key, value] of Object.entries(change)) {
      if (value) next.set(key, value);
      else next.delete(key);
    }
    nav.replace(nav.pathname + "?" + next.toString());
  };
  if (snapshot.isPending) return <div className="p-6 text-muted-foreground" role="status">{t(($) => $.supervision.loading)}</div>;
  if (snapshot.isError || !data) {
    return (
      <div role="alert" className="space-y-3 p-6">
        <p>{t(($) => $.supervision.load_failed)}</p>
        <p className="text-muted-foreground">{errorText(snapshot.error)}</p>
        <Button onClick={() => void snapshot.refetch()}>{t(($) => $.supervision.retry)}</Button>
      </div>
    );
  }
  const config = data.config;
  const summary = data.summary;
  const paused = !config.enabled && !config.error && Boolean(config.agent_id);
  const filtersActive = Boolean(search || project || filter !== "all");
  const stateLabels: Record<ReviewState, string> = {
    no_runs: t(($) => $.supervision.status_no_runs),
    running: t(($) => $.supervision.status_running),
    stopped: t(($) => $.supervision.status_stopped),
    failed: t(($) => $.supervision.status_failed),
    retrying: t(($) => $.supervision.status_retrying),
    changed: t(($) => $.supervision.status_changed),
    pending: t(($) => $.supervision.status_pending),
    covered: t(($) => $.supervision.status_covered),
  };
  const stateTones: Record<ReviewState, string> = {
    no_runs: "text-muted-foreground",
    running: "border-info/40 bg-info/10 text-info",
    stopped: "text-muted-foreground",
    failed: "border-destructive/40 bg-destructive/10 text-destructive",
    retrying: "border-warning/40 bg-warning/10 text-warning",
    changed: "border-warning/40 bg-warning/10 text-warning",
    pending: "border-warning/40 bg-warning/10 text-warning",
    covered: "text-muted-foreground",
  };
  return (
    <div className="min-h-0 flex-1 overflow-y-auto p-4 sm:p-6">
      <div className="mb-5 flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2 text-body font-medium">
            <span aria-hidden className={cn("h-2 w-2 rounded-full", (config.error || summary.failed > 0) ? "bg-destructive" : config.enabled ? "bg-info" : "bg-muted-foreground")} />
            {config.error ? t(($) => $.supervision.interrupted) : config.enabled ? t(($) => $.supervision.active) : t(($) => $.supervision.paused)}
            {summary.running > 0 && (
              <span className="inline-flex items-center gap-1.5 text-caption font-normal text-info">
                <Spinner className="size-3" />
                {t(($) => $.supervision.checking_now, { count: summary.running })}
              </span>
            )}
          </div>
          {config.concurrency != null && <p className="text-caption text-muted-foreground">{t(($) => $.supervision.concurrency_summary, { running: summary.running, capacity: config.concurrency, pending: summary.pending })}</p>}
          <p className="text-caption text-muted-foreground">
            {t(($) => $.supervision.interval_summary, { minutes: Math.round(config.interval_seconds / 60) })}
            {" · "}
            {t(($) => $.supervision.last_success)} {moment(config.last_success_at)}
          </p>
          {config.enabled && <p role="status" className="text-caption text-muted-foreground">{summary.running > 0 ? t(($) => $.supervision.health_running) : summary.pending > 0 ? (summary.failed >= summary.pending ? t(($) => $.supervision.health_blocked) : t(($) => $.supervision.health_waiting)) : t(($) => $.supervision.health_idle)}</p>}
          {config.enabled && summary.failed > 0 && <div role="alert" className="space-y-2 text-caption text-destructive">
            <p>{t(($) => $.supervision.failure_limit_hint, {count:summary.failed})}</p>
            {data.can_manage && <Button variant="outline" disabled={check.isPending} onClick={() => check.mutate({ retry_failed: true })}>{t(($) => $.supervision.resume_failed)}</Button>}
          </div>}
          {paused && (
            <p id="supervision-paused-hint" className="text-caption text-muted-foreground">{t(($) => $.supervision.paused_hint)}</p>
          )}
        </div>
        {data.can_manage && (
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              disabled={!config.enabled || check.isPending}
              aria-describedby={paused ? "supervision-paused-hint" : undefined}
              onClick={() => check.mutate({})}
            >
              {check.isPending && <Spinner className="size-4" />}
              {t(($) => $.supervision.check_new)}
            </Button>
            {summary.running > 0 && (
              <Button variant="outline" disabled={check.isPending} onClick={() => check.mutate({ stop: true })}>
                {t(($) => $.supervision.stop_check)}
              </Button>
            )}
            <Button variant="outline" onClick={() => setSettings(true)}>
              <Settings2 className="size-4" />
              {t(($) => $.supervision.settings)}
            </Button>
          </div>
        )}
      </div>

      {config.error && (
        <div role="alert" className="mb-4 rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-body">
          <AlertTriangle className="mr-2 inline size-4" />
          {config.error}
        </div>
      )}

      {!config.agent_id ? (
        <div className="rounded-lg border p-8 text-center">
          <h2 className="text-lg font-semibold">{t(($) => $.supervision.welcome)}</h2>
          <p className="mx-auto mt-2 max-w-lg text-body text-muted-foreground">{t(($) => $.supervision.welcome_description)}</p>
          {data.can_manage && <Button className="mt-5" onClick={() => setSettings(true)}>{t(($) => $.supervision.configure)}</Button>}
        </div>
      ) : (
        <>
          <section aria-label={t(($) => $.supervision.coverage)} className="mb-5 rounded-lg border p-4">
            <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
              <span className="inline-flex items-baseline gap-1.5 text-body">
                <strong className={cn("font-medium tabular-nums", summary.open_findings > 0 ? "text-warning" : "text-foreground")}>
                  {t(($) => $.supervision.summary_problems, { count: summary.open_findings })}
                </strong>
                <span className="text-caption text-muted-foreground">{t(($) => $.supervision.summary_finding_tasks, { tasks: summary.issues_with_findings })}</span>
              </span>
              <span className="inline-flex items-baseline gap-1.5 text-body">
                <strong className="font-medium tabular-nums text-foreground">{t(($) => $.supervision.summary_incomplete, { count: summary.incomplete })}</strong>
              </span>
              <span className="inline-flex items-baseline gap-1.5 text-body">
                <strong className={cn("font-medium tabular-nums", summary.failed > 0 ? "text-destructive" : "text-foreground")}>
                  {t(($) => $.supervision.summary_failed, { count: summary.failed })}
                </strong>
              </span>
              <span aria-hidden className="hidden h-3 w-px bg-border sm:inline-block" />
              <span className="flex flex-wrap items-center gap-x-3 gap-y-1 text-caption text-muted-foreground">
                <span>{t(($) => $.supervision.total, { count: summary.total })}</span>
                <span>{t(($) => $.supervision.covered, { count: summary.covered })}</span>
                <span>{t(($) => $.supervision.pending, { count: summary.pending })}</span>
                <span>{t(($) => $.supervision.running, { count: summary.running })}</span>
                <span>{t(($) => $.supervision.no_runs, { count: summary.no_runs })}</span>
              </span>
            </div>
            <div
              className="my-3 h-1.5 overflow-hidden rounded-full bg-muted"
              role="progressbar"
              aria-valuenow={summary.covered}
              aria-valuemax={Math.max(1, summary.total)}
              aria-valuemin={0}
              aria-label={t(($) => $.supervision.coverage)}
            >
              <div
                className="h-full bg-info transition-[width] motion-reduce:transition-none"
                style={{ width: (summary.total ? Math.min(100, (summary.covered / summary.total) * 100) : 0) + "%" }}
              />
            </div>
            <p className="text-caption text-muted-foreground">
              {config.inventory_complete ? t(($) => $.supervision.coverage_hint) : t(($) => $.supervision.registering)}
            </p>
            {summary.oldest_pending_at && (
              <p className="mt-1 text-caption text-muted-foreground">{t(($) => $.supervision.oldest_pending)} {moment(summary.oldest_pending_at)}</p>
            )}
          </section>

          <div className="mb-4 flex flex-wrap items-center gap-2">
            <div className="relative min-w-48 flex-1 sm:max-w-sm">
              <Search className="pointer-events-none absolute left-3 top-3 size-4 text-muted-foreground" />
              <Input
                aria-label={t(($) => $.supervision.search)}
                placeholder={t(($) => $.supervision.search)}
                className="pl-9"
                value={search}
                onChange={(e) => route({ search: e.target.value, offset: null })}
              />
            </div>
            <select
              aria-label={t(($) => $.supervision.project)}
              className="h-10 max-w-full rounded-md border bg-background px-3 text-body"
              value={project}
              onChange={(e) => route({ project: e.target.value, offset: null })}
            >
              <option value="">{t(($) => $.supervision.all_projects)}</option>
              {projects.data?.projects.map((item) => <option key={item.id} value={item.id}>{item.title}</option>)}
            </select>
            <select
              aria-label={t(($) => $.supervision.filter)}
              className="h-10 rounded-md border bg-background px-3 text-body"
              value={filter}
              onChange={(e) => route({ filter: e.target.value, offset: null })}
            >
              <option value="all">{t(($) => $.supervision.all)}</option>
              <option value="problems">{t(($) => $.supervision.problems)}</option>
              <option value="incomplete">{t(($) => $.supervision.incomplete)}</option>
              <option value="pending">{t(($) => $.supervision.pending_filter)}</option>
              <option value="failed">{t(($) => $.supervision.filter_failed)}</option>
            </select>
          </div>

          {list.isError ? (
            <div role="alert" className="p-4">
              {errorText(list.error)}
              <Button variant="outline" onClick={() => void list.refetch()}>{t(($) => $.supervision.retry)}</Button>
            </div>
          ) : (
            <div className="overflow-hidden rounded-lg border">
              <div className="hidden grid-cols-[minmax(0,1fr)_170px_150px_120px] gap-4 border-b bg-muted/30 px-4 py-3 text-caption text-muted-foreground md:grid">
                <span>{t(($) => $.supervision.task)}</span>
                <span>{t(($) => $.supervision.progress)}</span>
                <span>{t(($) => $.supervision.conclusion)}</span>
                <span>{t(($) => $.supervision.last_check)}</span>
              </div>
              {list.isPending ? (
                <div className="p-6" role="status">{t(($) => $.supervision.loading)}</div>
              ) : list.data?.items.length === 0 ? (
                <div className="p-8 text-center">
                  <p className="text-body text-muted-foreground">{filtersActive ? t(($) => $.supervision.empty) : t(($) => $.supervision.empty_all)}</p>
                  {filtersActive && (
                    <Button variant="ghost" className="mt-2" onClick={() => route({ search: null, filter: null, project: null, offset: null })}>
                      {t(($) => $.supervision.clear_filters)}
                    </Button>
                  )}
                </div>
              ) : (
                list.data?.items.map((item) => {
                  const state = reviewState(item);
                  const conclusion = item.open_findings > 0
                    ? t(($) => $.supervision.problem_count, { count: item.open_findings })
                    : item.gaps.length > 0
                      ? t(($) => $.supervision.incomplete)
                      : item.last_checked_at
                        ? t(($) => $.supervision.no_findings)
                        : t(($) => $.supervision.no_conclusion);
                  return (
                    <button
                      key={item.issue_id}
                      type="button"
                      onClick={() => route({ selected: item.issue_id })}
                      className="grid w-full gap-x-3 gap-y-1 border-b px-4 py-3 text-left text-body last:border-b-0 hover:bg-accent/50 focus-visible:outline-2 focus-visible:outline-ring md:grid-cols-[minmax(0,1fr)_170px_150px_120px] md:items-center md:gap-4"
                    >
                      <span className="min-w-0">
                        <span className="block truncate font-medium">{item.identifier} {item.title}</span>
                        <span className="hidden truncate text-caption text-muted-foreground md:block">{statusLabel(item.business_status)}</span>
                        <span className="mt-1 flex flex-wrap items-center gap-x-2 text-caption md:hidden">
                          <span className="text-muted-foreground">{statusLabel(item.business_status)}</span>
                          <span aria-hidden className="text-muted-foreground">·</span>
                          <span className={cn(item.open_findings > 0 ? "font-medium text-warning" : "text-muted-foreground")}>{conclusion}</span>
                          <span aria-hidden className="text-muted-foreground">·</span>
                          <span className="tabular-nums text-muted-foreground">{moment(item.last_checked_at)}</span>
                        </span>
                      </span>
                      <span className="justify-self-end md:justify-self-start">
                        <Badge variant="outline" className={stateTones[state]}>
                          {state === "running" && <Spinner className="size-3" />}
                          {stateLabels[state]}
                        </Badge>
                      </span>
                      <span
                        className={cn(
                          "hidden text-caption md:block",
                          item.open_findings > 0 ? "font-medium text-warning" : "text-muted-foreground",
                        )}
                      >
                        {conclusion}
                      </span>
                      <span className="hidden text-caption tabular-nums text-muted-foreground md:block">
                        {moment(item.last_checked_at)}
                      </span>
                    </button>
                  );
                })
              )}
            </div>
          )}

          <div className="mt-3 flex flex-wrap items-center justify-between gap-2">
            <span className="text-caption text-muted-foreground">
              {list.data && list.data.items.length > 0
                ? t(($) => $.supervision.page_range, { from: offset + 1, to: offset + list.data.items.length, total: summary.total })
                : null}
            </span>
            <span className="flex gap-2">
              <Button variant="outline" disabled={offset === 0} onClick={() => route({ offset: String(Math.max(0, offset - 50)) })}>
                {t(($) => $.supervision.previous)}
              </Button>
              <Button variant="outline" disabled={!list.data?.has_more} onClick={() => route({ offset: String(offset + 50) })}>
                {t(($) => $.supervision.next)}
              </Button>
            </span>
          </div>
        </>
      )}

      {/* 只在打开时挂载：每次打开都从服务端配置重新初始化运行时、型号和提示词。 */}
      {settings && <SupervisionSettings open close={() => setSettings(false)} config={config} />}
      <SupervisionDetail issueId={selected} canManage={data.can_manage} enabled={config.enabled} close={() => route({ selected: null })} />
    </div>
  );
}

