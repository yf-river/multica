"use client";

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { AlertTriangle, ChevronRight, Download, HelpCircle, Loader2, RotateCcw, WifiOff } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import * as XLSX from "xlsx";
import { api } from "@multica/core/api";
import { issueExecutionTreeOptions, issueKeys, issueListOptions } from "@multica/core/issues/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type {
  AgentTask,
  AgentTaskArtifact,
  Issue,
  IssueExecutionTreeResponse,
  IssueTimelineNode,
  IssueTimelineSummary,
} from "@multica/core/types";
import type { TaskMessagePayload } from "@multica/core/types/events";
import { useWSEvent, useWSReconnect } from "@multica/core/realtime";
import { cn } from "@multica/ui/lib/utils";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { Dialog, DialogContent, DialogTitle } from "@multica/ui/components/ui/dialog";
import { PageHeader } from "../../layout/page-header";
import { AppLink, useNavigation } from "../../navigation";
import { TranscriptButton } from "../../common/task-transcript";
import { createIssueReviewDraftCase } from "./run-review-draft-case";
import {
  buildEventTaskLabelById,
  buildRunReviewEventGroups,
  buildRunReviewEventRows,
  filterRunReviewEventRows,
  filterVisibleRunReviewEventRows,
  flattenExecutionTasks,
  type RunReviewEventGroupData,
  type RunReviewEventRowData,
} from "./run-review-events";
import {
  artifactDownloadHref,
  buildRunReviewNodeXlsxSheets,
  buildRunReviewRawEventsXlsxSheets,
  buildXlsxWorkbook,
  type XlsxSheetSpec,
} from "./run-review-export";
import {
  cacheReuseRate,
  formatDateTime,
  formatDuration,
  formatJSON,
  formatNumber,
  formatPercent,
  shortId,
  statusLabel,
} from "./run-review-format";
import {
  buildRunReviewDurationSummary,
  buildRunReviewLiveSummary,
  buildRunReviewLiveTimelineNodes,
  hasActiveTimelineNode,
  isActiveTask,
  isRetryableTask,
  latestTerminalAgentTask,
  runReviewMessageRefreshDelayMs,
  runReviewTotalDurationMs,
  shouldRefreshRunReviewForTaskEvent,
  useRunReviewLiveNow,
  type RunReviewTaskEventPayload,
} from "./run-review-live";
import {
  agentNodeDisplayLabel,
  buildAgentNodeRows,
  buildChildLanes,
  buildStageRows,
  buildTimelineAgentRows,
  buildTimelineBarRows,
  dedupeArtifacts,
  formatTimeTick,
  nodeTokenTotal,
  timelineSegmentStyle,
  timelineSegmentTooltipRows,
  type ChildLane,
  type TimelineBarRow,
  type TimelineBarSegment,
  type TimelineNodeRow,
} from "./run-review-timeline";

export function buildRunReviewOptimizerHref(evaluationView: (view: string) => string, issueId: string): string {
  return `${evaluationView("runs")}?issue=${encodeURIComponent(issueId)}`;
}

function buildRunReviewTokenSummary(summary: IssueTimelineSummary | undefined): string {
  const cacheRead = summary?.total_cache_read_tokens ?? 0;
  const cacheWrite = summary?.total_cache_write_tokens ?? 0;
  return [
    `输入 ${formatTokenMillions(summary?.total_input_tokens ?? 0)}`,
    `输出 ${formatTokenMillions(summary?.total_output_tokens ?? 0)}`,
    `读 ${formatTokenMillions(cacheRead)}`,
    `写 ${formatTokenMillions(cacheWrite)}`,
    `命中 ${formatPercent(cacheReuseRate(cacheRead, cacheWrite))}`,
  ].join(" · ");
}

function buildRunReviewTurnSummary(agentRows: ReturnType<typeof buildAgentNodeRows>): string {
  const summary = agentRows
    .map((row) => `${agentNodeDisplayLabel(row)} ${formatNumber(row.node.agent_turn_count ?? 0)}`)
    .join(" · ");
  return summary || "暂无执行轮次";
}

export function RunReviewsPage() {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const selectedIssueId = navigation.searchParams.get("issue");
  const issuesQuery = useQuery(issueListOptions(wsId, { sort_by: "created_at", sort_direction: "desc" }));
  const issues = useMemo(() => issuesQuery.data ?? [], [issuesQuery.data]);
  const selectedIssue = useMemo(
    () => issues.find((issue) => issue.id === selectedIssueId) ?? issues[0] ?? null,
    [issues, selectedIssueId],
  );
  const treeQuery = useQuery({
    ...issueExecutionTreeOptions(selectedIssue?.id ?? ""),
    enabled: Boolean(selectedIssue?.id),
  });

  return (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <PageHeader>
        <div className="min-w-0">
          <h1 className="truncate text-sm font-semibold">运行复盘</h1>
        </div>
      </PageHeader>

      <div className="grid min-h-0 flex-1 grid-cols-1 overflow-hidden lg:grid-cols-[360px_minmax(0,1fr)]">
        <aside className="min-h-0 border-b lg:border-r lg:border-b-0">
          <div className="flex h-10 items-center justify-between border-b px-3">
            <div className="text-xs font-medium text-muted-foreground">Issue 运行记录</div>
            <div className="text-xs text-muted-foreground">{issues.length} 条</div>
          </div>
          <div className="max-h-[38vh] min-h-0 overflow-y-auto lg:max-h-none lg:h-[calc(100vh-7.5rem)]">
            {issuesQuery.isLoading ? (
              <IssueListSkeleton />
            ) : issues.length === 0 ? (
              <div className="px-3 py-6 text-sm text-muted-foreground">暂无 issue。请先通过公开 UI/API 创建任务。</div>
            ) : (
              <div className="divide-y">
                {issues.map((issue) => (
                  <IssueRunRow
                    key={issue.id}
                    issue={issue}
                    active={issue.id === selectedIssue?.id}
                    href={`${paths.runReviews()}?issue=${encodeURIComponent(issue.id)}`}
                  />
                ))}
              </div>
            )}
          </div>
        </aside>

        <main className="min-h-0 overflow-y-auto">
          {selectedIssue ? (
            <RunReviewDetail
              issue={selectedIssue}
              tree={treeQuery.data}
              loading={treeQuery.isLoading}
              issueHref={paths.issueDetail(selectedIssue.id)}
              evalDraftHref={`${paths.evaluationView("datasets")}?issue=${encodeURIComponent(selectedIssue.id)}&mode=draft`}
              optimizerHref={buildRunReviewOptimizerHref(paths.evaluationView, selectedIssue.id)}
            />
          ) : (
            <div className="px-6 py-10 text-sm text-muted-foreground">选择一条 issue 查看完整链路。</div>
          )}
        </main>
      </div>
    </div>
  );
}

