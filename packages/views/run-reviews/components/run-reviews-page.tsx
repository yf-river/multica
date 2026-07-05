"use client";

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Activity, AlertTriangle, Download, GitBranch, HelpCircle, ListChecks, Loader2, RotateCcw, Timer, WifiOff } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { issueExecutionTreeOptions, issueKeys, issueListOptions } from "@multica/core/issues/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import type { AgentTask, AgentTaskArtifact, CreatePromptEvaluationCaseRequest, Issue, IssueTimelineNode, IssueTimelineSummary, IssueExecutionNode, IssueExecutionTreeResponse, TaskTraceEvent } from "@multica/core/types";
import { useWSEvent, useWSReconnect } from "@multica/core/realtime";
import type {
  TaskCancelledPayload,
  TaskCompletedPayload,
  TaskDispatchPayload,
  TaskFailedPayload,
  TaskMessagePayload,
  TaskQueuedPayload,
  TaskRunningPayload,
  TaskWaitingLocalDirectoryPayload,
} from "@multica/core/types/events";
import type { PromptEvaluationToolCallChain } from "@multica/core/types/prompt-evaluation";
import { cn } from "@multica/ui/lib/utils";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Tooltip, TooltipContent, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { PageHeader } from "../../layout/page-header";
import { AppLink, useNavigation } from "../../navigation";
import { TranscriptButton } from "../../common/task-transcript";
import { SOP_STAGE_DEFINITIONS, normalizeSopStageName, sopStageDisplayName } from "../../common/sop-stage-labels";

const STAGES = SOP_STAGE_DEFINITIONS;

const ISSUE_REVIEW_DRAFT_DATASET_NAME = "Issue 复盘评测 Draft";
const RUN_REVIEW_MESSAGE_REFRESH_DEBOUNCE_MS = 1_200;
const RUN_REVIEW_MESSAGE_REFRESH_MAX_WAIT_MS = 4_000;
const RUN_REVIEW_LIVE_DURATION_TICK_MS = 1_000;

export function buildRunReviewOptimizerHref(trainingView: (view: string) => string, issueId: string): string {
  return `${trainingView("evaluation-runs")}?issue=${encodeURIComponent(issueId)}`;
}

export function runReviewTotalDurationMs(summary: IssueTimelineSummary | undefined): number {
  return summary?.wall_clock_duration_ms ?? summary?.total_duration_ms ?? 0;
}

export function buildRunReviewDurationTooltipRows(summary: IssueTimelineSummary | undefined): Array<[string, string]> {
  const agentExecution = summary?.agent_execution_duration_ms ?? summary?.total_duration_ms ?? 0;
  const humanConfirmation = summary?.human_confirmation_duration_ms;
  return [
    ["Agent 执行耗时", formatDuration(agentExecution)],
    ["人工/等待耗时", humanConfirmation == null ? "未记录" : formatDuration(humanConfirmation)],
  ];
}

export function buildRunReviewLiveSummary(
  summary: IssueTimelineSummary | undefined,
  activeTasks: AgentTask[],
  timelineNodes: IssueTimelineNode[],
  nowMs: number,
): IssueTimelineSummary | undefined {
  if (!summary) return summary;
  const liveDurationMs = Math.max(
    0,
    ...activeTasks.map((task) => liveElapsedMs(task.started_at ?? task.dispatched_at ?? task.created_at, nowMs)),
    ...timelineNodes
      .filter(isActiveTimelineNode)
      .map((node) => liveElapsedMs(node.started_at, nowMs)),
  );
  if (liveDurationMs <= 0) return summary;
  return {
    ...summary,
    total_duration_ms: Math.max(summary.total_duration_ms ?? 0, liveDurationMs),
    agent_execution_duration_ms: Math.max(summary.agent_execution_duration_ms ?? summary.total_duration_ms ?? 0, liveDurationMs),
    wall_clock_duration_ms: summary.wall_clock_duration_ms == null
      ? summary.wall_clock_duration_ms
      : Math.max(summary.wall_clock_duration_ms, liveDurationMs),
  };
}

export function buildRunReviewLiveTimelineNodes(timelineNodes: IssueTimelineNode[], nowMs: number): IssueTimelineNode[] {
  return timelineNodes.map((node) => {
    if (!isActiveTimelineNode(node)) return node;
    const durationMs = liveElapsedMs(node.started_at, nowMs);
    if (durationMs <= (node.duration_ms ?? 0)) return node;
    return { ...node, duration_ms: durationMs };
  });
}

function useRunReviewLiveNow(active: boolean) {
  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    setNowMs(Date.now());
    const timer = setInterval(() => setNowMs(Date.now()), RUN_REVIEW_LIVE_DURATION_TICK_MS);
    return () => clearInterval(timer);
  }, [active]);
  return nowMs;
}

function liveElapsedMs(startedAt: string | null | undefined, nowMs: number) {
  const startedMs = parseTimeMs(startedAt ?? undefined);
  if (startedMs === null || startedMs > nowMs) return 0;
  return nowMs - startedMs;
}

function hasActiveTimelineNode(timelineNodes: IssueTimelineNode[]) {
  return timelineNodes.some(isActiveTimelineNode);
}

function isActiveTimelineNode(node: Pick<IssueTimelineNode, "status" | "started_at" | "completed_at">) {
  return isActiveStatus(node.status) && Boolean(node.started_at) && !node.completed_at;
}