function IssueRunRow({ issue, active, href }: { issue: Issue; active: boolean; href: string }) {
  const activityLabel = issueRunRowActivityLabel(issue);
  return (
    <AppLink
      href={href}
      className={cn(
        "block px-3 py-2.5 text-left text-sm hover:bg-accent/60",
        active && "bg-accent text-accent-foreground",
      )}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="truncate font-medium">{issue.identifier ? `${issue.identifier} ` : ""}{issue.title}</div>
          <div className="mt-1 flex flex-wrap gap-1 text-[11px] text-muted-foreground">
            {issueRunRowMetaLabels(issue).map((label) => (
              <span key={label}>{label}</span>
            ))}
          </div>
        </div>
        {activityLabel && (
          <span className={cn("shrink-0 rounded border px-1.5 py-0.5 text-[11px]", activityLabel.tone === "running" ? "border-info/40 text-info" : "text-muted-foreground")}>
            {activityLabel.label}
          </span>
        )}
      </div>
    </AppLink>
  );
}

export function issueRunRowMetaLabels(issue: Pick<Issue, "project" | "status" | "child_progress">): string[] {
  const childTotal = issue.child_progress?.total ?? 0;
  return [
    issue.project?.title ?? "未绑定项目",
    `状态 ${statusLabel(issue.status)}`,
    ...(childTotal > 0 ? [`子任务 ${issue.child_progress?.done ?? 0}/${childTotal}`] : []),
  ];
}

export function issueRunRowActivityLabel(
  issue: Pick<Issue, "agent_activity">,
): { label: string; tone: "running" | "queued" } | null {
  const running = issue.agent_activity?.running_count ?? 0;
  const queued = issue.agent_activity?.queued_count ?? 0;
  if (running > 0) return { label: `运行 ${running}`, tone: "running" };
  if (queued > 0) return { label: `排队 ${queued}`, tone: "queued" };
  return null;
}

function useRunReviewRealtimeSync(issueId: string, wsId: string) {
  const queryClient = useQueryClient();
  const messageTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const lastMessageRefreshAtRef = useRef(0);

  const clearMessageTimer = useCallback(() => {
    if (messageTimerRef.current) {
      clearTimeout(messageTimerRef.current);
      messageTimerRef.current = null;
    }
  }, []);

  const refreshSelectedIssue = useCallback((includeTasks: boolean) => {
    if (!issueId) return;
    clearMessageTimer();
    lastMessageRefreshAtRef.current = Date.now();
    queryClient.invalidateQueries({ queryKey: issueKeys.executionTree(issueId) });
    if (includeTasks) {
      queryClient.invalidateQueries({ queryKey: issueKeys.tasks(issueId) });
      queryClient.invalidateQueries({ queryKey: issueKeys.list(wsId) });
    }
  }, [clearMessageTimer, issueId, queryClient, wsId]);

  const handleLifecycleEvent = useCallback((payload: unknown) => {
    if (!shouldRefreshRunReviewForTaskEvent(issueId, payload as RunReviewTaskEventPayload)) return;
    refreshSelectedIssue(true);
  }, [issueId, refreshSelectedIssue]);

  const handleTaskMessage = useCallback((payload: unknown) => {
    if (!shouldRefreshRunReviewForTaskEvent(issueId, payload as TaskMessagePayload)) return;
    const nowMs = Date.now();
    const delayMs = runReviewMessageRefreshDelayMs(nowMs, lastMessageRefreshAtRef.current);
    clearMessageTimer();
    messageTimerRef.current = setTimeout(() => {
      messageTimerRef.current = null;
      lastMessageRefreshAtRef.current = Date.now();
      queryClient.invalidateQueries({ queryKey: issueKeys.executionTree(issueId) });
    }, delayMs);
  }, [clearMessageTimer, issueId, queryClient]);

  useEffect(() => {
    clearMessageTimer();
    lastMessageRefreshAtRef.current = 0;
    return clearMessageTimer;
  }, [clearMessageTimer, issueId]);
  useWSReconnect(() => refreshSelectedIssue(true));
  useWSEvent("task:queued", handleLifecycleEvent);
  useWSEvent("task:dispatch", handleLifecycleEvent);
  useWSEvent("task:running", handleLifecycleEvent);
  useWSEvent("task:waiting_local_directory", handleLifecycleEvent);
  useWSEvent("task:completed", handleLifecycleEvent);
  useWSEvent("task:failed", handleLifecycleEvent);
  useWSEvent("task:cancelled", handleLifecycleEvent);
  useWSEvent("task:message", handleTaskMessage);
}