export function RunReviewsPage() {
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const navigation = useNavigation();
  const selectedIssueId = navigation.searchParams.get("issue");
  const issuesQuery = useQuery(issueListOptions(wsId, { sort_by: "created_at", sort_direction: "desc" }));
  const issues = issuesQuery.data ?? [];
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
              evalDraftHref={`${paths.trainingView("datasets")}?issue=${encodeURIComponent(selectedIssue.id)}&mode=draft`}
              optimizerHref={buildRunReviewOptimizerHref(paths.trainingView, selectedIssue.id)}
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

type RunReviewTaskEventPayload =
  | TaskQueuedPayload
  | TaskDispatchPayload
  | TaskRunningPayload
  | TaskWaitingLocalDirectoryPayload
  | TaskCompletedPayload
  | TaskFailedPayload
  | TaskCancelledPayload
  | TaskMessagePayload;

export function shouldRefreshRunReviewForTaskEvent(issueId: string, payload: Pick<RunReviewTaskEventPayload, "issue_id"> | null | undefined): boolean {
  return Boolean(issueId && payload?.issue_id === issueId);
}

export function runReviewMessageRefreshDelayMs(
  nowMs: number,
  lastRefreshAtMs: number,
  debounceMs = RUN_REVIEW_MESSAGE_REFRESH_DEBOUNCE_MS,
  maxWaitMs = RUN_REVIEW_MESSAGE_REFRESH_MAX_WAIT_MS,
): number {
  if (lastRefreshAtMs <= 0) return debounceMs;
  const elapsedMs = Math.max(0, nowMs - lastRefreshAtMs);
  if (elapsedMs >= maxWaitMs) return 0;
  return Math.min(debounceMs, maxWaitMs - elapsedMs);
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
  useRunReviewRealtimeSync(issue.id, wsId);
  const { data: tasks = [] } = useQuery({
    queryKey: issueKeys.tasks(issue.id),
    queryFn: () => api.listTasksByIssue(issue.id),
    staleTime: 30_000,
    refetchOnWindowFocus: true,
  });
  const activeTasks = useMemo(() => tasks.filter(isActiveTask), [tasks]);
  const baseTimelineNodes = tree?.timeline_nodes ?? [];
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
  const tokenTotal = (summary?.total_input_tokens ?? 0) +
    (summary?.total_output_tokens ?? 0) +
    (summary?.total_cache_read_tokens ?? 0) +
    (summary?.total_cache_write_tokens ?? 0);
  const nodeCsv = buildRunReviewNodeCsv(issue, summary, agentNodeRows, visibleChildLanes);
  const rawEventsCsv = buildRunReviewRawEventsCsv(eventRows);
  const taskById = useMemo(() => {
    const result = new Map<string, AgentTask>();
    for (const task of [...flattenExecutionTasks(tree), ...tasks]) {
      result.set(task.id, task);
    }
    return result;
  }, [tasks, tree]);
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

        <div className="grid gap-0 divide-y text-sm md:grid-cols-3 md:divide-x md:divide-y-0">
          <Metric
            label="总耗时"
            value={formatDuration(wallClockDurationMs)}
            icon={<Timer className="size-3.5" />}
            tooltip={
              <MetricTooltip
                rows={buildRunReviewDurationTooltipRows(summary)}
              />
            }
          />
          <Metric
            label="总 Token"
            value={formatNumber(tokenTotal)}
            icon={<Activity className="size-3.5" />}
            tooltip={
              <MetricTooltip
                rows={[
                  ["输入 Token", formatNumber(summary?.total_input_tokens ?? 0)],
                  ["输出 Token", formatNumber(summary?.total_output_tokens ?? 0)],
                  ["缓存读", formatNumber(summary?.total_cache_read_tokens ?? 0)],
                  ["缓存写", formatNumber(summary?.total_cache_write_tokens ?? 0)],
                  ["缓存命中率", formatPercent(cacheReuseRate(summary?.total_cache_read_tokens ?? 0, summary?.total_cache_write_tokens ?? 0))],
                ]}
              />
            }
          />
          <Metric label="思考轮次" value={formatNumber(summary?.agent_turn_count ?? 0)} icon={<ListChecks className="size-3.5" />} />
        </div>
      </section>

      {loading ? <DetailSkeleton /> : null}

      <section className="rounded-md border bg-card">
        <SectionTitle title="横向时序图" subtitle="按真实出现的执行节点绘制；节点存在但缺少开始/结束时间时会单独标记。" />
        <TimelineLaneChart stageRows={visibleTimelineRows} childLanes={visibleChildLanes} timelineNodes={timelineNodes} />
      </section>

      <section className="rounded-md border bg-card">
        <SectionTitle title="子任务泳道" subtitle="只展示执行树中真实关联的跨项目子 issue。" />
        <div className="space-y-2 px-4 pb-4">
          {visibleChildLanes.length > 0 ? visibleChildLanes.map((lane) => (
            <div key={lane.key} className="flex items-center justify-between gap-3 rounded-md border bg-background px-3 py-2 text-sm">
              <div className="min-w-0">
                <div className="font-medium">{lane.label}</div>
                <div className="truncate text-xs text-muted-foreground">{lane.issue ? `${lane.issue.identifier} ${lane.issue.title}` : ""}</div>
              </div>
              <span className="shrink-0 rounded border px-2 py-1 text-xs text-muted-foreground">{statusLabel(lane.issue?.status ?? "")}</span>
            </div>
          )) : (
            <div className="rounded-md border border-dashed bg-muted/20 px-3 py-4 text-sm text-muted-foreground">
              当前执行树暂无跨项目子 issue。
            </div>
          )}
        </div>
      </section>

      <section className="rounded-md border bg-card">
        <SectionTitle
          title="节点表"
          subtitle="按 Agent 运行节点展示耗时、token、轮次和产物。"
          action={
            <ExportButton
              label="导出节点数据"
              onClick={() => downloadCsv(`run-review-nodes-${issue.identifier || issue.id}.csv`, nodeCsv)}
            />
          }
        />
        <div className="hidden md:block">
          <table className="w-full table-fixed text-sm">
            <thead className="border-y bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="w-[18%] px-3 py-2 font-medium">节点</th>
                <th className="w-[16%] px-3 py-2 font-medium">Agent</th>
                <th className="w-[12%] px-3 py-2 font-medium">状态</th>
                <th className="w-[12%] px-3 py-2 font-medium">耗时</th>
                <th className="w-[12%] px-3 py-2 font-medium">Token</th>
                <th className="w-[10%] px-3 py-2 font-medium">思考轮次</th>
                <th className="w-[20%] px-3 py-2 font-medium">产物</th>
              </tr>
            </thead>
            <tbody className="divide-y">
              {agentNodeRows.length > 0 ? agentNodeRows.map((row) => (
                <tr key={row.key}>
                  <td className="truncate px-3 py-2">{agentNodeDisplayLabel(row)}</td>
                  <td className="truncate px-3 py-2 text-muted-foreground">{row.node.agent_name ?? row.key}</td>
                  <td className="truncate px-3 py-2">{statusLabel(row.node.status)}</td>
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
                  <td className="px-3 py-5 text-sm text-muted-foreground" colSpan={7}>暂无真实 Agent 节点。</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        <div className="divide-y md:hidden">
          {agentNodeRows.length > 0 ? agentNodeRows.map((row) => (
            <div key={row.key} className="px-4 py-3 text-sm">
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0 truncate font-medium">{agentNodeDisplayLabel(row)}</div>
                <span className="shrink-0 rounded border px-2 py-0.5 text-xs text-muted-foreground">
                  {statusLabel(row.node.status)}
                </span>
              </div>
              <div className="mt-2 grid grid-cols-2 gap-x-3 gap-y-1 text-xs text-muted-foreground">
                <NodeFact label="Agent" value={row.node.agent_name ?? row.key} />
                <NodeFact label="耗时" value={formatDuration(row.node.duration_ms ?? 0)} />
                <NodeFact label="Token" value={formatNumber(nodeTokenTotal(row.node))} />
                <NodeFact label="思考轮次" value={formatNumber(row.node.agent_turn_count ?? 0)} />
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
          subtitle="展示去重后的事件摘要；通过每行右侧详细信息查看完整转录。"
          action={
            <ExportButton
              label="导出 RAW 交互信息"
              onClick={() => downloadCsv(`run-review-events-${issue.identifier || issue.id}.csv`, rawEventsCsv)}
            />
          }
        />
        <div className="min-h-[24rem] divide-y">
          {eventRows.length > 0 ? eventRows.map((node) => (
            <RunReviewEventRow
              key={node.id}
              event={node}
              task={node.taskId ? taskById.get(node.taskId) : undefined}
            />
          )) : (
            <div className="flex gap-2 px-4 py-6 text-sm text-muted-foreground">
              <AlertTriangle className="size-4" />
              暂无事件。真实任务开始后会回写 trace、用量和证据。
            </div>
          )}
        </div>
      </section>
    </div>
  );
}

function RunReviewEventRow({
  event,
  task,
}: {
  event: RunReviewEventRowData;
  task: AgentTask | undefined;
}) {
  return (
    <div className="flex gap-3 px-4 py-3 text-sm" data-testid={`run-review-event-${event.kind}`}>
      <div className={cn("mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-md border", eventToneClasses(event.severity).icon)}>
        <GitBranch className="size-3.5" />
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="shrink-0 rounded border px-1.5 py-0.5 text-[11px] text-muted-foreground">{event.category}</span>
          <span className={cn("shrink-0 rounded border px-1.5 py-0.5 text-[11px]", eventToneClasses(event.severity).chip)}>
            {event.outcome}
          </span>
          <span className="min-w-0 truncate font-medium">{event.title}</span>
        </div>
        <div className="mt-0.5 flex flex-wrap gap-x-2 gap-y-1 text-xs text-muted-foreground">
          {event.timeLabel && <span>{event.timeLabel}</span>}
          {event.sourceLabel && <span>{event.sourceLabel}</span>}
          {event.object && <span>{event.object}</span>}
          {event.durationMs > 0 && <span>耗时 {formatDuration(event.durationMs)}</span>}
          {event.tokenTotal > 0 && <span>Token {formatNumber(event.tokenTotal)}</span>}
          {event.taskId && <span className="font-mono">task {shortId(event.taskId)}</span>}
        </div>
        {event.summary && (
          <div className={cn("mt-1 rounded border px-2 py-1 text-xs leading-5", eventToneClasses(event.severity).summary)}>
            {event.summary}
          </div>
        )}
      </div>
      {task && (
        <div className="shrink-0">
          <TranscriptButton task={task} agentName="" title="详细信息" />
        </div>
      )}
    </div>
  );
}

function eventToneClasses(severity: RunReviewEventRowData["severity"]) {
  switch (severity) {
    case "error":
      return {
        icon: "border-destructive/30 bg-destructive/10 text-destructive",
        chip: "border-destructive/30 bg-destructive/10 text-destructive",
        summary: "border-destructive/25 bg-destructive/5 text-destructive",
      };
    case "warning":
      return {
        icon: "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300",
        chip: "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300",
        summary: "border-amber-500/25 bg-amber-500/5 text-foreground",
      };
    default:
      return {
        icon: "border-border bg-background text-muted-foreground",
        chip: "border-border bg-muted/30 text-muted-foreground",
        summary: "border-border/70 bg-muted/20 text-foreground",
      };
  }
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

function Metric({ label, value, icon, tooltip }: { label: string; value: string; icon: ReactNode; tooltip?: ReactNode }) {
  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <div className="rounded-md border bg-background p-2 text-muted-foreground">{icon}</div>
      <div>
        <div className="flex items-center gap-1 text-xs text-muted-foreground">
          <span>{label}</span>
          {tooltip && (
            <Tooltip>
              <TooltipTrigger
                render={
                  <button type="button" className="text-muted-foreground hover:text-foreground" aria-label={`${label}说明`}>
                    <HelpCircle className="size-3" />
                  </button>
                }
              />
              <TooltipContent side="top" className="max-w-72">
                {tooltip}
              </TooltipContent>
            </Tooltip>
          )}
        </div>
        <div className="text-sm font-semibold">{value}</div>
      </div>
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

function agentNodeDisplayLabel(row: ReturnType<typeof buildAgentNodeRows>[number]) {
  return row.runCount > 1 ? `${row.label} (${row.runCount} 次)` : row.label;
}

function ArtifactLinks({ artifacts }: { artifacts: AgentTaskArtifact[] }) {
  if (!artifacts.length) return <span className="text-muted-foreground">-</span>;
  return (
    <div className="flex min-w-0 flex-wrap gap-1.5">
      {artifacts.slice(0, 3).map((artifact) => (
        <a
          key={artifact.id}
          href={artifact.markdown_url || artifact.download_url}
          target="_blank"
          rel="noreferrer"
          className="inline-flex max-w-full items-center rounded border bg-background px-2 py-0.5 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
          title={artifact.filename}
        >
          <span className="truncate">{artifact.title || artifact.filename}</span>
        </a>
      ))}
      {artifacts.length > 3 ? (
        <span className="inline-flex items-center rounded border px-2 py-0.5 text-xs text-muted-foreground">
          +{artifacts.length - 3}
        </span>
      ) : null}
    </div>
  );
}

export interface TimelineNodeSegment {
  key: string;
  label: string;
  node: IssueTimelineNode;
  ordinal: number;
  total: number;
}

export type TimelineNodeRow = {
  key: string;
  label: string;
  node?: IssueTimelineNode;
  segments?: TimelineNodeSegment[];
};
type ChildLane = ReturnType<typeof buildChildLanes>[number];

interface TimelineBarSegment {
  key: string;
  label: string;
  status: string;
  startMs: number | null;
  endMs: number | null;
  durationMs: number;
  tokenTotal: number;
  turns: number;
  ordinal: number;
  total: number;
}

interface TimelineBarRow {
  key: string;
  label: string;
  kind: "stage" | "child";
  status: string;
  subtitle: string;
  segments: TimelineBarSegment[];
  missing: boolean;
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
                row.segments.map((segment) => (
                  segment.startMs === null || segment.endMs === null ? null : (
                    <div
                      key={segment.key}
                      className={cn(
                        "absolute top-1 bottom-1 min-w-[2rem] rounded px-2 text-[11px] leading-7 text-white shadow-sm",
                        row.kind === "child" ? "bg-sky-600" : "bg-emerald-600",
                      )}
                      data-testid={`run-review-timeline-bar-${segment.key}`}
                      style={{
                        left: `${Math.max(0, ((segment.startMs - min) / span) * 100)}%`,
                        width: `${Math.max(6, ((segment.endMs - segment.startMs) / span) * 100)}%`,
                      }}
                      title={[
                        segment.total > 1 ? `${row.label} #${segment.ordinal}` : row.label,
                        `开始 ${formatDateTime(segment.startMs)}`,
                        `结束 ${formatDateTime(segment.endMs)}`,
                        `耗时 ${formatDuration(segment.durationMs)}`,
                        `Token ${formatNumber(segment.tokenTotal)}`,
                        `思考轮次 ${formatNumber(segment.turns)}`,
                      ].join(" · ")}
                    >
                      <span className="block truncate">
                        {segment.total > 1 ? `#${segment.ordinal} · ` : ""}{formatDuration(segment.durationMs)} · {formatNumber(segment.tokenTotal)} token
                      </span>
                    </div>
                  )
                ))
              )}
            </div>
          </div>
        )) : (
          <div className="rounded-md border border-dashed bg-muted/20 px-3 py-4 text-sm text-muted-foreground">
            暂无可绘制的真实执行节点。
          </div>
        )}
      </div>
    </div>
  );
}

function buildTimelineBarRows(
  stageRows: TimelineNodeRow[],
  childLanes: ChildLane[],
  timelineNodes: IssueTimelineNode[],
): TimelineBarRow[] {
  const stageBars = stageRows.filter((stage) => stage.node).map((stage) => {
    const segments = timelineRowSegments(stage);
    return {
      key: stage.key,
      label: stage.label,
      kind: "stage" as const,
      status: stage.node?.status ?? "missing",
      subtitle: timelineRowSubtitle(stage.node?.status ?? "missing", segments.length),
      segments,
      missing: !stage.node,
    };
  });
  const childBars = childLanes.filter((lane) => lane.issue).map((lane) => {
    const node = timelineNodes.find((item) => item.node_type === "child_issue_ref" && item.child_issue_id === lane.issue?.id);
    const segments = node ? [timelineNodeSegment(lane.key, lane.label, node, 1, 1)] : [];
    return {
      key: lane.key,
      label: lane.label,
      kind: "child" as const,
      status: lane.issue?.status ?? "missing",
      subtitle: statusLabel(lane.issue?.status ?? "missing"),
      segments,
      missing: !lane.issue,
    };
  });
  return [...stageBars, ...childBars];
}

function timelineRowSegments(row: TimelineNodeRow): TimelineBarSegment[] {
  const segments = row.segments?.length
    ? row.segments
    : row.node
      ? [{ key: row.key, label: row.label, node: row.node, ordinal: 1, total: 1 }]
      : [];
  return segments.map((segment) => timelineNodeSegment(segment.key, segment.label, segment.node, segment.ordinal, segment.total));
}

function timelineNodeSegment(
  key: string,
  label: string,
  node: IssueTimelineNode,
  ordinal: number,
  total: number,
): TimelineBarSegment {
  const timing = timelineTiming(node);
  return {
    key,
    label,
    status: node.status,
    ...timing,
    durationMs: node.duration_ms ?? timing.durationMs,
    tokenTotal: (node.input_tokens ?? 0) + (node.output_tokens ?? 0),
    turns: node.agent_turn_count ?? 0,
    ordinal,
    total,
  };
}

function timelineRowSubtitle(status: string, runCount: number) {
  return runCount > 1 ? `${formatNumber(runCount)} 次 · ${statusLabel(status)}` : statusLabel(status);
}

function timelineTiming(node: IssueTimelineNode | undefined) {
  if (!node) return { startMs: null, endMs: null, durationMs: 0 };
  const start = parseTimeMs(node.started_at);
  const completed = parseTimeMs(node.completed_at);
  const duration = Math.max(node.duration_ms ?? 0, 0);
  if (start === null && completed === null) return { startMs: null, endMs: null, durationMs: duration };
  const startMs = start ?? Math.max((completed as number) - Math.max(duration, 60_000), 0);
  const endMs = Math.max(completed ?? startMs + Math.max(duration, 60_000), startMs + Math.max(duration, 60_000));
  return { startMs, endMs, durationMs: Math.max(duration, endMs - startMs) };
}

function parseTimeMs(value: string | undefined) {
  if (!value) return null;
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : null;
}

function formatTimeTick(value: number) {
  return new Intl.DateTimeFormat("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(new Date(value));
}

function buildStageRows(nodes: IssueTimelineNode[]) {
  return STAGES.map((stage) => ({
    ...stage,
    node: nodes.filter((node) => {
      if (node.node_type !== "agent_task") return false;
      const agentName = normalizeSopStageName(node.agent_name);
      if (!agentName) return false;
      return stage.names.some((name) => agentName === normalizeSopStageName(name));
    }).sort(compareStageNodeCandidates)[0],
  }));
}

export function buildTimelineAgentRows(nodes: IssueTimelineNode[]): TimelineNodeRow[] {
  const agentNodes = nodes.filter((node) => node.node_type === "agent_task");
  const grouped = new Map<string, TimelineNodeRow>();
  for (const [index, node] of agentNodes.entries()) {
    const groupKey = timelineAgentRowGroupKey(node, index);
    const label = timelineAgentRowBaseLabel(node, index);
    const taskId = node.node_id.replace(/^task:/, "");
    const existing = grouped.get(groupKey);
    const segment = {
      key: `${groupKey}:${taskId || index}`,
      label,
      node,
      ordinal: 1,
      total: 1,
    };
    if (!existing) {
      grouped.set(groupKey, { key: groupKey, label, node: { ...node }, segments: [segment] });
      continue;
    }
    existing.node = existing.node ? mergeTimelineNode(existing.node, node) : { ...node };
    existing.segments = [...(existing.segments ?? []), segment];
  }

  return [...grouped.values()].map((row) => {
    const segments = [...(row.segments ?? [])].sort(compareTimelineSegments);
    const total = segments.length || 1;
    return {
      ...row,
      segments: segments.map((segment, index) => ({
        ...segment,
        ordinal: index + 1,
        total,
      })),
    };
  });
}

function compareTimelineSegments(left: TimelineNodeSegment, right: TimelineNodeSegment) {
  const leftStart = parseTimeMs(left.node.started_at) ?? parseTimeMs(left.node.completed_at) ?? 0;
  const rightStart = parseTimeMs(right.node.started_at) ?? parseTimeMs(right.node.completed_at) ?? 0;
  if (leftStart !== rightStart) return leftStart - rightStart;
  return left.key.localeCompare(right.key);
}

function timelineAgentRowGroupKey(node: IssueTimelineNode, index: number) {
  const taskId = node.node_id.replace(/^task:/, "");
  const agentName = node.agent_name || taskId || `agent-node-${index + 1}`;
  return node.agent_id || normalizeSopStageName(agentName) || agentName || taskId || `agent-node-${index + 1}`;
}

function timelineAgentRowBaseLabel(node: IssueTimelineNode, index: number) {
  const taskId = node.node_id.replace(/^task:/, "");
  const agentName = node.agent_name || taskId || `agent-node-${index + 1}`;
  return sopStageDisplayName(agentName) || node.summary || agentName;
}

function compareStageNodeCandidates(left: IssueTimelineNode, right: IssueTimelineNode) {
  return stageNodeScore(right) - stageNodeScore(left);
}

function stageNodeScore(node: IssueTimelineNode) {
  let score = 0;
  if (node.node_type === "agent_task") score += 1000;
  if (node.status === "completed" || node.status === "已完成") score += 200;
  if (node.status === "cancelled" || node.status === "已取消") score -= 200;
  if ((node.input_tokens ?? 0) + (node.output_tokens ?? 0) > 0) score += 100;
  if (node.started_at && node.completed_at) score += 50;
  if ((node.agent_turn_count ?? 0) > 0) score += 25;
  return score;
}

function buildChildLanes(tree: IssueExecutionTreeResponse | undefined) {
  return (tree?.root?.children ?? []).map((child) => ({
    key: child.issue.id,
    label: child.issue.project?.title || child.issue.title || child.issue.identifier || "子任务",
    issue: child.issue,
  }));
}

export interface RunReviewEventRowData {
  id: string;
  kind: "message" | "trace" | "tool" | "node";
  category: string;
  timestampMs: number;
  timeLabel: string;
  taskId?: string;
  sourceLabel: string;
  object: string;
  title: string;
  outcome: string;
  summary: string;
  detail: string;
  metadataDetail: string;
  durationMs: number;
  tokenTotal: number;
  severity: "normal" | "warning" | "error";
  rawSourceLabel?: string;
  rawPayload?: unknown;
  linkedRawPayloads?: Array<{ label: string; payload: unknown }>;
}

export function buildRunReviewEventRows(
  tree: IssueExecutionTreeResponse | undefined,
  timelineNodes: IssueTimelineNode[],
): RunReviewEventRowData[] {
  const nodes = flattenExecutionNodes(tree);
  const rows: RunReviewEventRowData[] = [];
  const hasSourceFetchTrace = nodes.some((node) => (node.trace_events ?? []).some(isSourceFetchTrace));
  for (const node of nodes) {
    const messageByKey = new Map((node.task_messages ?? []).map((message) => [toolMessageKey(message.task_id, message.seq), message]));
    const coveredToolMessageKeys = new Set<string>();
    for (const chain of node.tool_call_chains ?? []) {
      if (chain.task_id && chain.use_seq) coveredToolMessageKeys.add(toolMessageKey(chain.task_id, chain.use_seq));
      if (chain.task_id && chain.result_seq) coveredToolMessageKeys.add(toolMessageKey(chain.task_id, chain.result_seq));
    }
    for (const message of node.task_messages ?? []) {
      if ((message.type === "tool_use" || message.type === "tool_result") && coveredToolMessageKeys.has(toolMessageKey(message.task_id, message.seq))) {
        continue;
      }
      rows.push(message.type === "tool_use" || message.type === "tool_result"
        ? runReviewToolMessageEvent(message)
        : runReviewMessageEvent(message));
    }
    for (const event of node.trace_events ?? []) {
      rows.push(runReviewTraceEvent(event));
    }
    for (const chain of node.tool_call_chains ?? []) {
      const linkedMessages = chain.task_id
        ? [
            chain.use_seq ? messageByKey.get(toolMessageKey(chain.task_id, chain.use_seq)) : undefined,
            chain.result_seq ? messageByKey.get(toolMessageKey(chain.task_id, chain.result_seq)) : undefined,
          ].filter(Boolean) as TaskMessagePayload[]
        : [];
      rows.push(runReviewToolEvent(chain, linkedMessages));
    }
  }
  for (const node of timelineNodes) {
    if (node.node_type === "agent_task" || node.node_type === "tool_call" || node.node_type === "status_change") continue;
    if (node.node_type === "source_fetch" && hasSourceFetchTrace) continue;
    rows.push(runReviewTimelineNodeEvent(node));
  }
  return rows
    .toSorted((a, b) => {
      if (a.timestampMs !== b.timestampMs) return b.timestampMs - a.timestampMs;
      return a.id.localeCompare(b.id);
    });
}

export function buildRunReviewNodeCsv(
  issue: Issue,
  summary: IssueExecutionTreeResponse["issue_summary"] | undefined,
  agentRows: ReturnType<typeof buildAgentNodeRows>,
  childLanes: ReturnType<typeof buildChildLanes>,
): string {
  const headers = [
    "row_type",
    "issue_id",
    "issue_identifier",
    "issue_title",
    "total_duration_ms",
    "total_token",
    "total_thinking_rounds",
    "node_key",
    "node_label",
    "node_run_count",
    "node_status",
    "node_agent",
    "node_started_at",
    "node_completed_at",
    "node_duration_ms",
    "node_input_tokens",
    "node_output_tokens",
    "node_cache_read_tokens",
    "node_cache_write_tokens",
    "node_token_total",
    "node_thinking_rounds",
    "artifact_count",
    "artifacts",
  ];
  const totalToken = (summary?.total_input_tokens ?? 0) +
    (summary?.total_output_tokens ?? 0) +
    (summary?.total_cache_read_tokens ?? 0) +
    (summary?.total_cache_write_tokens ?? 0);
  const rows: Array<Array<string | number>> = [[
    "summary",
    issue.id,
    issue.identifier,
    issue.title,
    summary?.total_duration_ms ?? 0,
    totalToken,
    summary?.agent_turn_count ?? 0,
    "",
    "",
    "",
    summary?.acceptance_status ?? "",
    "",
    "",
    "",
    "",
    "",
    "",
    "",
    "",
    "",
    "",
    "",
    "",
  ]];

  for (const row of agentRows) {
    const node = row.node;
    rows.push([
      "agent_node",
      issue.id,
      issue.identifier,
      issue.title,
      summary?.total_duration_ms ?? 0,
      totalToken,
      summary?.agent_turn_count ?? 0,
      row.key,
      row.label,
      row.runCount ?? 1,
      node.status,
      node.agent_name ?? row.key,
      node.started_at ?? "",
      node.completed_at ?? "",
      node.duration_ms ?? 0,
      node.input_tokens ?? 0,
      node.output_tokens ?? 0,
      node.cache_read_tokens ?? 0,
      node.cache_write_tokens ?? 0,
      nodeTokenTotal(node),
      node.agent_turn_count ?? 0,
      node.artifacts?.length ?? 0,
      formatArtifactsForCsv(node.artifacts ?? []),
    ]);
  }

  for (const lane of childLanes) {
    rows.push([
      "child_issue",
      issue.id,
      issue.identifier,
      issue.title,
      summary?.total_duration_ms ?? 0,
      totalToken,
      summary?.agent_turn_count ?? 0,
      lane.key,
      lane.label,
      lane.issue?.status ?? "",
      "",
      "",
      "",
      "",
      "",
      "",
      "",
      "",
      "",
      "",
      "",
      "",
    ]);
  }

  return toCsv([headers, ...rows]);
}

export function buildRunReviewRawEventsCsv(eventRows: RunReviewEventRowData[]): string {
  const headers = [
    "id",
    "kind",
    "category",
    "time",
    "timestamp_ms",
    "task_id",
    "source",
    "object",
    "title",
    "outcome",
    "severity",
    "duration_ms",
    "token_total",
    "summary",
    "detail",
    "metadata_detail",
    "raw_source",
    "raw_json",
    "linked_raw_json",
  ];
  return toCsv([
    headers,
    ...eventRows.map((event) => [
      event.id,
      event.kind,
      event.category,
      event.timeLabel,
      event.timestampMs,
      event.taskId ?? "",
      event.sourceLabel,
      event.object,
      event.title,
      event.outcome,
      event.severity,
      event.durationMs,
      event.tokenTotal,
      event.summary,
      event.detail,
      event.metadataDetail,
      event.rawSourceLabel ?? "",
      event.rawPayload === undefined ? "" : formatJSON(event.rawPayload),
      event.linkedRawPayloads?.length ? formatJSON(event.linkedRawPayloads) : "",
    ]),
  ]);
}

function toolMessageKey(taskId: string, seq: number) {
  return `${taskId}:${seq}`;
}

function formatArtifactsForCsv(artifacts: AgentTaskArtifact[]) {
  return artifacts.map((artifact) => {
    const href = artifact.markdown_url || artifact.download_url;
    return href ? `${artifact.filename} <${href}>` : artifact.filename;
  }).join("\n");
}

function flattenExecutionNodes(tree: IssueExecutionTreeResponse | undefined): IssueExecutionNode[] {
  if (!tree) return [];
  const result: IssueExecutionNode[] = [];
  const walk = (node: IssueExecutionNode) => {
    result.push(node);
    for (const child of node.children ?? []) walk(child);
  };
  walk(tree.root);
  return result;
}

export function buildAgentNodeRows(nodes: IssueTimelineNode[]) {
  const grouped = new Map<string, { key: string; label: string; node: IssueTimelineNode; runCount: number; taskIds: string[] }>();
  for (const [index, node] of nodes.filter((item) => item.node_type === "agent_task").entries()) {
    const rawTaskId = node.node_id.replace(/^task:/, "");
    const agentName = node.agent_name || rawTaskId || `agent-node-${index + 1}`;
    const key = node.agent_id || normalizeSopStageName(agentName) || agentName || rawTaskId || `agent-node-${index + 1}`;
    const label = sopStageDisplayName(agentName) || node.summary || agentName;
    const existing = grouped.get(key);
    if (!existing) {
      grouped.set(key, { key, label, node: { ...node }, runCount: 1, taskIds: rawTaskId ? [rawTaskId] : [] });
      continue;
    }
    existing.node = mergeTimelineNode(existing.node, node);
    existing.runCount += 1;
    if (rawTaskId) existing.taskIds.push(rawTaskId);
  }
  return [...grouped.values()];
}

function mergeTimelineNode(left: IssueTimelineNode, right: IssueTimelineNode): IssueTimelineNode {
  const leftStart = parseTimeMs(left.started_at);
  const rightStart = parseTimeMs(right.started_at);
  const leftCompleted = parseTimeMs(left.completed_at);
  const rightCompleted = parseTimeMs(right.completed_at);
  return {
    ...left,
    status: mergeNodeStatus(left.status, right.status),
    started_at: earliestTime(left.started_at, right.started_at),
    completed_at: latestTime(left.completed_at, right.completed_at),
    duration_ms: (left.duration_ms ?? 0) + (right.duration_ms ?? 0),
    input_tokens: (left.input_tokens ?? 0) + (right.input_tokens ?? 0),
    output_tokens: (left.output_tokens ?? 0) + (right.output_tokens ?? 0),
    cache_read_tokens: (left.cache_read_tokens ?? 0) + (right.cache_read_tokens ?? 0),
    cache_write_tokens: (left.cache_write_tokens ?? 0) + (right.cache_write_tokens ?? 0),
    message_count: (left.message_count ?? 0) + (right.message_count ?? 0),
    agent_turn_count: (left.agent_turn_count ?? 0) + (right.agent_turn_count ?? 0),
    trace_event_count: (left.trace_event_count ?? 0) + (right.trace_event_count ?? 0),
    usage_unavailable_trace: left.usage_unavailable_trace || right.usage_unavailable_trace,
    artifacts: [...(left.artifacts ?? []), ...(right.artifacts ?? [])],
    evidence_refs: [...(left.evidence_refs ?? []), ...(right.evidence_refs ?? [])],
    summary: rightCompleted !== null && (leftCompleted === null || rightCompleted >= leftCompleted)
      ? right.summary
      : left.summary,
    node_id: left.node_id,
    node_type: left.node_type,
    agent_name: left.agent_name || right.agent_name,
    agent_id: left.agent_id || right.agent_id,
    // Keep a wall-clock hint for charts/tooltips when individual runs overlap.
    duration_ms_wall_clock: leftStart !== null || rightStart !== null || leftCompleted !== null || rightCompleted !== null
      ? Math.max((latestNumber(leftCompleted, rightCompleted) ?? 0) - (earliestNumber(leftStart, rightStart) ?? 0), 0)
      : undefined,
  } as IssueTimelineNode;
}

function mergeNodeStatus(left: string, right: string) {
  if (left === "failed" || right === "failed" || left === "blocked" || right === "blocked") return "failed";
  if (isActiveStatus(left) || isActiveStatus(right)) return "running";
  if (left === "completed" && right === "completed") return "completed";
  return right || left;
}

function earliestTime(left: string | undefined, right: string | undefined) {
  const leftMs = parseTimeMs(left);
  const rightMs = parseTimeMs(right);
  const ms = earliestNumber(leftMs, rightMs);
  return ms === null ? left || right || "" : new Date(ms).toISOString();
}

function latestTime(left: string | undefined, right: string | undefined) {
  const leftMs = parseTimeMs(left);
  const rightMs = parseTimeMs(right);
  const ms = latestNumber(leftMs, rightMs);
  return ms === null ? left || right || "" : new Date(ms).toISOString();
}

function earliestNumber(...values: Array<number | null>) {
  const present = values.filter((value): value is number => value !== null);
  return present.length ? Math.min(...present) : null;
}

function latestNumber(...values: Array<number | null>) {
  const present = values.filter((value): value is number => value !== null);
  return present.length ? Math.max(...present) : null;
}

function flattenExecutionTasks(tree: IssueExecutionTreeResponse | undefined): AgentTask[] {
  return flattenExecutionNodes(tree).flatMap((node) => node.tasks ?? []);
}

function runReviewMessageEvent(message: TaskMessagePayload): RunReviewEventRowData {
  const detailParts = [
    message.content ? `content:\n${message.content}` : "",
    message.input ? `input:\n${formatJSON(message.input)}` : "",
    message.output ? `output:\n${message.output}` : "",
  ].filter(Boolean);
  const summary = firstNonEmpty(
    taskMessageText(message),
    message.input ? formatJSON(message.input) : "",
    "任务消息未记录正文",
  );
  const failure = message.type === "error" || hasFailureSignal(summary);
  return {
    id: `message:${message.task_id}:${message.seq}`,
    kind: "message",
    category: taskMessageKindLabel(message.type),
    timestampMs: parseTimeMs(message.created_at) ?? 0,
    timeLabel: formatEventTime(message.created_at),
    taskId: message.task_id,
    sourceLabel: "模型输出",
    object: `消息 #${message.seq}`,
    title: taskMessageKindLabel(message.type),
    outcome: message.type === "error" ? "异常" : "已记录",
    summary: conciseEventSummary(summary, failure),
    detail: detailParts.join("\n\n"),
    metadataDetail: "",
    durationMs: 0,
    tokenTotal: 0,
    severity: failure ? "error" : "normal",
    rawSourceLabel: "task_message",
    rawPayload: message,
  };
}

function runReviewToolMessageEvent(message: TaskMessagePayload): RunReviewEventRowData {
  const semantic = semanticToolAction(message.tool, message.input, message.output);
  const detailParts = [
    message.tool ? `raw_tool: ${message.tool}` : "",
    message.input ? `input:\n${formatJSON(message.input)}` : "",
    message.output ? `output:\n${message.output}` : "",
  ].filter(Boolean);
  return {
    id: `message:${message.task_id}:${message.seq}`,
    kind: "tool",
    category: semantic.category,
    timestampMs: parseTimeMs(message.created_at) ?? 0,
    timeLabel: formatEventTime(message.created_at),
    taskId: message.task_id,
    sourceLabel: semantic.sourceLabel,
    object: semantic.object,
    title: semantic.title,
    outcome: semantic.outcome,
    summary: semantic.summary,
    detail: detailParts.join("\n\n"),
    metadataDetail: "",
    durationMs: 0,
    tokenTotal: 0,
    severity: semantic.severity,
    rawSourceLabel: "task_message",
    rawPayload: message,
  };
}

function isSourceFetchTrace(event: TaskTraceEvent): boolean {
  const type = event.event_type.toLowerCase();
  return type.includes("source") || type.includes("fetch");
}

function runReviewTraceEvent(event: TaskTraceEvent): RunReviewEventRowData {
  const tokenTotal = event.input_tokens + event.output_tokens + event.cache_read_tokens + event.cache_write_tokens;
  const durationMs = event.run_ms ?? event.duration_ms ?? event.total_ms ?? event.queue_wait_ms ?? 0;
  const failure = [event.failure_reason, event.error_type].filter(Boolean).join(" · ");
  const metadata = event.metadata ?? {};
  const isUserInput = event.event_type === "user_input.received";
  const userInputKind = stringFromUnknown(metadata.input_kind);
  const userInputSummary = stringFromUnknown(metadata.summary);
  const userInputSnapshot = stringFromUnknown(metadata.content_snapshot);
  const sourceLabel = isUserInput
    ? [event.source, userInputKind].filter(Boolean).join(" / ") || event.event_type
    : [event.source, event.provider, event.model].filter(Boolean).join(" / ") || event.event_type;
  return {
    id: `trace:${event.id}`,
    kind: "trace",
    category: isUserInput ? "输入" : "Trace",
    timestampMs: parseTimeMs(event.created_at) ?? 0,
    timeLabel: formatEventTime(event.created_at),
    taskId: event.task_id,
    sourceLabel,
    object: isUserInput ? userInputKind || event.event_type : event.event_type,
    title: isUserInput ? "用户原始输入" : event.event_name || traceEventStageLabel(event.event_type),
    outcome: failure || event.event_type === "task.failed" ? "异常" : statusLabel(event.status),
    summary: isUserInput ? conciseEventSummary(userInputSummary, false) : failure ? conciseEventSummary(failure, true) : "",
    detail: isUserInput && userInputSnapshot ? `原文快照：\n${userInputSnapshot}` : "",
    metadataDetail: Object.keys(metadata).length > 0 ? `metadata:\n${formatJSON(metadata)}` : "",
    durationMs,
    tokenTotal,
    severity: event.event_type === "task.failed" || Boolean(failure) ? "error" : "normal",
    rawSourceLabel: "task_trace_event",
    rawPayload: event,
  };
}

function runReviewToolEvent(chain: PromptEvaluationToolCallChain, linkedMessages: TaskMessagePayload[] = []): RunReviewEventRowData {
  const semantic = semanticToolAction(chain.tool, chain.input, chain.output);
  const detailParts = [
    chain.tool ? `raw_tool: ${chain.tool}` : "",
    chain.input ? `input:\n${formatJSON(chain.input)}` : "",
    chain.output ? `output:\n${chain.output}` : "",
    chain.failure_reason ? `failure:\n${chain.failure_reason}` : "",
  ].filter(Boolean);
  return {
    id: `tool:${chain.task_id || "unknown-task"}:${chain.id}`,
    kind: "tool",
    category: semantic.category,
    timestampMs: parseTimeMs(chain.completed_at) ?? parseTimeMs(chain.created_at) ?? 0,
    timeLabel: formatEventTime(chain.completed_at || chain.created_at),
    taskId: chain.task_id,
    sourceLabel: semantic.sourceLabel,
    object: semantic.object,
    title: semantic.title,
    outcome: semantic.outcome,
    summary: conciseEventSummary(firstNonEmpty(chain.failure_reason, semantic.summary), chain.failure_signal),
    detail: detailParts.join("\n\n"),
    metadataDetail: "",
    durationMs: chain.duration_ms ?? 0,
    tokenTotal: 0,
    severity: chain.failure_signal ? "error" : chain.status === "缺少结果" || chain.status === "孤立结果" ? "warning" : semantic.severity,
    rawSourceLabel: "tool_call_chain",
    rawPayload: chain,
    linkedRawPayloads: linkedMessages.map((message) => ({
      label: `关联 task_message #${message.seq} ${taskMessageKindLabel(message.type)}`,
      payload: message,
    })),
  };
}

interface SemanticToolAction {
  category: string;
  sourceLabel: string;
  object: string;
  title: string;
  outcome: string;
  summary: string;
  severity: RunReviewEventRowData["severity"];
}

function semanticToolAction(tool: string | undefined, input: Record<string, unknown> | undefined, output: string | undefined): SemanticToolAction {
  const command = stringFromUnknown(input?.command);
  if (command) return semanticCommandAction(command, output);

  const path = firstNonEmpty(
    stringFromUnknown(input?.file_path),
    stringFromUnknown(input?.path),
    stringFromUnknown(input?.target_file),
  );
  if (path) {
    const action = semanticFileToolAction(tool);
    return {
      category: action.category,
      sourceLabel: action.sourceLabel,
      object: shortPath(path),
      title: `${action.titlePrefix}：${shortPath(path)}`,
      outcome: "已记录",
      summary: "",
      severity: "normal",
    };
  }

  const query = firstNonEmpty(stringFromUnknown(input?.query), stringFromUnknown(input?.pattern));
  if (query) {
    return {
      category: "搜索",
      sourceLabel: "搜索",
      object: truncateText(query, 96),
      title: `搜索：${truncateText(query, 96)}`,
      outcome: "已记录",
      summary: "",
      severity: "normal",
    };
  }

  const outputSignal = output ? outputOutcome(output) : null;
  if (tool === "patch_apply") {
    return {
      category: "修改",
      sourceLabel: "代码修改",
      object: "",
      title: "应用代码修改",
      outcome: outputSignal?.outcome ?? "已记录",
      summary: outputSignal?.summary ?? "",
      severity: outputSignal?.severity ?? "normal",
    };
  }

  const fallback = readableToolName(tool);
  return {
    category: fallback === "工具调用" ? "工具" : fallback,
    sourceLabel: fallback,
    object: "",
    title: fallback,
    outcome: outputSignal?.outcome ?? "已记录",
    summary: outputSignal?.summary ?? "",
    severity: outputSignal?.severity ?? "normal",
  };
}

function semanticCommandAction(command: string, output: string | undefined): SemanticToolAction {
  const normalized = command.trim();
  const segment = meaningfulCommandSegment(normalized);
  const executable = commandExecutable(segment);

  if (executable === "rg" || executable === "grep" || executable === "find" || isGitSubcommand(segment, "grep")) {
    return {
      category: "搜索",
      sourceLabel: "代码搜索",
      object: searchQueryFromCommand(segment) || truncateText(segment, 96),
      title: `搜索代码：${searchQueryFromCommand(segment) || truncateText(segment, 96)}`,
      ...semanticOutputState(output),
    };
  }

  if (["sed", "cat", "nl", "ls", "head", "tail"].includes(executable) || isGitReadCommand(segment)) {
    return {
      category: "查看",
      sourceLabel: "读取上下文",
      object: readTargetFromCommand(segment) || truncateText(segment, 96),
      title: `${readCommandTitlePrefix(segment, executable)}：${readTargetFromCommand(segment) || truncateText(segment, 96)}`,
      ...semanticOutputState(output),
    };
  }

  if (isPnpmTypecheck(segment)) {
    return {
      category: "验证",
      sourceLabel: "类型检查",
      object: pnpmFilterFromCommand(segment) || "TypeScript",
      title: `运行类型检查：${pnpmFilterFromCommand(segment) || "TypeScript"}`,
      ...semanticOutputState(output),
    };
  }

  if (isPnpmTest(segment)) {
    return {
      category: "验证",
      sourceLabel: "前端测试",
      object: testTargetFromCommand(segment) || pnpmFilterFromCommand(segment) || "Vitest",
      title: `运行前端单测：${testTargetFromCommand(segment) || pnpmFilterFromCommand(segment) || "Vitest"}`,
      ...semanticOutputState(output),
    };
  }

  if (isGoTest(segment)) {
    const target = goTestTargetFromCommand(segment);
    return {
      category: "验证",
      sourceLabel: "后端测试",
      object: target,
      title: `运行后端单测：${target}`,
      ...semanticOutputState(output),
    };
  }

  if (isBuildCommand(segment)) {
    return {
      category: "构建",
      sourceLabel: "构建",
      object: truncateText(segment, 96),
      title: `运行构建：${truncateText(segment, 96)}`,
      ...semanticOutputState(output),
    };
  }

  if (executable === "curl" || executable === "http" || executable === "wget") {
    return {
      category: "接口",
      sourceLabel: "接口检查",
      object: httpTargetFromCommand(segment) || truncateText(segment, 96),
      title: `检查接口：${httpTargetFromCommand(segment) || truncateText(segment, 96)}`,
      ...semanticOutputState(output),
    };
  }

  if (executable === "make" && (segment.includes("dev") || segment.includes("start") || segment.includes("server"))) {
    return {
      category: "服务",
      sourceLabel: "运行服务",
      object: truncateText(segment, 96),
      title: `运行服务：${truncateText(segment, 96)}`,
      ...semanticOutputState(output),
    };
  }

  if (executable === "make" && (segment.includes("check") || segment.includes("verify") || segment.includes("test"))) {
    return {
      category: "验证",
      sourceLabel: "验证",
      object: truncateText(segment, 96),
      title: `运行验证：${truncateText(segment, 96)}`,
      ...semanticOutputState(output),
    };
  }

  return {
    category: "命令",
    sourceLabel: "命令执行",
    object: truncateText(segment || normalized, 96),
    title: `执行命令：${truncateText(segment || normalized, 96)}`,
    ...semanticOutputState(output),
  };
}

function semanticOutputState(output: string | undefined): Pick<SemanticToolAction, "outcome" | "summary" | "severity"> {
  if (!output) return { outcome: "已记录", summary: "", severity: "normal" };
  return outputOutcome(output);
}

function outputOutcome(output: string): Pick<SemanticToolAction, "outcome" | "summary" | "severity"> {
  if (toolOutputHasSuccessfulExitCode(output)) {
    return { outcome: "已返回", summary: conciseEventSummary(summarizeToolOutput(output), false), severity: "normal" };
  }
  const errorLine = extractErrorLine(output);
  if (errorLine) {
    return { outcome: "异常线索", summary: conciseEventSummary(errorLine, true), severity: "error" };
  }
  return { outcome: "已返回", summary: conciseEventSummary(summarizeToolOutput(output), false), severity: "normal" };
}

function toolOutputHasSuccessfulExitCode(output: string) {
  return /\bExit Code:\s*0\b/i.test(output) || /\bexit\s+(?:status|code)\s*[:=]?\s*0\b/i.test(output);
}

function extractErrorLine(output: string) {
  const patterns = [
    /\bError:\s*.+/i,
    /\bFAIL\b.+/i,
    /\bfailed\b.+/i,
    /\bpanic:\s*.+/i,
    /\bFATAL\b.+/i,
    /\bHTTP\s+[45]\d\d\b.*/i,
    /\bexit status\s+\d+.*/i,
    /\bprovider timeout\b.*/i,
    /\btimeout\b.*/i,
  ];
  const lines = output.split("\n").map((line) => line.trim()).filter(Boolean);
  for (const pattern of patterns) {
    const line = lines.find((item) => pattern.test(item));
    if (line) return line;
  }
  return "";
}

function conciseEventSummary(value: string, isFailure: boolean) {
  const text = truncateText(value, 220);
  if (!text) return "";
  return isFailure ? `异常摘要：${text}` : text;
}

function meaningfulCommandSegment(command: string) {
  const segments = command
    .split(/\s+(?:&&|\|\|)\s+|;\s*/g)
    .map((item) => item.trim())
    .filter(Boolean);
  const candidate = segments.find((segment) => {
    const executable = commandExecutable(segment);
    return executable !== "cd" && executable !== "export" && executable !== "";
  });
  return candidate ?? command.trim();
}

function commandExecutable(command: string) {
  const tokens = shellWords(command);
  const executable = tokens.find((token) => {
    if (!token) return false;
    if (/^[A-Z_][A-Z0-9_]*=/.test(token)) return false;
    return true;
  });
  return stripCommandPath(executable ?? "").toLowerCase();
}

function shellWords(command: string) {
  return command.match(/"[^"]*"|'[^']*'|\S+/g)?.map((token) => token.replace(/^["']|["']$/g, "")) ?? [];
}

function stripCommandPath(value: string) {
  const parts = value.split("/");
  return parts[parts.length - 1] ?? value;
}

function isGitSubcommand(command: string, subcommand: string) {
  const tokens = shellWords(command);
  const gitIndex = tokens.findIndex((token) => stripCommandPath(token).toLowerCase() === "git");
  return gitIndex >= 0 && tokens[gitIndex + 1] === subcommand;
}

function isGitReadCommand(command: string) {
  const tokens = shellWords(command);
  const gitIndex = tokens.findIndex((token) => stripCommandPath(token).toLowerCase() === "git");
  const subcommand = gitIndex >= 0 ? tokens[gitIndex + 1] : "";
  return ["show", "diff", "status", "log"].includes(subcommand ?? "");
}

function searchQueryFromCommand(command: string) {
  const tokens = shellWords(command);
  const executable = commandExecutable(command);
  if (executable === "find") return tokens.find((token) => token !== "find" && !token.startsWith("-")) ?? "";
  const startIndex = isGitSubcommand(command, "grep") ? tokens.findIndex((token) => token === "grep") + 1 : 1;
  return tokens.slice(startIndex).find((token) => !token.startsWith("-") && !token.includes("=")) ?? "";
}

function readCommandTitlePrefix(command: string, executable: string) {
  if (isGitReadCommand(command)) return "查看 Git 信息";
  if (executable === "ls") return "查看目录";
  return "查看文件";
}