function RunReviewDetail({
  issue,
  tree,
  loading,
  issueHref,
  evalDraftHref,
  optimizerHref,
}: {
  issue: Issue;
  tree: IssueExecutionTreeResponse | undefined;
  loading: boolean;
  issueHref: string;
  evalDraftHref: string;
  optimizerHref: string;
}) {
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const [eventQuery, setEventQuery] = useState("");
  const [collapsedEventGroupKeys, setCollapsedEventGroupKeys] = useState<Set<string>>(() => new Set());
  const [selectedRawEvent, setSelectedRawEvent] = useState<RunReviewEventRowData | null>(null);
  useRunReviewRealtimeSync(issue.id, wsId);
  const { data: tasks = [] } = useQuery({
    queryKey: issueKeys.tasks(issue.id),
    queryFn: () => api.listTasksByIssue(issue.id),
    staleTime: 30_000,
    refetchOnWindowFocus: true,
  });
  const activeTasks = useMemo(() => tasks.filter(isActiveTask), [tasks]);
  const baseTimelineNodes = useMemo(
    () => tree?.timeline_nodes ?? [],
    [tree?.timeline_nodes],
  );
  const liveNowMs = useRunReviewLiveNow(activeTasks.length > 0 || hasActiveTimelineNode(baseTimelineNodes));
  const timelineNodes = useMemo(
    () => buildRunReviewLiveTimelineNodes(baseTimelineNodes, liveNowMs),
    [baseTimelineNodes, liveNowMs],
  );
  const summary = useMemo(
    () => buildRunReviewLiveSummary(tree?.issue_summary, activeTasks, baseTimelineNodes, liveNowMs),
    [activeTasks, baseTimelineNodes, liveNowMs, tree?.issue_summary],
  );
  const wallClockDurationMs = runReviewTotalDurationMs(summary);
  const stageRows = buildStageRows(timelineNodes);
  const childLanes = buildChildLanes(tree);
  const agentNodeRows = buildAgentNodeRows(timelineNodes);
  const visibleTimelineRows = buildTimelineAgentRows(timelineNodes);
  const visibleChildLanes = childLanes;
  const eventRows = buildRunReviewEventRows(tree, timelineNodes);
  const readableEventRows = useMemo(
    () => filterVisibleRunReviewEventRows(eventRows),
    [eventRows],
  );
  const visibleEventRows = useMemo(
    () => filterRunReviewEventRows(readableEventRows, eventQuery),
    [eventQuery, readableEventRows],
  );
  const taskLabelById = useMemo(() => buildEventTaskLabelById(timelineNodes), [timelineNodes]);
  const visibleEventGroups = useMemo(
    () => buildRunReviewEventGroups(visibleEventRows, taskLabelById),
    [taskLabelById, visibleEventRows],
  );
  const eventSearchActive = eventQuery.trim().length > 0;
  const tokenTotal = (summary?.total_input_tokens ?? 0) +
    (summary?.total_output_tokens ?? 0) +
    (summary?.total_cache_read_tokens ?? 0) +
    (summary?.total_cache_write_tokens ?? 0);
  const nodeXlsxSheets = buildRunReviewNodeXlsxSheets(issue, summary, agentNodeRows, visibleChildLanes);
  const rawEventsXlsxSheets = buildRunReviewRawEventsXlsxSheets(eventRows);
  const taskById = useMemo(() => {
    const result = new Map<string, AgentTask>();
    for (const task of [...flattenExecutionTasks(tree), ...tasks]) {
      result.set(task.id, task);
    }
    return result;
  }, [tasks, tree]);
  const toggleEventGroup = useCallback((groupKey: string) => {
    setCollapsedEventGroupKeys((current) => {
      const next = new Set(current);
      if (next.has(groupKey)) {
        next.delete(groupKey);
      } else {
        next.add(groupKey);
      }
      return next;
    });
  }, []);
  const latestTerminalTask = useMemo(() => latestTerminalAgentTask(tasks), [tasks]);
  const latestFailedTask = activeTasks.length === 0 && latestTerminalTask && isRetryableTask(latestTerminalTask)
    ? latestTerminalTask
    : null;
  const taskStatusLabel = activeTasks.length > 0
    ? `运行中 ${activeTasks.length}`
    : latestFailedTask
      ? "任务失败，待重试"
      : latestTerminalTask
        ? statusLabel(latestTerminalTask.status)
        : "无运行任务";
  const createDraftMut = useMutation({
    mutationFn: () => createIssueReviewDraftCase(issue, tree, stageRows, childLanes),
    onSuccess: (created) => {
      queryClient.invalidateQueries({ queryKey: ["prompt-library"] });
      toast.success(`评测用例已生成：${created.case_name || created.id}`);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "生成评测用例失败");
    },
  });
  const retryTaskMut = useMutation({
    mutationFn: (taskId: string) => api.rerunIssue(issue.id, taskId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: issueKeys.tasks(issue.id) });
      queryClient.invalidateQueries({ queryKey: issueKeys.executionTree(issue.id) });
      queryClient.invalidateQueries({ queryKey: issueKeys.list(wsId) });
      queryClient.invalidateQueries({ queryKey: issueKeys.detail(wsId, issue.id) });
      toast.success("已重新入队最新失败任务");
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "重试失败任务失败");
    },
  });
  const createdDraft = createDraftMut.data;
  const createdDraftHref = createdDraft
    ? `${evalDraftHref}&case=${encodeURIComponent(createdDraft.id)}&status=draft`
    : evalDraftHref;

  return (
    <div className="space-y-4 px-4 py-4">
      <section className="rounded-md border bg-card">
        <div className="flex flex-col gap-3 border-b px-4 py-3 lg:flex-row lg:items-start lg:justify-between">
          <div className="min-w-0">
            <div className="text-xs text-muted-foreground">{issue.identifier}</div>
            <h2 className="mt-0.5 truncate text-base font-semibold">{issue.title}</h2>
            <div className="mt-1 flex flex-wrap gap-2 text-xs text-muted-foreground">
              <span>项目：{issue.project?.title ?? "未绑定"}</span>
              <span>状态：{statusLabel(issue.status)}</span>
              <span>任务：{taskStatusLabel}</span>
              <span>验收：{summary?.acceptance_status ?? "待运行"}</span>
            </div>
          </div>
          <div className="flex flex-wrap gap-2">
            <AppLink className="rounded border px-2.5 py-1.5 text-xs hover:bg-accent" href={issueHref}>返回 issue</AppLink>
            <button
              type="button"
              className="rounded border border-info/40 bg-info/10 px-2.5 py-1.5 text-xs text-info hover:bg-info/15 disabled:cursor-not-allowed disabled:opacity-60"
              onClick={() => createDraftMut.mutate()}
              disabled={createDraftMut.isPending || !tree}
              data-testid="run-review-create-eval-draft"
            >
              {createDraftMut.isPending ? "生成中..." : "生成评测用例"}
            </button>
            <AppLink className="rounded border px-2.5 py-1.5 text-xs hover:bg-accent" href={optimizerHref}>进入评测资产</AppLink>
          </div>
        </div>

        {createdDraft && (
          <div className="border-b bg-info/5 px-4 py-2 text-xs text-muted-foreground" data-testid="run-review-created-eval-draft">
            已生成 draft case {createdDraft.id}。请进入训练与评估确认输入、期望行为和验证方式，再批准为 active。
            <AppLink className="ml-2 text-info underline-offset-2 hover:underline" href={createdDraftHref}>
              查看 Draft
            </AppLink>
          </div>
        )}

        {latestFailedTask && (
          <RunFailureBanner
            task={latestFailedTask}
            retrying={retryTaskMut.isPending}
            onRetry={() => retryTaskMut.mutate(latestFailedTask.id)}
          />
        )}

        <div className="grid gap-3 border-t p-4 text-sm md:grid-cols-3">
          <Metric
            label="总耗时"
            value={formatDuration(wallClockDurationMs)}
            detail={buildRunReviewDurationSummary(summary)}
          />
          <Metric
            label="总 Token"
            value={formatNumber(tokenTotal)}
            detail={buildRunReviewTokenSummary(summary)}
          />
          <Metric
            label="执行轮次"
            value={formatNumber(summary?.agent_turn_count ?? 0)}
            detail={buildRunReviewTurnSummary(agentNodeRows)}
          />
        </div>
      </section>

      {loading ? <DetailSkeleton /> : null}

      <section className="rounded-md border bg-card">
        <SectionTitle title="横向时序图" subtitle="按真实出现的执行节点绘制；节点存在但缺少开始/结束时间时会单独标记。" />
        <TimelineLaneChart stageRows={visibleTimelineRows} childLanes={visibleChildLanes} timelineNodes={timelineNodes} />
      </section>

      <section className="rounded-md border bg-card">
        <SectionTitle
          title="节点表"
          subtitle="按 Agent 运行节点展示耗时、token、执行轮次和产物。"
          action={
            <ExportButton
              label="导出节点数据"
              onClick={() => downloadXlsx(`run-review-nodes-${issue.identifier || issue.id}.xlsx`, nodeXlsxSheets)}
            />
          }
        />
        <div className="hidden md:block">
          <table className="w-full table-fixed text-sm">
            <thead className="border-y bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="w-[20%] px-3 py-2 font-medium">节点</th>
                <th className="w-[18%] px-3 py-2 font-medium">Agent</th>
                <th className="w-[12%] px-3 py-2 font-medium">耗时</th>
                <th className="w-[12%] px-3 py-2 font-medium">Token</th>
                <th className="w-[12%] px-3 py-2 font-medium">执行轮次</th>
                <th className="w-[26%] px-3 py-2 font-medium">产物</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {agentNodeRows.length > 0 ? agentNodeRows.map((row) => (
                <tr key={row.key}>
                  <td className="truncate px-3 py-2">{agentNodeDisplayLabel(row)}</td>
                  <td className="truncate px-3 py-2 text-muted-foreground">{row.node.agent_name ?? row.key}</td>
                  <td className="truncate px-3 py-2">
                    <NodeMetric value={formatDuration(row.node.duration_ms ?? 0)} tooltip={nodeDurationTooltip(row.node)} />
                  </td>
                  <td className="truncate px-3 py-2">
                    <NodeMetric value={formatNumber(nodeTokenTotal(row.node))} tooltip={nodeTokenTooltip(row.node)} />
                  </td>
                  <td className="truncate px-3 py-2">{formatNumber(row.node.agent_turn_count ?? 0)}</td>
                  <td className="px-3 py-2"><ArtifactLinks artifacts={row.node.artifacts ?? []} /></td>
                </tr>
              )) : (
                <tr>
                  <td className="px-3 py-5 text-sm text-muted-foreground" colSpan={6}>暂无真实 Agent 节点。</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        <div className="divide-y md:hidden">
          {agentNodeRows.length > 0 ? agentNodeRows.map((row) => (
            <div key={row.key} className="px-4 py-3 text-sm">
              <div className="min-w-0 truncate font-medium">{agentNodeDisplayLabel(row)}</div>
              <div className="mt-2 grid grid-cols-2 gap-x-3 gap-y-1 text-xs text-muted-foreground">
                <NodeFact label="Agent" value={row.node.agent_name ?? row.key} />
                <NodeFact label="耗时" value={formatDuration(row.node.duration_ms ?? 0)} />
                <NodeFact label="Token" value={formatNumber(nodeTokenTotal(row.node))} />
                <NodeFact label="执行轮次" value={formatNumber(row.node.agent_turn_count ?? 0)} />
              </div>
              <div className="mt-2 text-xs">
                <ArtifactLinks artifacts={row.node.artifacts ?? []} />
              </div>
            </div>
          )) : (
            <div className="px-4 py-5 text-sm text-muted-foreground">暂无真实 Agent 节点。</div>
          )}
        </div>
      </section>

      <section className="rounded-md border bg-card">
        <SectionTitle
          title="事件流"
          subtitle="展示可读动作；完整记录见 RAW 导出。"
          action={
            <ExportButton
              label="导出 RAW 交互信息"
              onClick={() => downloadXlsx(`run-review-events-${issue.identifier || issue.id}.xlsx`, rawEventsXlsxSheets)}
            />
          }
        />
        <div className="border-y p-3">
          <input
            className="w-full rounded-md border bg-background px-3 py-2 text-sm outline-none focus:border-ring"
            value={eventQuery}
            onChange={(event) => setEventQuery(event.target.value)}
            placeholder="搜索事件、Agent、工具、结果或 task"
          />
        </div>
        <div className="min-h-[24rem] space-y-3 p-3">
          {visibleEventGroups.length > 0 ? visibleEventGroups.map((group, index) => (
            <RunReviewEventGroup
              key={group.key}
              group={group}
              colorClassName={eventGroupAccentClass(group, index)}
              task={group.taskId ? taskById.get(group.taskId) : undefined}
              open={eventSearchActive || !collapsedEventGroupKeys.has(group.key)}
              onToggle={() => toggleEventGroup(group.key)}
              onOpenRaw={setSelectedRawEvent}
            />
          )) : (
            <div className="flex gap-2 rounded-md border border-dashed bg-muted/20 px-3 py-6 text-sm text-muted-foreground">
              <AlertTriangle className="size-4" />
              {eventSearchActive
                ? "没有匹配的事件。"
                : eventRows.length > 0
                  ? "暂无可读动作，完整记录可通过 RAW 导出查看。"
                  : "暂无事件。真实任务开始后会回写 trace、用量和证据。"}
            </div>
          )}
        </div>
      </section>
      <RunReviewEventRawDialog event={selectedRawEvent} onOpenChange={(open) => { if (!open) setSelectedRawEvent(null); }} />
    </div>
  );
}
function RunReviewEventGroup({
  group,
  colorClassName,
  task,
  open,
  onToggle,
  onOpenRaw,
}: {
  group: RunReviewEventGroupData;
  colorClassName: string;
  task: AgentTask | undefined;
  open: boolean;
  onToggle: () => void;
  onOpenRaw: (event: RunReviewEventRowData) => void;
}) {
  return (
    <div className="overflow-hidden rounded-md border bg-muted/15 shadow-sm" data-testid="run-review-event-group">
      <div className="flex items-start gap-3 border-b bg-muted/45 px-3 py-3">
        <button
          type="button"
          className="mt-0.5 rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground"
          onClick={onToggle}
          aria-label={open ? "收起事件组" : "展开事件组"}
        >
          <ChevronRight className={cn("size-3.5 transition-transform", open && "rotate-90")} />
        </button>
        <div className={cn("mt-1 h-10 w-2 shrink-0 rounded-full", colorClassName)} />
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <span className="min-w-[12rem] flex-1 truncate font-medium">{group.label}</span>
            <span className={cn("shrink-0 rounded border px-1.5 py-0.5 text-[11px]", eventGroupOutcomeClass(group))}>{group.outcome}</span>
          </div>
          <div className="mt-2 flex flex-wrap gap-1.5 text-[11px] text-muted-foreground">
            <span className="rounded border px-1.5 py-0.5">{formatNumber(group.events.length)} 个事件</span>
            {group.timeRangeLabel && <span className="rounded border px-1.5 py-0.5">{group.timeRangeLabel}</span>}
            {group.tokenTotal > 0 && <span className="rounded border px-1.5 py-0.5">Token {formatNumber(group.tokenTotal)}</span>}
            {group.taskId && <span className="rounded border px-1.5 py-0.5 font-mono">task {shortId(group.taskId)}</span>}
          </div>
        </div>
        {task && (
          <div className="shrink-0">
            <TranscriptButton task={task} agentName="" title="完整 transcript" />
          </div>
        )}
      </div>
      {open && (
        <div className="space-y-2 border-l-4 border-muted-foreground/10 bg-background/60 p-3 pl-5">
          {group.events.map((event, index) => (
            <RunReviewEventRow
              key={event.id}
              event={event}
              colorClassName={eventCardAccentClass(event, index)}
              onOpenRaw={() => onOpenRaw(event)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function RunReviewEventRow({
  event,
  colorClassName,
  onOpenRaw,
}: {
  event: RunReviewEventRowData;
  colorClassName: string;
  onOpenRaw: () => void;
}) {
  const tone = eventToneClasses(event.severity);
  return (
    <article
      className="cursor-pointer rounded-md border bg-background p-3 text-sm shadow-sm transition-colors hover:bg-accent/25"
      data-testid={`run-review-event-${event.kind}`}
      data-event-id={event.id}
      role="button"
      tabIndex={0}
      aria-label={`查看事件详情：${event.title}`}
      onClick={onOpenRaw}
      onKeyDown={(keyboardEvent) => {
        if (keyboardEvent.key === "Enter" || keyboardEvent.key === " ") {
          keyboardEvent.preventDefault();
          onOpenRaw();
        }
      }}
    >
      <div className="flex gap-3">
        <div className={cn("mt-0.5 h-auto w-1.5 shrink-0 rounded-full", colorClassName)} />
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <span className={cn("shrink-0 rounded border px-1.5 py-0.5 text-[11px]", tone.chip)}>{event.category}</span>
            <span className={cn("shrink-0 rounded border px-1.5 py-0.5 text-[11px]", tone.outcome)}>{event.outcome}</span>
            <span className="min-w-[12rem] flex-1 truncate font-medium">{event.title}</span>
            {event.timeLabel && <span className="shrink-0 text-xs text-muted-foreground">{event.timeLabel}</span>}
          </div>
          {event.summary && (
            <div className={cn("mt-2 rounded border px-2 py-1.5 text-xs leading-5", tone.summary)}>
              {event.summary}
            </div>
          )}
          <div className="mt-2 flex flex-wrap gap-1.5 text-[11px] text-muted-foreground">
            {event.sourceLabel && <span className="rounded border px-1.5 py-0.5">{event.sourceLabel}</span>}
            {event.object && <span className="rounded border px-1.5 py-0.5">{event.object}</span>}
            {event.durationMs >= 1000 && <span className="rounded border px-1.5 py-0.5">耗时 {formatDuration(event.durationMs)}</span>}
            {event.tokenTotal > 0 && <span className="rounded border px-1.5 py-0.5">Token {formatNumber(event.tokenTotal)}</span>}
            {event.taskId && <span className="rounded border px-1.5 py-0.5 font-mono">task {shortId(event.taskId)}</span>}
          </div>
        </div>
      </div>
    </article>
  );
}

function RunReviewEventRawDialog({
  event,
  onOpenChange,
}: {
  event: RunReviewEventRowData | null;
  onOpenChange: (open: boolean) => void;
}) {
  return (
    <Dialog open={Boolean(event)} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[88vh] w-[calc(100vw-2rem)] max-w-6xl flex-col gap-0 p-0 sm:!max-w-6xl lg:w-[calc(100vw-4rem)]">
        <div className="border-b px-5 py-4">
          <DialogTitle className="text-base font-semibold">{event?.title ?? "事件详情"}</DialogTitle>
          <div className="mt-2 flex flex-wrap gap-1.5 text-[11px] text-muted-foreground">
            {event?.category && <span className="rounded border px-1.5 py-0.5">{event.category}</span>}
            {event?.outcome && <span className="rounded border px-1.5 py-0.5">{event.outcome}</span>}
            {event?.timeLabel && <span className="rounded border px-1.5 py-0.5">{event.timeLabel}</span>}
            {event?.taskId && <span className="rounded border px-1.5 py-0.5 font-mono">task {shortId(event.taskId)}</span>}
          </div>
        </div>
        <div className="min-h-0 flex-1 space-y-3 overflow-auto bg-muted/10 p-5 text-sm">
          {event ? (
            <>
              <EventRawBlock title="摘要" value={event.summary || "无摘要"} />
              {event.detail && <EventRawBlock title="详情" value={event.detail} />}
              {event.metadataDetail && <EventRawBlock title="Metadata" value={event.metadataDetail} />}
              <EventRawBlock title={event.rawSourceLabel || "Raw JSON"} value={event.rawPayload === undefined ? "无 raw payload" : formatJSON(event.rawPayload)} />
              {event.linkedRawPayloads?.map((item, index) => (
                <EventRawBlock key={`${item.label}:${index}`} title={item.label} value={formatJSON(item.payload)} />
              ))}
            </>
          ) : null}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function EventRawBlock({ title, value }: { title: string; value: string }) {
  return (
    <section className="rounded-md border bg-background shadow-sm">
      <div className="border-b px-3 py-2 text-xs font-medium text-muted-foreground">{title}</div>
      <pre className="max-h-[60vh] overflow-auto whitespace-pre px-3 py-2 text-xs leading-5">{value}</pre>
    </section>
  );
}

function formatTokenMillions(value: number) {
  return `${((value || 0) / 1_000_000).toFixed(2)}M`;
}

function eventToneClasses(severity: RunReviewEventRowData["severity"]) {
  switch (severity) {
    case "error":
      return {
        icon: "border-destructive/30 bg-destructive/10 text-destructive",
        chip: "border-destructive/30 bg-destructive/10 text-destructive",
        outcome: "border-destructive/30 bg-destructive/10 text-destructive",
        summary: "border-destructive/25 bg-destructive/5 text-destructive",
      };
    case "warning":
      return {
        icon: "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300",
        chip: "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300",
        outcome: "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300",
        summary: "border-amber-500/25 bg-amber-500/5 text-foreground",
      };
    default:
      return {
        icon: "border-border bg-background text-muted-foreground",
        chip: "border-border bg-muted/30 text-muted-foreground",
        outcome: "border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300",
        summary: "border-border/70 bg-muted/20 text-foreground",
      };
  }
}
function eventCardAccentClass(event: RunReviewEventRowData, index: number): string {
  if (event.severity === "error") return "bg-destructive";
  if (event.severity === "warning") return "bg-amber-500";
  return timelinePaletteClass(event.taskId || event.sourceLabel || event.category || event.id || String(index));
}

function eventGroupAccentClass(group: RunReviewEventGroupData, index: number): string {
  if (group.severity === "error") return "bg-destructive";
  if (group.severity === "warning") return "bg-amber-500";
  return timelinePaletteClass(group.taskId || group.label || group.key || String(index));
}

function eventGroupOutcomeClass(group: RunReviewEventGroupData): string {
  if (group.severity === "error") return "border-destructive/30 bg-destructive/10 text-destructive";
  if (group.severity === "warning") return "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300";
  return "border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300";
}

function timelineNodeColorClass(node: IssueTimelineNode): string {
  return `${timelinePaletteClass(node.agent_id || node.agent_name || node.node_id)} text-white`;
}

function timelinePaletteClass(key: string) {
  const colors = ["bg-violet-600", "bg-sky-600", "bg-emerald-600", "bg-amber-600", "bg-rose-600", "bg-teal-600", "bg-indigo-600", "bg-cyan-700"];
  return colors[stableHash(key) % colors.length] ?? "bg-slate-600";
}

function stableHash(value: string) {
  let hash = 2166136261;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 16777619);
  }
  return hash >>> 0;
}

function RunFailureBanner({
  task,
  retrying,
  onRetry,
}: {
  task: AgentTask;
  retrying: boolean;
  onRetry: () => void;
}) {
  const failureReason = String(task.failure_reason ?? "");
  const providerNetwork = failureReason === "agent_error.provider_network";
  return (
    <div className="flex flex-col gap-3 border-b bg-destructive/5 px-4 py-3 text-sm md:flex-row md:items-center md:justify-between">
      <div className="flex min-w-0 gap-3">
        <div className="mt-0.5 rounded-md border bg-background p-2 text-destructive">
          {providerNetwork ? <WifiOff className="size-4" /> : <AlertTriangle className="size-4" />}
        </div>
        <div className="min-w-0">
          <div className="font-medium text-destructive">
            {providerNetwork ? "模型网络连接中断，网络恢复后可重试" : "最新任务失败，等待重试"}
          </div>
          <div className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">
            {task.error || failureReason || "未记录失败详情"}
          </div>
        </div>
      </div>
      <button
        type="button"
        className="inline-flex shrink-0 items-center justify-center gap-1.5 rounded border border-destructive/30 bg-background px-2.5 py-1.5 text-xs text-destructive hover:bg-destructive/10 disabled:cursor-not-allowed disabled:opacity-60"
        onClick={onRetry}
        disabled={retrying}
        data-testid="run-review-retry-latest-failed-task"
      >
        {retrying ? <Loader2 className="size-3.5 animate-spin" /> : <RotateCcw className="size-3.5" />}
        重试最新失败任务
      </button>
    </div>
  );
}

function SectionTitle({ title, subtitle, action }: { title: string; subtitle: string; action?: ReactNode }) {
  return (
    <div className="flex flex-col gap-2 px-4 py-3 sm:flex-row sm:items-start sm:justify-between">
      <div className="min-w-0">
        <div className="text-sm font-semibold">{title}</div>
        <div className="mt-0.5 text-xs text-muted-foreground">{subtitle}</div>
      </div>
      {action && <div className="shrink-0">{action}</div>}
    </div>
  );
}

function Metric({ label, value, detail }: { label: string; value: string; detail: string }) {
  return (
    <div className="min-w-0 rounded-md border bg-background px-4 py-3 shadow-sm">
      <div className="truncate text-sm font-medium text-muted-foreground">{label}</div>
      <div className="mt-2 truncate text-2xl font-semibold leading-none tracking-normal text-foreground">{value}</div>
      <div className="mt-3 truncate text-sm text-muted-foreground" title={detail}>{detail}</div>
    </div>
  );
}

function MetricTooltip({ rows }: { rows: Array<[string, string]> }) {
  return (
    <div className="grid gap-1 text-xs">
      {rows.map(([label, value]) => (
        <div key={label} className="flex items-center justify-between gap-4">
          <span className="text-muted-foreground">{label}</span>
          <span className="font-medium text-foreground">{value}</span>
        </div>
      ))}
    </div>
  );
}

function ExportButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button
      type="button"
      className="inline-flex items-center gap-1.5 rounded border bg-background px-2.5 py-1.5 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
      onClick={onClick}
    >
      <Download className="size-3.5" />
      {label}
    </button>
  );
}

function NodeMetric({ value, tooltip }: { value: string; tooltip: ReactNode }) {
  return (
    <span className="inline-flex min-w-0 items-center gap-1">
      <span className="truncate">{value}</span>
      <TooltipProvider delay={0}>
        <Tooltip>
          <TooltipTrigger
            render={
              <button type="button" className="shrink-0 text-muted-foreground hover:text-foreground" aria-label="节点指标说明">
                <HelpCircle className="size-3" />
              </button>
            }
          />
          <TooltipContent side="top" className="max-w-72">
            {tooltip}
          </TooltipContent>
        </Tooltip>
      </TooltipProvider>
    </span>
  );
}

function NodeFact({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <span className="text-muted-foreground/80">{label}：</span>
      <span className="break-words">{value}</span>
    </div>
  );
}

function ArtifactLinks({ artifacts }: { artifacts: AgentTaskArtifact[] }) {
  const uniqueArtifacts = dedupeArtifacts(artifacts);
  if (!uniqueArtifacts.length) return <span className="text-muted-foreground">-</span>;
  return (
    <div className="flex min-w-0 flex-wrap gap-1.5">
      {uniqueArtifacts.slice(0, 3).map((artifact) => (
        <a
          key={artifact.id}
          href={artifactDownloadHref(artifact)}
          target="_blank"
          rel="noreferrer"
          className="inline-flex max-w-full items-center rounded border bg-background px-2 py-0.5 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
          title={artifact.filename}
        >
          <span className="truncate">{artifact.title || artifact.filename}</span>
        </a>
      ))}
      {uniqueArtifacts.length > 3 ? (
        <span className="inline-flex items-center rounded border px-2 py-0.5 text-xs text-muted-foreground">
          +{uniqueArtifacts.length - 3}
        </span>
      ) : null}
    </div>
  );
}

function TimelineLaneChart({
  stageRows,
  childLanes,
  timelineNodes,
}: {
  stageRows: TimelineNodeRow[];
  childLanes: ChildLane[];
  timelineNodes: IssueTimelineNode[];
}) {
  const rows = buildTimelineBarRows(stageRows, childLanes, timelineNodes);
  const timedSegments = rows.flatMap((row) => row.segments).filter((segment) => segment.startMs !== null && segment.endMs !== null);
  const min = timedSegments.length > 0 ? Math.min(...timedSegments.map((segment) => segment.startMs as number)) : 0;
  const max = timedSegments.length > 0 ? Math.max(...timedSegments.map((segment) => segment.endMs as number)) : min + 1;
  const span = Math.max(max - min, 1);
  const ticks = timedSegments.length > 0
    ? [min, min + span / 2, max].map((value) => formatTimeTick(value))
    : ["开始", "中点", "结束"];

  return (
    <div className="px-4 pb-4" data-testid="run-review-horizontal-timeline">
      <div className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-3 text-[11px] text-muted-foreground">
        <div />
        <div className="grid grid-cols-3">
          {ticks.map((tick, index) => (
            <div key={`${tick}-${index}`} className={cn(index === 1 && "text-center", index === 2 && "text-right")}>
              {tick}
            </div>
          ))}
        </div>
      </div>
      <TooltipProvider delay={0}>
        <div className="mt-2 space-y-1.5">
          {rows.length > 0 ? rows.map((row) => (
            <div key={row.key} className="grid grid-cols-[6.5rem_minmax(0,1fr)] items-center gap-x-3">
              <div className="min-w-0">
                <div className="truncate text-xs font-medium">{row.label}</div>
                <div className="truncate text-[11px] text-muted-foreground">{row.subtitle}</div>
              </div>
              <div className="relative h-9 overflow-hidden rounded-md border bg-muted/20">
                <div className="absolute inset-y-0 left-1/3 w-px bg-border/70" />
                <div className="absolute inset-y-0 left-2/3 w-px bg-border/70" />
                {row.missing || row.segments.length === 0 || row.segments.every((segment) => segment.startMs === null || segment.endMs === null) ? (
                  <div className="flex h-full items-center px-2 text-[11px] text-muted-foreground">
                    {row.missing ? "缺节点" : "缺时间"}
                  </div>
                ) : (
                  row.segments.map((segment) => {
                    if (segment.startMs === null || segment.endMs === null) return null;
                    const label = timelineSegmentTitle(row, segment);
                    return (
                      <Tooltip key={segment.key}>
                        <TooltipTrigger
                          render={
                            <div
                              className={cn(
                                "absolute top-1 bottom-1 overflow-hidden rounded px-2 text-[11px] leading-7 shadow-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1",
                                timelineSegmentClassName(row.kind, segment),
                              )}
                              data-testid={`run-review-timeline-bar-${segment.key}`}
                              style={timelineSegmentStyle(segment.startMs, segment.endMs, min, span)}
                              aria-label={label}
                              tabIndex={0}
                            >
                              <span className="block truncate">
                                {timelineSegmentText(row, segment)}
                              </span>
                            </div>
                          }
                        />
                        <TooltipContent side="top" className="max-w-80">
                          <MetricTooltip rows={timelineSegmentTooltipRows(row, segment)} />
                        </TooltipContent>
                      </Tooltip>
                    );
                  })
                )}
              </div>
            </div>
          )) : (
            <div className="rounded-md border border-dashed bg-muted/20 px-3 py-4 text-sm text-muted-foreground">
              暂无可绘制的真实执行节点。
            </div>
          )}
        </div>
      </TooltipProvider>
    </div>
  );
}
function timelineSegmentClassName(kind: TimelineBarRow["kind"], _segment: TimelineBarSegment) {
  if (kind === "child") return "bg-sky-600 text-white";
  if (kind === "human_confirmation") return "border border-amber-700/30 bg-amber-500 text-white";
  return timelineNodeColorClass(_segment.node);
}

function timelineSegmentText(row: TimelineBarRow, segment: TimelineBarSegment) {
  if (row.kind === "human_confirmation") {
    return `${formatDuration(segment.durationMs)} · 人工确认`;
  }
  if (row.kind === "child") {
    return `${formatDuration(segment.durationMs)} · 等待子任务`;
  }
  return `${formatDuration(segment.durationMs)} · ${formatNumber(segment.tokenTotal)} token`;
}

function timelineSegmentTitle(row: TimelineBarRow, segment: TimelineBarSegment) {
  return timelineSegmentTooltipRows(row, segment)
    .map(([label, value]) => `${label} ${value}`)
    .join(" · ");
}
function IssueListSkeleton() {
  return (
    <div className="space-y-2 p-3">
      {Array.from({ length: 5 }).map((_, index) => (
        <Skeleton key={index} className="h-16 w-full rounded-md" />
      ))}
    </div>
  );
}

function DetailSkeleton() {
  return <Skeleton className="h-24 w-full rounded-md" />;
}
function nodeDurationTooltip(node: IssueTimelineNode | undefined) {
  return (
    <MetricTooltip
      rows={[
        ["开始时间", formatDateTime(node?.started_at)],
        ["结束时间", formatDateTime(node?.completed_at)],
        ["执行耗时", formatDuration(node?.duration_ms ?? 0)],
      ]}
    />
  );
}

function nodeTokenTooltip(node: IssueTimelineNode | undefined) {
  return (
    <MetricTooltip
      rows={[
        ["输入", formatNumber(node?.input_tokens ?? 0)],
        ["输出", formatNumber(node?.output_tokens ?? 0)],
        ["缓存读", formatNumber(node?.cache_read_tokens ?? 0)],
        ["缓存写", formatNumber(node?.cache_write_tokens ?? 0)],
        ["缓存命中率", formatPercent(cacheReuseRate(node?.cache_read_tokens ?? 0, node?.cache_write_tokens ?? 0))],
      ]}
    />
  );
}
function downloadXlsx(filename: string, sheets: XlsxSheetSpec[]) {
  if (typeof window === "undefined" || typeof document === "undefined") return;
  const workbook = buildXlsxWorkbook(sheets);
  const data = XLSX.write(workbook, { bookType: "xlsx", type: "array" }) as ArrayBuffer;
  const blob = new Blob([data], { type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" });
  const url = window.URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = sanitizeFilename(filename);
  document.body.appendChild(link);
  link.click();
  link.remove();
  window.URL.revokeObjectURL(url);
}

function sanitizeFilename(filename: string) {
  return filename.replace(/[^\w.-]+/g, "_");
}