function readTargetFromCommand(command: string) {
  const tokens = shellWords(command);
  const candidates = tokens.filter((token) => !token.startsWith("-") && !/^\d/.test(token) && !token.includes("="));
  const target = candidates[candidates.length - 1];
  if (!target || target === commandExecutable(command)) return "";
  return shortPath(target);
}

function isPnpmTypecheck(command: string) {
  return commandExecutable(command) === "pnpm" && /\btypecheck\b/.test(command);
}

function isPnpmTest(command: string) {
  return commandExecutable(command) === "pnpm" && (/\btest\b/.test(command) || /\bvitest\b/.test(command));
}

function isGoTest(command: string) {
  return commandExecutable(command) === "go" && /\btest\b/.test(command);
}

function isBuildCommand(command: string) {
  const executable = commandExecutable(command);
  return (executable === "pnpm" || executable === "go" || executable === "make") && /\bbuild\b/.test(command);
}

function pnpmFilterFromCommand(command: string) {
  const tokens = shellWords(command);
  const index = tokens.indexOf("--filter");
  return index >= 0 ? tokens[index + 1] ?? "" : "";
}

function testTargetFromCommand(command: string) {
  const tokens = shellWords(command);
  const runIndex = tokens.indexOf("run");
  const runTarget = runIndex >= 0 ? tokens[runIndex + 1] : undefined;
  if (runTarget) return shortPath(runTarget);
  const testIndex = tokens.indexOf("test");
  const testTarget = testIndex >= 0 ? tokens[testIndex + 1] : undefined;
  if (testTarget) return shortPath(testTarget);
  return "";
}

function goTestTargetFromCommand(command: string) {
  const tokens = shellWords(command);
  const runIndex = tokens.indexOf("-run");
  const runTarget = runIndex >= 0 ? tokens[runIndex + 1] : undefined;
  if (runTarget) return runTarget;
  const testIndex = tokens.indexOf("test");
  const testTarget = testIndex >= 0 ? tokens[testIndex + 1] : undefined;
  if (testTarget) return testTarget;
  return "go test";
}

function httpTargetFromCommand(command: string) {
  return shellWords(command).find((token) => /^https?:\/\//.test(token) || token.startsWith("/api/")) ?? "";
}

function semanticFileToolAction(tool: string | undefined) {
  const normalized = (tool ?? "").toLowerCase();
  if (normalized.includes("edit") || normalized.includes("write") || normalized.includes("patch")) {
    return { category: "修改", sourceLabel: "文件修改", titlePrefix: "修改文件" };
  }
  return { category: "查看", sourceLabel: "读取上下文", titlePrefix: "查看文件" };
}

function readableToolName(tool: string | undefined) {
  const normalized = (tool ?? "").toLowerCase();
  if (!normalized) return "工具调用";
  if (normalized === "exec_command") return "命令执行";
  if (normalized === "patch_apply") return "代码修改";
  return tool ?? "工具调用";
}

function summarizeToolOutput(output: string) {
  const firstLine = output.split("\n").find((line) => line.trim().length > 0) ?? "";
  return truncateText(firstLine, 220);
}

function stringFromUnknown(value: unknown) {
  return typeof value === "string" ? value : "";
}

function shortPath(path: string) {
  const normalized = path.replace(/^["']|["']$/g, "");
  const parts = normalized.split("/").filter(Boolean);
  if (parts.length <= 2) return normalized;
  return `.../${parts.slice(-2).join("/")}`;
}

function runReviewTimelineNodeEvent(node: IssueTimelineNode): RunReviewEventRowData {
  const tokenTotal = node.input_tokens + node.output_tokens + node.cache_read_tokens + node.cache_write_tokens;
  return {
    id: `node:${node.node_id}`,
    kind: "node",
    category: timelineNodeKindLabel(node.node_type),
    timestampMs: parseTimeMs(node.completed_at) ?? parseTimeMs(node.started_at) ?? 0,
    timeLabel: formatEventTime(node.completed_at || node.started_at),
    taskId: node.node_type === "agent_task" ? node.node_id.replace(/^task:/, "") : node.root_task_id,
    sourceLabel: node.agent_name || node.node_type,
    object: node.node_type,
    title: node.summary || timelineNodeKindLabel(node.node_type),
    outcome: statusLabel(node.status),
    summary: node.usage_unavailable_trace ? "模型用量未返回" : "",
    detail: node.evidence_refs.length > 0 ? `evidence_refs:\n${formatJSON(node.evidence_refs)}` : "",
    metadataDetail: "",
    durationMs: node.duration_ms,
    tokenTotal,
    severity: node.status === "failed" || node.status === "blocked" ? "error" : "normal",
    rawSourceLabel: "timeline_node",
    rawPayload: node,
  };
}

function taskMessageKindLabel(type: TaskMessagePayload["type"]): string {
  switch (type) {
    case "tool_use":
      return "工具调用";
    case "tool_result":
      return "工具结果";
    case "thinking":
      return "思考";
    case "error":
      return "错误";
    case "text":
    default:
      return "文本";
  }
}

function taskMessageText(message: TaskMessagePayload): string {
  return message.content || message.output || "";
}

function traceEventStageLabel(eventType: string): string {
  switch (eventType) {
    case "task.queued":
      return "任务入队";
    case "task.dispatched":
      return "任务领取";
    case "task.started":
      return "任务开始";
    case "task.waiting_local_directory":
      return "等待本地目录";
    case "task.completed":
      return "任务完成";
    case "task.failed":
      return "任务失败";
    case "task.cancelled":
      return "任务取消";
    case "llm.usage_reported":
      return "模型用量";
    case "llm.usage_unavailable":
      return "模型用量未返回";
    default:
      return eventType || "未分类事件";
  }
}

function timelineNodeKindLabel(type: IssueTimelineNode["node_type"]): string {
  switch (type) {
    case "agent_task":
      return "任务";
    case "squad_step":
      return "SOP";
    case "child_issue_ref":
      return "子任务";
    case "source_fetch":
      return "来源";
    case "approval":
      return "唤醒";
    case "tool_call":
      return "工具";
    case "status_change":
      return "状态";
    case "evidence":
    default:
      return "证据";
  }
}

function formatEventTime(value: string | undefined) {
  if (!value) return "";
  const ms = parseTimeMs(value);
  if (ms === null) return "";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(new Date(ms));
}

function formatJSON(value: unknown) {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function truncateText(value: string, max: number) {
  const normalized = value.replace(/\s+/g, " ").trim();
  return normalized.length > max ? `${normalized.slice(0, max - 3)}...` : normalized;
}

function firstNonEmpty(...values: Array<string | undefined | null>) {
  return values.find((value) => typeof value === "string" && value.trim() !== "")?.trim() ?? "";
}

function hasFailureSignal(value: string) {
  const line = extractErrorLine(value);
  return Boolean(line && !isBenignFailureCounterLine(line));
}

function isBenignFailureCounterLine(value: string) {
  const lower = value.toLowerCase();
  return (
    /\b0\s+(?:chart\(s\)\s+)?(?:failed|failure|failures|errors)\b/.test(lower) ||
    /\b(?:pass|passed|success|successful|通过|成功)\b.*\b0\s+(?:failed|failure|failures|errors)\b/.test(lower) ||
    /\b0\s+(?:failed|failure|failures|errors)\b.*\b(?:pass|passed|success|successful|通过|成功)\b/.test(lower)
  );
}

function shortId(value: string) {
  return value.length > 8 ? value.slice(0, 8) : value;
}

function isActiveStatus(status: string | undefined) {
  return status === "queued" ||
    status === "dispatched" ||
    status === "waiting_local_directory" ||
    status === "running";
}

function isActiveTask(task: AgentTask) {
  return isActiveStatus(task.status);
}

function isRetryableTask(task: AgentTask) {
  return task.status === "failed" || task.status === "cancelled";
}

function latestTerminalAgentTask(tasks: AgentTask[]) {
  return tasks
    .filter((task) => task.status === "completed" || task.status === "failed" || task.status === "cancelled")
    .toSorted((a, b) => taskTimeMs(b) - taskTimeMs(a))[0];
}

function taskTimeMs(task: AgentTask) {
  return parseTimeMs(task.completed_at ?? undefined) ?? parseTimeMs(task.started_at ?? undefined) ?? parseTimeMs(task.created_at) ?? 0;
}

async function createIssueReviewDraftCase(
  issue: Issue,
  tree: IssueExecutionTreeResponse | undefined,
  stageRows: ReturnType<typeof buildStageRows>,
  childLanes: ReturnType<typeof buildChildLanes>,
) {
  if (!tree) throw new Error("执行树尚未加载，不能生成评测用例");
  const assets = await api.listPromptEvaluationAssets({ asset_type: "数据集", status: "启用" });
  let asset = assets.items.find((item) => item.name === ISSUE_REVIEW_DRAFT_DATASET_NAME);
  if (!asset) {
    asset = await api.createPromptEvaluationAsset({
      name: ISSUE_REVIEW_DRAFT_DATASET_NAME,
      description: "从运行复盘生成的 eval case draft，需人工确认后进入 active 评测集。",
      asset_type: "数据集",
      status: "启用",
      payload: {
        schema_version: 1,
        schema: "multica.training_evaluation.payload.v1",
        语义版本: "multica.training_evaluation.v1",
        cases: [],
        payload_contract: {
          source: "run-review",
          review_flow: "draft -> approved -> active",
        },
      },
    });
  }
  return api.createPromptEvaluationCase(buildIssueReviewDraftCaseRequest({
    issue,
    tree,
    stageRows,
    childLanes,
    assetId: asset.id,
    promptId: asset.prompt_id,
  }));
}

export function buildIssueReviewDraftCaseRequest({
  issue,
  tree,
  stageRows,
  childLanes,
  assetId,
  promptId,
}: {
  issue: Issue;
  tree: IssueExecutionTreeResponse;
  stageRows: ReturnType<typeof buildStageRows>;
  childLanes: ReturnType<typeof buildChildLanes>;
  assetId: string;
  promptId?: string | null;
}): CreatePromptEvaluationCaseRequest {
  const stageFacts = stageRows.map((stage) => ({
    stage: stage.label,
    status: stage.node ? stage.node.status : "missing",
    agent: stage.node?.agent_name ?? stage.key,
    duration_ms: stage.node?.duration_ms ?? 0,
    token_total: (stage.node?.input_tokens ?? 0) + (stage.node?.output_tokens ?? 0),
    turns: stage.node?.agent_turn_count ?? 0,
    artifacts: stage.node?.artifacts ?? [],
    evidence_refs: stage.node?.evidence_refs ?? [],
  }));
  const childFacts = childLanes.map((lane) => ({
    lane: lane.key,
    issue_id: lane.issue.id,
    identifier: lane.issue.identifier,
    title: lane.issue.title,
    status: lane.issue.status,
  }));
  const caseName = `${issue.identifier ? `${issue.identifier} ` : ""}${issue.title}`.trim() || `issue ${issue.id}`;
  const runSnapshot = buildIssueReviewRunSnapshot(issue, tree, stageRows, stageFacts, childFacts);
  return {
    asset_id: assetId,
    prompt_id: promptId ?? null,
    case_name: `Draft: ${caseName}`,
    variables: {
      issue_id: issue.id,
      issue_identifier: issue.identifier,
      issue_title: issue.title,
      project: issue.project?.title ?? "",
      current_status: issue.status,
      source: "run-review",
    },
    expected_contains: ["PM-项目经理", "01-需求澄清", "02-方案设计", "03-任务拆分", "04-开发", "05-验证测试", "证据"],
    input: {
      source: "run-review",
      issue: {
        id: issue.id,
        identifier: issue.identifier,
        title: issue.title,
        project: issue.project?.title ?? null,
        status: issue.status,
      },
      run_review: {
        issue_summary: tree.issue_summary ?? null,
        stage_facts: stageFacts,
        child_lanes: childFacts,
        timeline_node_count: tree.timeline_nodes?.length ?? 0,
      },
      run_snapshot: runSnapshot,
    },
    expected: {
      expected_behavior: "能复现该 issue 的 PM+01-05 执行链路，识别实际关联的跨项目子任务，并保留可追溯证据。",
      validation: "检查 DAG/子任务、阶段节点、token/耗时/轮次、实际 child lane、evidence refs 和结构化断言。",
      assertions: buildIssueReviewAssertions(issue, stageRows, childLanes, tree),
      approval_required: true,
      review_flow: "draft -> approved -> active",
    },
    tags: [
      "issue-review",
      "draft",
      "run-snapshot",
      "prompt-snapshot",
      "skill-snapshot",
      `issue:${issue.id}`,
      issue.project?.title ?? "unknown-project",
    ],
    status: "draft",
  };
}

function buildIssueReviewRunSnapshot(
  issue: Issue,
  tree: IssueExecutionTreeResponse,
  stageRows: ReturnType<typeof buildStageRows>,
  stageFacts: Array<Record<string, unknown>>,
  childFacts: Array<Record<string, unknown>>,
) {
  const nodes = flattenExecutionNodes(tree);
  const nodeByTaskId = new Map<string, IssueExecutionNode>();
  for (const node of nodes) {
    for (const task of node.tasks ?? []) nodeByTaskId.set(task.id, node);
  }
  const stages = stageRows.map((stage) => buildRunSnapshotStage(stage, nodeByTaskId));
  const promptSkillSnapshots = stages.map((stage) => buildPromptSkillSnapshot(stage));
  return {
    schema_version: 1,
    schema: "multica.run_review.snapshot.v1",
    source: "run-review",
    captured_at: new Date().toISOString(),
    issue: {
      id: issue.id,
      identifier: issue.identifier,
      title: issue.title,
      project: issue.project?.title ?? null,
      status: issue.status,
    },
    summary: tree.issue_summary ?? null,
    stage_facts: stageFacts,
    child_lanes: childFacts,
    stages,
    tool_evidence: buildRunSnapshotToolEvidence(nodes),
    prompt_skill_snapshots: promptSkillSnapshots,
    evidence_refs: buildRunSnapshotEvidenceRefs(tree, stages, promptSkillSnapshots),
    timeline_node_count: tree.timeline_nodes?.length ?? 0,
    source_limits: {
      prompt_capture: "best_effort_task_trace_snapshot",
      content_truncation_chars: 1200,
      raw_tool_output_truncation_chars: 1200,
      formal_prompt_library_write: false,
    },
    review_flow: "draft -> approved -> active",
  };
}

type StageRowForSnapshot = ReturnType<typeof buildStageRows>[number];

function buildRunSnapshotStage(stage: StageRowForSnapshot, nodeByTaskId: Map<string, IssueExecutionNode>) {
  const taskId = stage.node?.node_type === "agent_task" ? stage.node.node_id.replace(/^task:/, "") : "";
  const node = taskId ? nodeByTaskId.get(taskId) : undefined;
  const task = node?.tasks?.find((item) => item.id === taskId);
  const messages = (node?.task_messages ?? []).filter((message) => !taskId || message.task_id === taskId);
  const traceEvents = (node?.trace_events ?? []).filter((event) => !taskId || event.task_id === taskId);
  const toolChains = (node?.tool_call_chains ?? []).filter((chain) => !taskId || chain.task_id === taskId);
  const outputText = firstNonEmpty(
    stringFromUnknown(task?.result),
    task?.error ?? "",
    latestMessageText(messages),
    traceEvents.find((event) => event.failure_reason)?.failure_reason ?? "",
  );
  const inputText = firstNonEmpty(
    task?.trigger_summary ?? "",
    messages.find((message) => message.type === "tool_use")?.input ? formatJSON(messages.find((message) => message.type === "tool_use")?.input) : "",
    messages.find((message) => message.content)?.content ?? "",
  );
  const handoffText = messages
    .map((message) => taskMessageText(message))
    .find((text) => /handoff|交接|结论|阻断|验收|完成/i.test(text));
  return {
    stage: stage.label,
    stage_key: stage.key,
    status: stage.node ? stage.node.status : "missing",
    agent: stage.node?.agent_name ?? stage.key,
    task_id: taskId || null,
    agent_id: stage.node?.agent_id ?? task?.agent_id ?? null,
    runtime_id: task?.runtime_id ?? traceEvents.find((event) => event.runtime_id)?.runtime_id ?? null,
    started_at: stage.node?.started_at ?? task?.started_at ?? null,
    completed_at: stage.node?.completed_at ?? task?.completed_at ?? null,
    duration_ms: stage.node?.duration_ms ?? 0,
    token_total: (stage.node?.input_tokens ?? 0) + (stage.node?.output_tokens ?? 0),
    turns: stage.node?.agent_turn_count ?? 0,
    input_summary: truncateText(inputText, 420),
    output_summary: truncateText(outputText, 420),
    handoff_summary: truncateText(handoffText ?? "", 420),
    failure_reason: task?.error ?? traceEvents.find((event) => event.failure_reason)?.failure_reason ?? "",
    message_refs: messages.slice(0, 20).map((message) => ({ type: "task_message", task_id: message.task_id, seq: message.seq })),
    trace_refs: traceEvents.slice(0, 20).map((event) => ({ type: "trace_event", id: event.id, task_id: event.task_id })),
    tool_refs: toolChains.slice(0, 20).map((chain) => ({ type: "tool_call_chain", id: chain.id, task_id: chain.task_id })),
    artifacts: stage.node?.artifacts ?? [],
    evidence_refs: stage.node?.evidence_refs ?? [],
    prompt_capture_text: truncateText(firstNonEmpty(inputText, task?.trigger_summary ?? "", latestMessageText(messages)), 1200),
    runtime: {
      provider: firstNonEmpty(traceEvents.find((event) => event.provider)?.provider ?? ""),
      model: firstNonEmpty(traceEvents.find((event) => event.model)?.model ?? ""),
    },
  };
}

function buildRunSnapshotToolEvidence(nodes: IssueExecutionNode[]) {
  const chains = nodes.flatMap((node) => node.tool_call_chains ?? []);
  const messages = nodes.flatMap((node) => node.task_messages ?? []).filter((message) => message.type === "tool_use" || message.type === "tool_result");
  const chainRows = chains.map((chain) => {
    const semantic = semanticToolAction(chain.tool, chain.input, chain.output);
    return {
      id: chain.id,
      task_id: chain.task_id,
      source: "tool_call_chain",
      tool: chain.tool,
      category: semantic.category,
      action: semantic.title,
      object: semantic.object,
      status: chain.status,
      outcome: semantic.outcome,
      failure_signal: chain.failure_signal || semantic.severity === "error",
      failure_reason: firstNonEmpty(chain.failure_reason, extractErrorLine(chain.output ?? "")),
      input_summary: truncateText(chain.input ? formatJSON(chain.input) : "", 420),
      output_summary: truncateText(firstNonEmpty(semantic.summary, summarizeToolOutput(chain.output ?? "")), 420),
      raw_output_excerpt: truncateText(chain.output ?? "", 1200),
      duration_ms: chain.duration_ms ?? 0,
      created_at: chain.created_at,
      completed_at: chain.completed_at,
      evidence_ref: { type: "tool_call_chain", id: chain.id, task_id: chain.task_id },
    };
  });
  const chainMessageKeys = new Set<string>();
  for (const chain of chains) {
    if (chain.task_id && chain.use_seq) chainMessageKeys.add(toolMessageKey(chain.task_id, chain.use_seq));
    if (chain.task_id && chain.result_seq) chainMessageKeys.add(toolMessageKey(chain.task_id, chain.result_seq));
  }
  const orphanRows = messages
    .filter((message) => !chainMessageKeys.has(toolMessageKey(message.task_id, message.seq)))
    .map((message) => {
      const semantic = semanticToolAction(message.tool, message.input, message.output);
      return {
        id: `${message.task_id}:${message.seq}`,
        task_id: message.task_id,
        source: "task_message",
        tool: message.tool ?? "",
        category: semantic.category,
        action: semantic.title,
        object: semantic.object,
        status: message.type,
        outcome: semantic.outcome,
        failure_signal: semantic.severity === "error",
        failure_reason: extractErrorLine(message.output ?? ""),
        input_summary: truncateText(message.input ? formatJSON(message.input) : "", 420),
        output_summary: truncateText(firstNonEmpty(semantic.summary, summarizeToolOutput(message.output ?? "")), 420),
        raw_output_excerpt: truncateText(message.output ?? "", 1200),
        duration_ms: 0,
        created_at: message.created_at ?? "",
        completed_at: message.created_at ?? "",
        evidence_ref: { type: "task_message", task_id: message.task_id, seq: message.seq },
      };
    });
  return [...chainRows, ...orphanRows].slice(0, 80);
}

function buildPromptSkillSnapshot(stage: ReturnType<typeof buildRunSnapshotStage>) {
  const promptText = stage.prompt_capture_text;
  const agentName = typeof stage.agent === "string" ? stage.agent : "";
  const skillPath = sopSkillPathForAgent(agentName);
  const captureStatus = promptText ? "captured_excerpt" : skillPath ? "ref_only" : "missing";
  return {
    role: stage.stage,
    stage_key: stage.stage_key,
    task_id: stage.task_id,
    agent: stage.agent,
    source: promptText ? "task_trace" : skillPath ? "skill_ref" : "missing",
    capture_status: captureStatus,
    content_summary: truncateText(promptText, 420),
    content_excerpt: promptText,
    content_hash: promptText ? stableContentHash(promptText) : "",
    skill_path: skillPath,
    skill_hash: "",
    runtime_provider: stage.runtime.provider,
    model: stage.runtime.model,
    evidence_refs: [
      ...(stage.message_refs as Array<Record<string, unknown>>).slice(0, 5),
      ...(stage.trace_refs as Array<Record<string, unknown>>).slice(0, 5),
    ],
  };
}

function sopSkillPathForAgent(agentName: string) {
  const normalized = normalizeSopStageName(agentName);
  const stage = SOP_STAGE_DEFINITIONS.find((item) => item.names.some((name) => normalizeSopStageName(name) === normalized));
  if (!stage || stage.key === "pm") return "";
  const canonical = stage.names.find((name) => /^[0-5]{2}-[a-z-]+$/.test(name)) ?? "";
  return canonical ? `.codebuddy/skills/${canonical}/SKILL.md` : "";
}

function buildRunSnapshotEvidenceRefs(
  tree: IssueExecutionTreeResponse,
  stages: Array<ReturnType<typeof buildRunSnapshotStage>>,
  promptSkillSnapshots: Array<ReturnType<typeof buildPromptSkillSnapshot>>,
) {
  const timelineRefs = (tree.timeline_nodes ?? []).slice(0, 80).map((node) => ({ type: "timeline_node", id: node.node_id, node_type: node.node_type }));
  const stageRefs = stages.flatMap((stage) => [
    ...(stage.message_refs as Array<Record<string, unknown>>),
    ...(stage.trace_refs as Array<Record<string, unknown>>),
    ...(stage.tool_refs as Array<Record<string, unknown>>),
  ]);
  const promptRefs = promptSkillSnapshots.map((snapshot) => ({
    type: "prompt_skill_snapshot",
    role: snapshot.role,
    task_id: snapshot.task_id,
    content_hash: snapshot.content_hash,
    capture_status: snapshot.capture_status,
  }));
  return [...stageRefs, ...timelineRefs, ...promptRefs].slice(0, 200);
}

function buildIssueReviewAssertions(
  issue: Issue,
  stageRows: ReturnType<typeof buildStageRows>,
  childLanes: ReturnType<typeof buildChildLanes>,
  tree: IssueExecutionTreeResponse,
) {
  const requiredStages = STAGES.map((stage) => stage.label);
  const missingStages = stageRows.filter((stage) => !stage.node).map((stage) => stage.label);
  const terminalStatus = issue.status === "done" ? "done" : tree.issue_summary?.acceptance_status ?? issue.status;
  return {
    required_stages: requiredStages,
    missing_required_stages: missingStages,
    disallow_missing_required_stage: true,
    must_identify_child_issues: childLanes.length > 0,
    expected_child_issue_count: childLanes.length,
    must_keep_evidence: true,
    must_report_blocker_on_failure: true,
    must_update_done_when_verified: stageRows.some((stage) => stage.key === "05" && stage.node?.status === "completed"),
    expected_terminal_status: terminalStatus,
    require_prompt_skill_snapshot_refs: true,
    require_tool_evidence_on_tool_use: true,
  };
}

function latestMessageText(messages: TaskMessagePayload[]) {
  const message = messages
    .filter((item) => item.type === "text" || item.type === "error")
    .toSorted((a, b) => (b.seq ?? 0) - (a.seq ?? 0))[0];
  return taskMessageText(message ?? ({} as TaskMessagePayload));
}

function stableContentHash(value: string) {
  let hash = 0x811c9dc5;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return `fnv1a:${(hash >>> 0).toString(16).padStart(8, "0")}`;
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

function formatDuration(ms: number) {
  if (!ms || ms <= 0) return "0m";
  const totalSeconds = Math.round(ms / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes <= 0) return `${seconds}s`;
  return seconds > 0 ? `${minutes}m ${seconds}s` : `${minutes}m`;
}

function formatNumber(value: number) {
  return new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 0 }).format(value || 0);
}

function formatPercent(value: number | null) {
  if (value === null) return "暂无";
  return new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1, style: "percent" }).format(value);
}

export function cacheReuseRate(cacheReadTokens: number, cacheWriteTokens: number) {
  const denominator = cacheReadTokens + cacheWriteTokens;
  if (denominator <= 0) return null;
  return cacheReadTokens / denominator;
}

function nodeTokenTotal(node: IssueTimelineNode | undefined) {
  if (!node) return 0;
  return node.input_tokens + node.output_tokens + node.cache_read_tokens + node.cache_write_tokens;
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

function formatDateTime(value: string | number | null | undefined) {
  if (value === null || value === undefined || value === "") return "暂无";
  const ms = typeof value === "number" ? value : parseTimeMs(value);
  if (ms === null) return "暂无";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  }).format(new Date(ms));
}

function toCsv(rows: Array<Array<string | number | boolean | null | undefined>>) {
  return `${rows.map((row) => row.map(csvCell).join(",")).join("\n")}\n`;
}

function csvCell(value: string | number | boolean | null | undefined) {
  const text = value === null || value === undefined ? "" : String(value);
  return /[",\n\r]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
}

function downloadCsv(filename: string, csv: string) {
  if (typeof window === "undefined" || typeof document === "undefined") return;
  const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
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
  return filename.replace(/[^\w.\-]+/g, "_");
}

function statusLabel(status: string) {
  const labels: Record<string, string> = {
    backlog: "待规划",
    todo: "待办",
    in_progress: "进行中",
    in_review: "验收中",
    done: "已完成",
    completed: "已完成",
    failed: "失败",
    blocked: "阻塞",
    cancelled: "已取消",
    queued: "排队",
    running: "运行中",
  };
  return labels[status] ?? status;
}
