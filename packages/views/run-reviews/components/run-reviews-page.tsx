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
import { resolvePublicFileUrlWithBase } from "@multica/core/workspace/avatar-url";
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
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { Dialog, DialogContent, DialogTitle } from "@multica/ui/components/ui/dialog";
import { PageHeader } from "../../layout/page-header";
import { AppLink, useNavigation } from "../../navigation";
import { TranscriptButton } from "../../common/task-transcript";
import { SOP_STAGE_DEFINITIONS, normalizeSopStageName, sopStageDisplayName } from "../../common/sop-stage-labels";

const STAGES = SOP_STAGE_DEFINITIONS;

const ISSUE_REVIEW_DRAFT_DATASET_NAME = "Issue 复盘评测 Draft";
const RUN_REVIEW_MESSAGE_REFRESH_DEBOUNCE_MS = 1_200;
const RUN_REVIEW_MESSAGE_REFRESH_MAX_WAIT_MS = 4_000;
const RUN_REVIEW_LIVE_DURATION_TICK_MS = 1_000;
const TIMELINE_SEGMENT_TEXT_MIN_WIDTH_PERCENT = 8;

type XlsxCellValue = string | number | boolean | null | undefined;

export interface XlsxHyperlink {
  row: number;
  col: number;
  target: string;
  tooltip?: string;
}

export interface XlsxSheetSpec {
  name: string;
  rows: XlsxCellValue[][];
  hyperlinks?: XlsxHyperlink[];
  columnWidths?: number[];
}

export function buildRunReviewOptimizerHref(evaluationView: (view: string) => string, issueId: string): string {
  return `${evaluationView("runs")}?issue=${encodeURIComponent(issueId)}`;
}

export function runReviewTotalDurationMs(summary: IssueTimelineSummary | undefined): number {
  if (!summary) return 0;
  const agentExecution = summary.agent_execution_duration_ms;
  if (agentExecution != null) {
    return agentExecution + (summary.human_confirmation_duration_ms ?? 0);
  }
  return summary.total_duration_ms ?? 0;
}

export function buildRunReviewDurationTooltipRows(summary: IssueTimelineSummary | undefined): Array<[string, string]> {
  const agentExecution = summary?.agent_execution_duration_ms ?? summary?.total_duration_ms ?? 0;
  const humanConfirmation = summary?.human_confirmation_duration_ms;
  return [
    ["Agent 执行耗时", formatDuration(agentExecution)],
    ["人工/等待耗时", humanConfirmation == null ? "未记录" : formatDuration(humanConfirmation)],
  ];
}

export function buildRunReviewDurationSummary(summary: IssueTimelineSummary | undefined): string {
  const agentExecution = summary?.agent_execution_duration_ms ?? summary?.total_duration_ms ?? 0;
  const humanConfirmation = summary?.human_confirmation_duration_ms;
  return `Agent 执行 ${formatDuration(agentExecution)} · 人工/等待 ${humanConfirmation == null ? "未记录" : formatDuration(humanConfirmation)}`;
}

export function buildRunReviewTokenSummary(summary: IssueTimelineSummary | undefined): string {
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

export function buildRunReviewTurnSummary(agentRows: ReturnType<typeof buildAgentNodeRows>): string {
  const summary = agentRows
    .map((row) => `${agentNodeDisplayLabel(row)} ${formatNumber(row.node.agent_turn_count ?? 0)}`)
    .join(" · ");
  return summary || "暂无执行轮次";
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
  const visibleEventRows = useMemo(
    () => filterRunReviewEventRows(eventRows, eventQuery),
    [eventQuery, eventRows],
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
          subtitle="按节点聚合事件；组标题查看完整 transcript，点击事件查看当前 raw。"
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
              {eventRows.length > 0 ? "没有匹配的事件。" : "暂无事件。真实任务开始后会回写 trace、用量和证据。"}
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

type TraceFlowKind = "input" | "agent" | "human" | "wait" | "system" | "error";

interface TraceUnitSegment {
  key: string;
  flowId: string;
  label: string;
  startMs: number | null;
  endMs: number | null;
  durationMs: number;
}

interface TraceUnit {
  id: string;
  key: string;
  label: string;
  kind: "Agent" | "人工" | "系统";
  description: string;
  durationMs: number;
  tokenTotal: number;
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
  turns: number;
  transcriptCount: number;
  colorClassName: string;
  segments: TraceUnitSegment[];
}

interface TraceFlowItem {
  id: string;
  unitId: string;
  kind: TraceFlowKind;
  title: string;
  summary: string;
  status: string;
  timeLabel: string;
  durationMs: number;
  taskId?: string;
  nodeId: string;
  agentName?: string;
  rawCount: number;
}

interface TraceTranscriptRow {
  id: string;
  type: string;
  title: string;
  timeLabel: string;
  content: string;
  payload: unknown;
}

interface TraceTranscriptBundle {
  rawCount: number;
  inputs: TraceTranscriptRow[];
  events: TraceTranscriptRow[];
  outputs: TraceTranscriptRow[];
  raw: TraceTranscriptRow[];
  searchText: string;
}

interface TraceRawExportRow {
  flowTitle: string;
  section: string;
  type: string;
  timeLabel: string;
  title: string;
  content: string;
  payload: unknown;
}

interface TraceViewModel {
  units: TraceUnit[];
  flowItems: TraceFlowItem[];
  transcriptBundles: Record<string, TraceTranscriptBundle>;
  rawRows: TraceRawExportRow[];
  sourceCounts: {
    timelineNodes: number;
    tasks: number;
    taskMessages: number;
    toolCallChains: number;
    traceEvents: number;
  };
}

export function RunReviewTraceView({
  model,
  nodeXlsxSheets,
  rawXlsxSheets,
  issue,
}: {
  model: TraceViewModel;
  nodeXlsxSheets: XlsxSheetSpec[];
  rawXlsxSheets: XlsxSheetSpec[];
  issue: Issue;
}) {
  const [selectedUnit, setSelectedUnit] = useState("all");
  const [selectedFlow, setSelectedFlow] = useState(model.flowItems[0]?.id ?? "");
  const [query, setQuery] = useState("");
  const visibleFlowItems = useMemo(() => {
    const q = query.trim().toLowerCase();
    return model.flowItems.filter((item) => {
      if (selectedUnit !== "all" && item.unitId !== selectedUnit) return false;
      if (!q) return true;
      const bundle = model.transcriptBundles[item.id];
      return [
        item.title,
        item.summary,
        item.agentName,
        item.status,
        item.taskId,
        traceUnitLabel(model, item.unitId),
        bundle?.searchText,
      ].join(" ").toLowerCase().includes(q);
    });
  }, [model, query, selectedUnit]);
  const activeFlow = visibleFlowItems.find((item) => item.id === selectedFlow) ?? visibleFlowItems[0] ?? model.flowItems[0];
  const activeBundle = activeFlow ? model.transcriptBundles[activeFlow.id] : undefined;
  const timelineRange = traceTimelineRange(model.units);

  return (
    <section className="rounded-md border bg-card" data-testid="run-review-trace-view">
      <div className="flex flex-col gap-3 border-b px-4 py-3 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0">
          <div className="text-sm font-semibold">Trace 视图</div>
          <div className="mt-0.5 flex flex-wrap gap-2 text-xs text-muted-foreground">
            <span>Timeline {formatNumber(model.sourceCounts.timelineNodes)}</span>
            <span>Tasks {formatNumber(model.sourceCounts.tasks)}</span>
            <span>Messages {formatNumber(model.sourceCounts.taskMessages)}</span>
            <span>Tool chains {formatNumber(model.sourceCounts.toolCallChains)}</span>
            <span>Trace events {formatNumber(model.sourceCounts.traceEvents)}</span>
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <ExportButton
            label="导出节点信息"
            onClick={() => downloadXlsx(`run-review-trace-nodes-${issue.identifier || issue.id}.xlsx`, nodeXlsxSheets)}
          />
          <ExportButton
            label="导出 raw 信息"
            onClick={() => downloadXlsx(`run-review-trace-raw-${issue.identifier || issue.id}.xlsx`, rawXlsxSheets)}
          />
        </div>
      </div>
      <div className="grid min-h-[42rem] grid-cols-1 lg:grid-cols-[19rem_minmax(0,1fr)_24rem]">
        <aside className="min-h-0 border-b lg:border-r lg:border-b-0">
          <div className="max-h-[28rem] space-y-2 overflow-y-auto p-3 lg:max-h-[calc(100vh-18rem)]">
            <TraceUnitCard
              active={selectedUnit === "all"}
              title="全部"
              kind="总览"
              description={`${model.flowItems.length} 个主流程节点，${formatNumber(model.rawRows.length)} 条 transcript/raw。`}
              onClick={() => {
                setSelectedUnit("all");
                setSelectedFlow(model.flowItems[0]?.id ?? "");
              }}
            />
            {model.units.map((unit) => (
              <TraceUnitCard
                key={unit.id}
                active={selectedUnit === unit.id}
                title={unit.label}
                kind={unit.kind}
                description={unit.description}
                metrics={[
                  ["耗时", formatDuration(unit.durationMs)],
                  ["Token", formatCompactToken(unit.tokenTotal)],
                  ["执行轮次", unit.kind === "Agent" ? formatNumber(unit.turns) : "-"],
                ]}
                onClick={() => {
                  setSelectedUnit(unit.id);
                  setSelectedFlow(model.flowItems.find((item) => item.unitId === unit.id)?.id ?? model.flowItems[0]?.id ?? "");
                }}
              />
            ))}
          </div>
        </aside>
        <main className="min-h-0 border-b lg:border-r lg:border-b-0">
          <div className="border-b p-3">
            <input
              className="w-full rounded-md border bg-background px-3 py-2 text-sm outline-none focus:border-ring"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="搜索流程、Agent、工具、输出或 transcript"
            />
          </div>
          <div className="max-h-[34rem] overflow-y-auto p-3 lg:max-h-[calc(100vh-22rem)]">
            <TraceTimelineCard
              units={model.units}
              range={timelineRange}
              selectedUnit={selectedUnit}
              onSelect={(unitId, flowId) => {
                setSelectedUnit(unitId);
                setSelectedFlow(flowId);
              }}
            />
            <div className="mt-3 space-y-2">
              {visibleFlowItems.length > 0 ? visibleFlowItems.map((item) => (
                <TraceFlowCard
                  key={item.id}
                  item={item}
                  active={activeFlow?.id === item.id}
                  unitLabel={traceUnitLabel(model, item.unitId)}
                  onClick={() => setSelectedFlow(item.id)}
                />
              )) : (
                <div className="rounded-md border border-dashed bg-muted/20 px-3 py-5 text-sm text-muted-foreground">
                  没有匹配的 Trace 节点。
                </div>
              )}
            </div>
          </div>
        </main>
        <aside className="min-h-0">
          <div className="max-h-[38rem] overflow-y-auto p-3 lg:max-h-[calc(100vh-18rem)]">
            {activeFlow && activeBundle ? (
              <TraceTranscriptDetail item={activeFlow} bundle={activeBundle} unitLabel={traceUnitLabel(model, activeFlow.unitId)} />
            ) : (
              <div className="rounded-md border border-dashed bg-muted/20 px-3 py-5 text-sm text-muted-foreground">
                选择一个流程节点查看 transcript。
              </div>
            )}
          </div>
        </aside>
      </div>
    </section>
  );
}

function TraceUnitCard({
  active,
  title,
  kind,
  description,
  metrics,
  onClick,
}: {
  active: boolean;
  title: string;
  kind: string;
  description: string;
  metrics?: Array<[string, string]>;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className={cn("w-full rounded-md border bg-background p-3 text-left text-sm hover:bg-accent/50", active && "border-info bg-info/5")}
      onClick={onClick}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="min-w-0 truncate font-medium">{title}</span>
        <span className="shrink-0 rounded border px-1.5 py-0.5 text-[11px] text-muted-foreground">{kind}</span>
      </div>
      <div className="mt-1 line-clamp-2 text-xs leading-5 text-muted-foreground">{description}</div>
      {metrics?.length ? (
        <div className="mt-2 grid grid-cols-3 gap-2 text-[11px] text-muted-foreground">
          {metrics.map(([label, value]) => (
            <span key={label} className="min-w-0">
              {label}
              <b className="mt-0.5 block truncate text-xs text-foreground">{value}</b>
            </span>
          ))}
        </div>
      ) : null}
    </button>
  );
}

function TraceTimelineCard({
  units,
  range,
  selectedUnit,
  onSelect,
}: {
  units: TraceUnit[];
  range: { min: number; max: number; span: number };
  selectedUnit: string;
  onSelect: (unitId: string, flowId: string) => void;
}) {
  if (!units.length) return null;
  const ticks = [range.min, range.min + range.span / 2, range.max].map((value) => formatTimeTick(value));
  return (
    <div className="rounded-md border bg-background p-3">
      <div className="grid grid-cols-[7rem_minmax(0,1fr)] gap-3 text-[11px] text-muted-foreground">
        <span />
        <div className="grid grid-cols-3">
          {ticks.map((tick, index) => (
            <span key={`${tick}-${index}`} className={cn(index === 1 && "text-center", index === 2 && "text-right")}>{tick}</span>
          ))}
        </div>
      </div>
      <div className="mt-2 space-y-1.5">
        {units.map((unit) => (
          <div key={unit.id} className="grid grid-cols-[7rem_minmax(0,1fr)] items-center gap-3">
            <div className={cn("truncate text-xs", selectedUnit === unit.id ? "font-semibold text-foreground" : "text-muted-foreground")}>{unit.label}</div>
            <div className="relative h-7 overflow-hidden rounded bg-muted/30">
              {unit.segments.map((segment) => {
                if (segment.startMs === null || segment.endMs === null) return null;
                return (
                  <button
                    key={segment.key}
                    type="button"
                    className={cn("absolute top-1 bottom-1 rounded", unit.colorClassName)}
                    style={traceSegmentStyle(segment, range)}
                    title={`${segment.label} · ${formatDuration(segment.durationMs)}`}
                    aria-label={`${segment.label} ${formatDuration(segment.durationMs)}`}
                    onClick={() => onSelect(unit.id, segment.flowId)}
                  />
                );
              })}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function TraceFlowCard({
  item,
  active,
  unitLabel,
  onClick,
}: {
  item: TraceFlowItem;
  active: boolean;
  unitLabel: string;
  onClick: () => void;
}) {
  const tone = traceFlowTone(item.kind, item.status);
  return (
    <button
      type="button"
      className={cn("w-full rounded-md border bg-background p-3 text-left text-sm hover:bg-accent/50", active && "border-info bg-info/5")}
      onClick={onClick}
      data-testid="run-review-trace-flow-card"
    >
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <span className={cn("rounded border px-1.5 py-0.5 text-[11px]", tone.chip)}>{traceFlowKindLabel(item.kind)}</span>
        <span className="min-w-[10rem] flex-1 truncate font-medium">{item.title}</span>
        <span className="shrink-0 text-xs text-muted-foreground">{item.timeLabel}</span>
      </div>
      <div className="mt-1 line-clamp-2 text-xs leading-5 text-muted-foreground">{item.summary}</div>
      <div className="mt-2 flex flex-wrap gap-1.5 text-[11px] text-muted-foreground">
        <span className="rounded border px-1.5 py-0.5">{unitLabel}</span>
        <span className="rounded border px-1.5 py-0.5">耗时 {formatDuration(item.durationMs)}</span>
        <span className="rounded border px-1.5 py-0.5">Transcript {formatNumber(item.rawCount)}</span>
      </div>
    </button>
  );
}

function TraceTranscriptDetail({ item, bundle, unitLabel }: { item: TraceFlowItem; bundle: TraceTranscriptBundle; unitLabel: string }) {
  return (
    <div>
      <div className="rounded-md border bg-background p-3">
        <div className="text-xs text-muted-foreground">{traceFlowKindLabel(item.kind)}</div>
        <h3 className="mt-1 text-sm font-semibold leading-5">{item.title}</h3>
        <div className="mt-2 grid grid-cols-2 gap-2 text-xs text-muted-foreground">
          <NodeFact label="节点" value={unitLabel} />
          <NodeFact label="时间" value={item.timeLabel || "暂无"} />
          <NodeFact label="状态" value={statusLabel(item.status)} />
          <NodeFact label="Raw" value={formatNumber(bundle.rawCount)} />
        </div>
      </div>
      <TraceTranscriptSection title="输入/提示词" rows={bundle.inputs} />
      <TraceTranscriptSection title="执行过程" rows={bundle.events} />
      <TraceTranscriptSection title="输出/结果" rows={bundle.outputs} />
      <TraceTranscriptSection title="原始 JSON" rows={bundle.raw} />
    </div>
  );
}

function TraceTranscriptSection({ title, rows }: { title: string; rows: TraceTranscriptRow[] }) {
  return (
    <section className="mt-3 overflow-hidden rounded-md border bg-background">
      <div className="flex items-center justify-between border-b bg-muted/30 px-3 py-2 text-xs font-medium">
        <span>{title}</span>
        <span className="text-muted-foreground">{formatNumber(rows.length)}</span>
      </div>
      <div className="divide-y">
        {rows.length > 0 ? rows.map((row) => (
          <article key={row.id} className="text-xs">
            <div className="p-3">
              <div className="flex min-w-0 items-center gap-2">
                <span className="shrink-0 rounded border px-1.5 py-0.5 text-[11px] text-muted-foreground">{row.type}</span>
                <span className="min-w-0 flex-1 truncate font-medium">{row.title}</span>
                <span className="shrink-0 text-muted-foreground">{row.timeLabel}</span>
              </div>
              {row.content ? <pre className="mt-2 max-h-52 overflow-auto whitespace-pre-wrap break-words rounded bg-muted/30 p-2 font-mono text-[11px] leading-5 text-foreground">{row.content}</pre> : null}
            </div>
            <details className="border-t bg-muted/10">
              <summary className="cursor-pointer px-3 py-2 text-muted-foreground">展开 JSON</summary>
              <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words px-3 pb-3 font-mono text-[11px] leading-5">{formatJSON(row.payload)}</pre>
            </details>
          </article>
        )) : (
          <div className="px-3 py-4 text-xs text-muted-foreground">无记录</div>
        )}
      </div>
    </section>
  );
}

export function buildTraceViewModel(
  tree: IssueExecutionTreeResponse | undefined,
  timelineNodes: IssueTimelineNode[],
  tasks: AgentTask[],
): TraceViewModel {
  const executionNodes = flattenExecutionNodes(tree);
  const allTasks = dedupeBy([...flattenExecutionTasks(tree), ...tasks], (task) => task.id);
  const taskById = new Map(allTasks.map((task) => [task.id, task]));
  const taskMessages = dedupeBy(executionNodes.flatMap((node) => node.task_messages ?? []), (message) => `${message.task_id}:${message.seq}:${message.type}`);
  const toolCallChains = dedupeBy(executionNodes.flatMap((node) => node.tool_call_chains ?? []), (chain) => `${chain.task_id}:${chain.id}`);
  const traceEvents = dedupeBy(executionNodes.flatMap((node) => node.trace_events ?? []), (event) => event.id || `${event.task_id}:${event.event_type}:${event.created_at}`);
  const messagesByTask = groupBy(taskMessages, (message) => message.task_id);
  const chainsByTask = groupBy(toolCallChains, (chain) => chain.task_id ?? "");
  const tracesByTask = groupBy(traceEvents, (event) => event.task_id);
  const timelineByTask = groupBy(timelineNodes, (node) => traceTaskIdFromTimelineNode(node));
  const primaryNodes = tracePrimaryNodes(timelineNodes);
  const unitContext: TraceBuildContext = {
    taskById,
    messagesByTask,
    chainsByTask,
    tracesByTask,
    timelineByTask,
  };
  const units = buildTraceUnits(timelineNodes, primaryNodes, unitContext);
  const unitByAgentKey = new Map(units.map((unit) => [unit.key, unit.id]));
  const flowItems = primaryNodes.map((node, index) => buildTraceFlowItem(node, index, unitByAgentKey, taskById, unitContext));
  const transcriptBundles: Record<string, TraceTranscriptBundle> = {};
  for (const item of flowItems) {
    transcriptBundles[item.id] = buildTraceTranscriptBundle(item, taskById, unitContext);
  }
  const rawRows = flowItems.flatMap((item) => flattenTraceRawRows(item, transcriptBundles[item.id] as TraceTranscriptBundle));
  return {
    units,
    flowItems,
    transcriptBundles,
    rawRows,
    sourceCounts: {
      timelineNodes: timelineNodes.length,
      tasks: allTasks.length,
      taskMessages: taskMessages.length,
      toolCallChains: toolCallChains.length,
      traceEvents: traceEvents.length,
    },
  };
}

interface TraceBuildContext {
  taskById: Map<string, AgentTask>;
  messagesByTask: Map<string, TaskMessagePayload[]>;
  chainsByTask: Map<string, PromptEvaluationToolCallChain[]>;
  tracesByTask: Map<string, TaskTraceEvent[]>;
  timelineByTask: Map<string, IssueTimelineNode[]>;
}

function tracePrimaryNodes(nodes: IssueTimelineNode[]) {
  const primaryTypes = new Set<IssueTimelineNode["node_type"]>(["source_fetch", "agent_task", "human_confirmation", "child_issue_ref", "dispatch_wait", "approval"]);
  return nodes
    .filter((node) => primaryTypes.has(node.node_type))
    .toSorted((left, right) => (parseTimeMs(left.started_at || left.completed_at) ?? 0) - (parseTimeMs(right.started_at || right.completed_at) ?? 0));
}

function buildTraceUnits(nodes: IssueTimelineNode[], primaryNodes: IssueTimelineNode[], context: TraceBuildContext): TraceUnit[] {
  const agentGroups = new Map<string, IssueTimelineNode[]>();
  for (const node of nodes.filter((item) => item.node_type === "agent_task")) {
    const key = traceAgentGroupKey(node);
    agentGroups.set(key, [...(agentGroups.get(key) ?? []), node]);
  }
  const units: TraceUnit[] = [...agentGroups.entries()].map(([key, groupNodes], index) => {
    const sorted = groupNodes.toSorted((left, right) => (parseTimeMs(left.started_at || left.completed_at) ?? 0) - (parseTimeMs(right.started_at || right.completed_at) ?? 0));
    const label = sorted[0]?.agent_name || key;
    const totals = traceNodeTotals(sorted);
    const transcriptCount = sorted.reduce((total, node) => total + traceTranscriptRawCount(traceTaskIdFromTimelineNode(node), context), 0);
    return {
      id: `trace-unit-${sanitizeKey(key)}`,
      key,
      label: sorted.length > 1 ? `${label} (${sorted.length} 次)` : label,
      kind: "Agent",
      description: `${formatNumber(sorted.length)} 次运行；${formatNumber(transcriptCount)} 条 transcript/raw；${formatNumber(totals.turns)} 次执行轮次。`,
      ...totals,
      transcriptCount,
      colorClassName: traceColorClass(index),
      segments: sorted.map((node) => traceUnitSegment(node)),
    };
  });
  const humanNodes = primaryNodes.filter((node) => node.node_type === "human_confirmation" || node.node_type === "approval");
  if (humanNodes.length) {
    const totals = traceNodeTotals(humanNodes);
    units.push({
      id: "trace-unit-human",
      key: "human",
      label: humanNodes.length > 1 ? `人工确认 (${humanNodes.length} 次)` : "人工确认",
      kind: "人工",
      description: `${formatNumber(humanNodes.length)} 次人工确认或审批。`,
      ...totals,
      transcriptCount: humanNodes.length,
      colorClassName: "bg-amber-500",
      segments: humanNodes.map((node) => traceUnitSegment(node)),
    });
  }
  const systemNodes = primaryNodes.filter((node) => !["agent_task", "human_confirmation", "approval"].includes(node.node_type));
  if (systemNodes.length) {
    const totals = traceNodeTotals(systemNodes);
    units.unshift({
      id: "trace-unit-system",
      key: "system",
      label: "系统事件",
      kind: "系统",
      description: `${formatNumber(systemNodes.length)} 个输入、等待或子任务节点。`,
      ...totals,
      transcriptCount: systemNodes.length,
      colorClassName: "bg-slate-500",
      segments: systemNodes.map((node) => traceUnitSegment(node)),
    });
  }
  return units.toSorted((left, right) => (left.segments[0]?.startMs ?? 0) - (right.segments[0]?.startMs ?? 0));
}

function traceUnitSegment(node: IssueTimelineNode): TraceUnitSegment {
  const timing = timelineTiming(node);
  return {
    key: node.node_id,
    flowId: traceFlowId(node),
    label: traceFlowTitle(node, undefined, 0),
    startMs: timing.startMs,
    endMs: timing.endMs,
    durationMs: timing.durationMs,
  };
}

function buildTraceFlowItem(
  node: IssueTimelineNode,
  index: number,
  unitByAgentKey: Map<string, string>,
  taskById: Map<string, AgentTask>,
  context: TraceBuildContext,
): TraceFlowItem {
  const taskId = traceTaskIdFromTimelineNode(node);
  const task = taskId ? taskById.get(taskId) : undefined;
  const kind = traceFlowKind(node);
  const unitId = node.node_type === "agent_task"
    ? unitByAgentKey.get(traceAgentGroupKey(node)) ?? "trace-unit-system"
    : node.node_type === "human_confirmation" || node.node_type === "approval"
      ? "trace-unit-human"
      : "trace-unit-system";
  const messageCount = taskId ? context.messagesByTask.get(taskId)?.length ?? 0 : 0;
  const toolCount = taskId ? context.chainsByTask.get(taskId)?.length ?? 0 : 0;
  const traceCount = taskId ? context.tracesByTask.get(taskId)?.length ?? 0 : 0;
  const rawCount = taskId ? traceTranscriptRawCount(taskId, context) : 1;
  return {
    id: traceFlowId(node),
    unitId,
    kind,
    title: traceFlowTitle(node, task, index),
    summary: node.node_type === "agent_task"
      ? `${node.agent_name || "Agent"} 执行 ${formatDuration(node.duration_ms)}；${formatNumber(messageCount)} 条 message，${formatNumber(toolCount)} 条工具调用链，${formatNumber(traceCount)} 条 trace event。${truncateText(firstNonEmpty(traceTaskOutput(task), node.summary), 140)}`
      : `${timelineNodeKindLabel(node.node_type)}；${formatNumber(rawCount)} 条 raw。${truncateText(node.summary || node.status || "", 140)}`,
    status: node.status || task?.status || "",
    timeLabel: formatEventTime(node.started_at || node.completed_at),
    durationMs: node.duration_ms ?? 0,
    taskId,
    nodeId: node.node_id,
    agentName: node.agent_name,
    rawCount,
  };
}

function buildTraceTranscriptBundle(
  item: TraceFlowItem,
  taskById: Map<string, AgentTask>,
  context: TraceBuildContext,
): TraceTranscriptBundle {
  if (!item.taskId) {
    const node = [...context.timelineByTask.values()].flat().find((candidate) => candidate.node_id === item.nodeId);
    const raw = node ? [traceTranscriptRow("timeline_node", node.node_id, formatEventTime(node.started_at || node.completed_at), timelineNodeKindLabel(node.node_type), node.summary || node.status || "", node)] : [];
    return traceBundle([], [], [], raw);
  }
  const task = taskById.get(item.taskId);
  const messages = (context.messagesByTask.get(item.taskId) ?? []).toSorted((left, right) => (left.seq ?? 0) - (right.seq ?? 0));
  const chains = (context.chainsByTask.get(item.taskId) ?? []).toSorted((left, right) => (left.use_seq ?? 0) - (right.use_seq ?? 0));
  const traces = (context.tracesByTask.get(item.taskId) ?? []).toSorted((left, right) => (parseTimeMs(left.created_at) ?? 0) - (parseTimeMs(right.created_at) ?? 0));
  const timeline = (context.timelineByTask.get(item.taskId) ?? []).toSorted((left, right) => (parseTimeMs(left.started_at || left.completed_at) ?? 0) - (parseTimeMs(right.started_at || right.completed_at) ?? 0));
  const inputs: TraceTranscriptRow[] = [];
  if (task?.trigger_summary) {
    inputs.push(traceTranscriptRow("trigger", `trigger:${item.taskId}`, formatEventTime(task.created_at), "触发输入", task.trigger_summary, { task_id: item.taskId, trigger_summary: task.trigger_summary }));
  }
  for (const event of traces.filter((trace) => trace.event_type === "user_input.received")) {
    inputs.push(traceTranscriptRow("trace", event.id, formatEventTime(event.created_at), event.event_name || event.event_type, traceEventText(event), event));
  }
  const events = buildTraceTranscriptEvents(messages, chains);
  const outputs: TraceTranscriptRow[] = [];
  if (traceTaskOutput(task)) {
    outputs.push(traceTranscriptRow("task_result", `result:${item.taskId}`, formatEventTime(task?.completed_at ?? undefined), "任务结果", traceTaskOutput(task), task?.result));
  }
  if (task?.error) {
    outputs.push(traceTranscriptRow("task_error", `error:${item.taskId}`, formatEventTime(task.completed_at ?? undefined), "任务错误", task.error, task));
  }
  const raw = [
    ...(task ? [traceTranscriptRow("agent_task", `task:${task.id}`, formatEventTime(task.started_at ?? task.created_at), "agent_task_queue", traceTaskOutput(task) || task.status, task)] : []),
    ...traces.map((trace) => traceTranscriptRow("task_trace_event", trace.id, formatEventTime(trace.created_at), trace.event_name || trace.event_type, traceEventText(trace), trace)),
    ...chains.map((chain) => traceTranscriptRow("tool_call_chain", `${chain.task_id}:${chain.id}`, formatEventTime(chain.completed_at || chain.created_at), chain.tool || chain.id, traceToolChainText(chain), chain)),
    ...timeline.map((node) => traceTranscriptRow("timeline_node", node.node_id, formatEventTime(node.started_at || node.completed_at), timelineNodeKindLabel(node.node_type), node.summary || node.status || "", node)),
  ];
  return traceBundle(inputs, events, outputs, raw);
}

function buildTraceTranscriptEvents(messages: TaskMessagePayload[], chains: PromptEvaluationToolCallChain[]): TraceTranscriptRow[] {
  const chainByUseSeq = new Map(chains.filter((chain) => chain.use_seq).map((chain) => [chain.use_seq, chain]));
  const chainByResultSeq = new Map(chains.filter((chain) => chain.result_seq).map((chain) => [chain.result_seq, chain]));
  return messages.map((message) => {
    const linkedChain = chainByUseSeq.get(message.seq) ?? chainByResultSeq.get(message.seq);
    const title = message.type === "tool_use"
      ? `调用工具 ${message.tool || linkedChain?.tool || ""}`.trim()
      : message.type === "tool_result"
        ? `工具结果 ${message.tool || linkedChain?.tool || ""}`.trim()
        : taskMessageKindLabel(message.type);
    const content = message.type === "tool_use"
      ? formatJSON(message.input || linkedChain?.input || {})
      : message.type === "tool_result"
        ? firstNonEmpty(message.output, linkedChain?.output, message.content)
        : firstNonEmpty(message.content, message.output, message.input ? formatJSON(message.input) : "");
    return traceTranscriptRow(`message:${message.type}`, `${message.task_id}:${message.seq}:${message.type}`, formatEventTime(message.created_at), title, content, { message, linked_tool_call_chain: linkedChain ?? null });
  });
}

function traceBundle(inputs: TraceTranscriptRow[], events: TraceTranscriptRow[], outputs: TraceTranscriptRow[], raw: TraceTranscriptRow[]): TraceTranscriptBundle {
  const rows = [...inputs, ...events, ...outputs, ...raw];
  return {
    rawCount: rows.length,
    inputs,
    events,
    outputs,
    raw,
    searchText: rows.map((row) => `${row.type} ${row.title} ${row.content}`).join(" "),
  };
}

function traceTranscriptRow(type: string, id: string, timeLabel: string, title: string, content: string, payload: unknown): TraceTranscriptRow {
  return { id: `${type}:${id}`, type, title: title || type, timeLabel, content: content || "", payload };
}

function flattenTraceRawRows(item: TraceFlowItem, bundle: TraceTranscriptBundle): TraceRawExportRow[] {
  return [
    ...bundle.inputs.map((row) => traceRawExportRow(item, "输入/提示词", row)),
    ...bundle.events.map((row) => traceRawExportRow(item, "执行过程", row)),
    ...bundle.outputs.map((row) => traceRawExportRow(item, "输出/结果", row)),
    ...bundle.raw.map((row) => traceRawExportRow(item, "原始 JSON", row)),
  ];
}

function traceRawExportRow(item: TraceFlowItem, section: string, row: TraceTranscriptRow): TraceRawExportRow {
  return {
    flowTitle: item.title,
    section,
    type: row.type,
    timeLabel: row.timeLabel,
    title: row.title,
    content: row.content,
    payload: row.payload,
  };
}

export function buildTraceNodeXlsxSheets(_issue: Issue, summary: IssueTimelineSummary | undefined, model: TraceViewModel): XlsxSheetSpec[] {
  const totalToken = (summary?.total_input_tokens ?? 0) +
    (summary?.total_output_tokens ?? 0) +
    (summary?.total_cache_read_tokens ?? 0) +
    (summary?.total_cache_write_tokens ?? 0);
  const rows: XlsxCellValue[][] = [
    ["总耗时", "Agent 执行耗时", "人工/等待耗时", "总 Token", "输入 Token", "输出 Token", "缓存读 Token", "缓存写 Token", "缓存命中率", "执行轮次"],
    [
      formatDuration(runReviewTotalDurationMs(summary)),
      formatDuration(summary?.agent_execution_duration_ms ?? summary?.total_duration_ms ?? 0),
      summary?.human_confirmation_duration_ms == null ? "未记录" : formatDuration(summary.human_confirmation_duration_ms),
      formatNumber(totalToken),
      formatNumber(summary?.total_input_tokens ?? 0),
      formatNumber(summary?.total_output_tokens ?? 0),
      formatNumber(summary?.total_cache_read_tokens ?? 0),
      formatNumber(summary?.total_cache_write_tokens ?? 0),
      formatPercent(cacheReuseRate(summary?.total_cache_read_tokens ?? 0, summary?.total_cache_write_tokens ?? 0)),
      formatNumber(summary?.agent_turn_count ?? 0),
    ],
    [],
    ["节点", "类型", "耗时", "Token 合计", "输入 Token", "输出 Token", "缓存读 Token", "缓存写 Token", "缓存命中率", "执行轮次", "Transcript", "说明"],
    ...model.units.map((unit): XlsxCellValue[] => [
      unit.label,
      unit.kind,
      formatDuration(unit.durationMs),
      formatNumber(unit.tokenTotal),
      formatNumber(unit.inputTokens),
      formatNumber(unit.outputTokens),
      formatNumber(unit.cacheReadTokens),
      formatNumber(unit.cacheWriteTokens),
      formatPercent(cacheReuseRate(unit.cacheReadTokens, unit.cacheWriteTokens)),
      unit.kind === "Agent" ? formatNumber(unit.turns) : "-",
      formatNumber(unit.transcriptCount),
      unit.description,
    ]),
  ];
  return [{ name: "Trace 节点信息", rows, columnWidths: [24, 16, 12, 14, 14, 14, 14, 14, 14, 12, 12, 48] }];
}

export function buildTraceRawXlsxSheets(model: TraceViewModel): XlsxSheetSpec[] {
  return [{
    name: "Trace RAW 信息",
    rows: [
      ["归属流程", "分区", "类型", "时间", "标题", "内容", "Payload"],
      ...model.rawRows.map((row): XlsxCellValue[] => [
        row.flowTitle,
        row.section,
        row.type,
        row.timeLabel,
        row.title,
        row.content,
        formatJSON(row.payload),
      ]),
    ],
    columnWidths: [32, 16, 18, 18, 30, 64, 72],
  }];
}

function traceNodeTotals(nodes: IssueTimelineNode[]) {
  return nodes.reduce((acc, node) => ({
    durationMs: acc.durationMs + (node.duration_ms ?? 0),
    tokenTotal: acc.tokenTotal + nodeTokenTotal(node),
    inputTokens: acc.inputTokens + (node.input_tokens ?? 0),
    outputTokens: acc.outputTokens + (node.output_tokens ?? 0),
    cacheReadTokens: acc.cacheReadTokens + (node.cache_read_tokens ?? 0),
    cacheWriteTokens: acc.cacheWriteTokens + (node.cache_write_tokens ?? 0),
    turns: acc.turns + (node.agent_turn_count ?? 0),
  }), { durationMs: 0, tokenTotal: 0, inputTokens: 0, outputTokens: 0, cacheReadTokens: 0, cacheWriteTokens: 0, turns: 0 });
}

function traceTaskIdFromTimelineNode(node: IssueTimelineNode | undefined) {
  if (!node) return "";
  if (node.node_type === "agent_task") return node.node_id.replace(/^task:/, "");
  if (node.parent_node_id?.startsWith("task:")) return node.parent_node_id.replace(/^task:/, "");
  return node.evidence_refs.find((ref) => ref.type === "agent_task")?.id ?? "";
}

function traceTranscriptRawCount(taskId: string, context: TraceBuildContext) {
  if (!taskId) return 0;
  return (context.taskById.has(taskId) ? 1 : 0) +
    (context.messagesByTask.get(taskId)?.length ?? 0) +
    (context.chainsByTask.get(taskId)?.length ?? 0) +
    (context.tracesByTask.get(taskId)?.length ?? 0) +
    (context.timelineByTask.get(taskId)?.length ?? 0);
}

function traceAgentGroupKey(node: IssueTimelineNode) {
  return node.agent_id || node.agent_name || traceTaskIdFromTimelineNode(node) || node.node_id;
}

function traceFlowId(node: IssueTimelineNode) {
  return `trace-flow-${sanitizeKey(node.node_id)}`;
}

function traceFlowKind(node: IssueTimelineNode): TraceFlowKind {
  if (node.status === "failed" || node.status === "blocked") return "error";
  if (node.node_type === "source_fetch") return "input";
  if (node.node_type === "agent_task") return "agent";
  if (node.node_type === "human_confirmation" || node.node_type === "approval") return "human";
  if (node.node_type === "child_issue_ref" || node.node_type === "dispatch_wait") return "wait";
  return "system";
}

function traceFlowTitle(node: IssueTimelineNode, task: AgentTask | undefined, index: number) {
  if (node.node_type === "source_fetch") return "读取输入来源";
  if (node.node_type === "human_confirmation") return firstNonEmpty(truncateText(node.summary, 72), "人工确认");
  if (node.node_type === "approval") return firstNonEmpty(truncateText(node.summary, 72), "审批");
  if (node.node_type !== "agent_task") return `${timelineNodeKindLabel(node.node_type)} · ${truncateText(node.summary || node.status || `节点 ${index + 1}`, 56)}`;
  return firstNonEmpty(firstHeading(traceTaskOutput(task)), truncateText(node.summary, 64), `${node.agent_name || "Agent"} · ${statusLabel(node.status)}`);
}

function traceTaskOutput(task: AgentTask | undefined) {
  const result = task?.result as { output?: unknown } | undefined;
  return typeof result?.output === "string" ? result.output : "";
}

function firstHeading(value: string) {
  return value
    .split("\n")
    .map((line) => line.replace(/^#+\s*/, "").replace(/\*\*/g, "").trim())
    .find(Boolean) ?? "";
}

function traceEventText(event: TaskTraceEvent) {
  return firstNonEmpty(
    event.failure_reason,
    event.error_type,
    stringFromUnknown(event.metadata?.summary),
    stringFromUnknown(event.metadata?.content_snapshot),
    event.event_name,
    event.status,
    event.event_type,
  );
}

function traceToolChainText(chain: PromptEvaluationToolCallChain) {
  return [
    chain.input ? `input:\n${formatJSON(chain.input)}` : "",
    chain.output ? `output:\n${chain.output}` : "",
    chain.failure_reason ? `failure:\n${chain.failure_reason}` : "",
  ].filter(Boolean).join("\n\n");
}

function traceUnitLabel(model: TraceViewModel, unitId: string) {
  return model.units.find((unit) => unit.id === unitId)?.label ?? "未归属";
}

function traceTimelineRange(units: TraceUnit[]) {
  const timed = units.flatMap((unit) => unit.segments).filter((segment) => segment.startMs !== null && segment.endMs !== null);
  if (!timed.length) return { min: 0, max: 1, span: 1 };
  const min = Math.min(...timed.map((segment) => segment.startMs as number));
  const max = Math.max(...timed.map((segment) => segment.endMs as number));
  return { min, max, span: Math.max(max - min, 1) };
}

function traceSegmentStyle(segment: TraceUnitSegment, range: { min: number; span: number }) {
  const start = segment.startMs ?? range.min;
  const end = segment.endMs ?? start + 1;
  const left = Math.max(0, ((start - range.min) / range.span) * 100);
  const width = Math.max(1.5, ((end - start) / range.span) * 100);
  return { left: `${left}%`, width: `${Math.min(width, 100 - left)}%` };
}

function traceFlowKindLabel(kind: TraceFlowKind) {
  const labels: Record<TraceFlowKind, string> = {
    input: "输入来源",
    agent: "Agent 执行",
    human: "人工确认",
    wait: "等待",
    system: "系统事件",
    error: "异常",
  };
  return labels[kind];
}

function traceFlowTone(kind: TraceFlowKind, status: string) {
  if (kind === "error" || status === "failed" || status === "blocked") {
    return { chip: "border-destructive/30 bg-destructive/10 text-destructive" };
  }
  if (kind === "human" || kind === "wait") {
    return { chip: "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300" };
  }
  if (kind === "agent") {
    return { chip: "border-info/30 bg-info/10 text-info" };
  }
  return { chip: "border-border bg-muted/30 text-muted-foreground" };
}

function traceColorClass(index: number) {
  const colors = ["bg-violet-600", "bg-sky-600", "bg-emerald-600", "bg-amber-600", "bg-rose-600", "bg-slate-600", "bg-teal-600", "bg-indigo-600"];
  return colors[index % colors.length] ?? "bg-slate-600";
}

function formatCompactToken(value: number) {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(2)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`;
  return formatNumber(value);
}

export function formatTokenMillions(value: number) {
  return `${((value || 0) / 1_000_000).toFixed(2)}M`;
}

function sanitizeKey(value: string) {
  return value.toLowerCase().replace(/[^a-z0-9\u4e00-\u9fa5]+/g, "-").replace(/^-|-$/g, "").slice(0, 96);
}

function dedupeBy<T>(items: T[], keyFn: (item: T) => string) {
  const map = new Map<string, T>();
  for (const item of items) {
    const key = keyFn(item);
    if (!map.has(key)) map.set(key, item);
  }
  return [...map.values()];
}

function groupBy<T>(items: T[], keyFn: (item: T) => string) {
  const map = new Map<string, T[]>();
  for (const item of items) {
    const key = keyFn(item);
    map.set(key, [...(map.get(key) ?? []), item]);
  }
  return map;
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

export function filterRunReviewEventRows(eventRows: RunReviewEventRowData[], query: string): RunReviewEventRowData[] {
  const q = query.trim().toLowerCase();
  if (!q) return eventRows;
  return eventRows.filter((event) => runReviewEventSearchText(event).toLowerCase().includes(q));
}

function runReviewEventSearchText(event: RunReviewEventRowData) {
  return [
    event.id,
    event.kind,
    event.category,
    event.outcome,
    event.title,
    event.summary,
    event.detail,
    event.metadataDetail,
    event.sourceLabel,
    event.object,
    event.taskId,
    event.rawSourceLabel,
  ].filter(Boolean).join(" ");
}

export function eventCardAccentClass(event: RunReviewEventRowData, index: number): string {
  if (event.severity === "error") return "bg-destructive";
  if (event.severity === "warning") return "bg-amber-500";
  return timelinePaletteClass(event.taskId || event.sourceLabel || event.category || event.id || String(index));
}

export function eventGroupAccentClass(group: RunReviewEventGroupData, index: number): string {
  if (group.severity === "error") return "bg-destructive";
  if (group.severity === "warning") return "bg-amber-500";
  return timelinePaletteClass(group.taskId || group.label || group.key || String(index));
}

function eventGroupOutcomeClass(group: RunReviewEventGroupData): string {
  if (group.severity === "error") return "border-destructive/30 bg-destructive/10 text-destructive";
  if (group.severity === "warning") return "border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300";
  return "border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300";
}

export function timelineNodeColorClass(node: IssueTimelineNode): string {
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

function agentNodeDisplayLabel(row: ReturnType<typeof buildAgentNodeRows>[number]) {
  return row.runCount > 1 ? `${row.label} (${row.runCount} 次)` : row.label;
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

export function artifactDownloadHref(artifact: AgentTaskArtifact, baseUrl?: string) {
  const endpoint = artifact.id
    ? `/api/attachments/${encodeURIComponent(artifact.id)}/download`
    : firstNonEmpty(artifact.download_url, artifact.markdown_url);
  const resolvedBaseUrl = baseUrl ?? (typeof api.getBaseUrl === "function" ? api.getBaseUrl() : "");
  return resolvePublicFileUrlWithBase(endpoint, resolvedBaseUrl) ?? endpoint;
}

export function artifactXlsxHyperlinkHref(artifact: AgentTaskArtifact, baseUrl?: string) {
  const resolvedBaseUrl = baseUrl ?? defaultXlsxHyperlinkBaseUrl();
  return toAbsoluteHyperlink(artifactDownloadHref(artifact, resolvedBaseUrl), resolvedBaseUrl);
}

function defaultXlsxHyperlinkBaseUrl() {
  const apiBaseUrl = typeof api.getBaseUrl === "function" ? api.getBaseUrl() : "";
  if (isAbsoluteUrl(apiBaseUrl)) return apiBaseUrl;
  if (typeof window !== "undefined" && window.location.origin) return window.location.origin;
  return apiBaseUrl;
}

function toAbsoluteHyperlink(href: string | null | undefined, baseUrl: string) {
  if (!href) return "";
  if (isAbsoluteUrl(href)) return href;
  if (!baseUrl) return href;
  try {
    return new URL(href, baseUrl).toString();
  } catch {
    return href;
  }
}

function isAbsoluteUrl(value: string | null | undefined) {
  return !!value && /^[a-z][a-z\d+.-]*:/i.test(value);
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

export interface TimelineBarSegment {
  key: string;
  label: string;
  node: IssueTimelineNode;
  status: string;
  startMs: number | null;
  endMs: number | null;
  durationMs: number;
  tokenTotal: number;
  turns: number;
  ordinal: number;
  total: number;
}

export interface TimelineBarRow {
  key: string;
  label: string;
  kind: "stage" | "child" | "human_confirmation";
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

export function buildTimelineBarRows(
  stageRows: TimelineNodeRow[],
  _childLanes: ChildLane[],
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
  const childIssueNodes = timelineNodes.filter((item) => item.node_type === "child_issue_ref");
  const childIssueSegments = childIssueNodes.map((node, index) => (
    timelineNodeSegment(node.node_id, childIssueSegmentLabel(node), node, index + 1, childIssueNodes.length)
  ));
  const childIssueBars = childIssueSegments.length > 0 ? [{
    key: "child-issue-wait",
    label: "子任务等待",
    kind: "child" as const,
    status: "completed",
    subtitle: timelineRowSubtitle("completed", childIssueSegments.length),
    segments: childIssueSegments,
    missing: false,
  }] : [];
  const humanConfirmationNodes = timelineNodes.filter((item) => item.node_type === "human_confirmation");
  const humanConfirmationSegments = humanConfirmationNodes.map((node, index) => (
    timelineNodeSegment(node.node_id, humanConfirmationSegmentLabel(node), node, index + 1, humanConfirmationNodes.length)
  ));
  const humanConfirmationBars = humanConfirmationSegments.length > 0 ? [{
    key: "human-confirmation",
    label: "人工确认",
    kind: "human_confirmation" as const,
    status: "completed",
    subtitle: timelineRowSubtitle("completed", humanConfirmationSegments.length),
    segments: humanConfirmationSegments,
    missing: false,
  }] : [];
  return [...humanConfirmationBars, ...childIssueBars, ...stageBars];
}

function humanConfirmationSegmentLabel(node: IssueTimelineNode) {
  return node.summary || "人工确认";
}

function childIssueSegmentLabel(node: IssueTimelineNode) {
  return `等待子任务完成：${node.summary || "子任务"}`;
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

export function timelineSegmentTooltipRows(row: TimelineBarRow, segment: TimelineBarSegment): Array<[string, string]> {
  const rows: Array<[string, string]> = [
    ["节点", row.label],
    ["开始", segment.startMs === null ? "未知" : formatDateTime(segment.startMs)],
    ["结束", segment.endMs === null ? "未知" : formatDateTime(segment.endMs)],
    ["耗时", formatDuration(segment.durationMs)],
  ];
  if (row.kind !== "human_confirmation" && row.kind !== "child") {
    rows.push(["Token", formatNumber(segment.tokenTotal)], ["执行轮次", formatNumber(segment.turns)]);
  }
  return rows;
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
    node,
    status: node.status,
    ...timing,
    durationMs: node.duration_ms ?? timing.durationMs,
    tokenTotal: (node.input_tokens ?? 0) + (node.output_tokens ?? 0),
    turns: node.agent_turn_count ?? 0,
    ordinal,
    total,
  };
}

export function timelineSegmentStyle(startMs: number, endMs: number, minMs: number, spanMs: number) {
  return {
    left: `${timelineSegmentLeftPercent(startMs, minMs, spanMs)}%`,
    width: `${timelineSegmentWidthPercent(startMs, endMs, spanMs)}%`,
  };
}

export function timelineSegmentLeftPercent(startMs: number, minMs: number, spanMs: number) {
  return Math.max(0, ((startMs - minMs) / Math.max(spanMs, 1)) * 100);
}

export function timelineSegmentWidthPercent(startMs: number, endMs: number, spanMs: number) {
  return Math.max(0, ((endMs - startMs) / Math.max(spanMs, 1)) * 100);
}

export function shouldShowTimelineSegmentText(widthPercent: number) {
  return widthPercent >= TIMELINE_SEGMENT_TEXT_MIN_WIDTH_PERCENT;
}

function timelineRowSubtitle(status: string, runCount: number) {
  return runCount > 1 ? `${formatNumber(runCount)} 次 · ${statusLabel(status)}` : statusLabel(status);
}

export function timelineTiming(node: IssueTimelineNode | undefined) {
  if (!node) return { startMs: null, endMs: null, durationMs: 0 };
  const start = parseTimeMs(node.started_at);
  const completed = parseTimeMs(node.completed_at);
  const duration = Math.max(node.duration_ms ?? 0, 0);
  if (start === null && completed === null) return { startMs: null, endMs: null, durationMs: duration };
  const fallbackDuration = Math.max(duration, 1);
  const startMs = start ?? Math.max((completed as number) - fallbackDuration, 0);
  const endMs = Math.max(completed ?? startMs + fallbackDuration, startMs);
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

export interface RunReviewEventGroupData {
  key: string;
  label: string;
  taskId?: string;
  events: RunReviewEventRowData[];
  startMs: number | null;
  endMs: number | null;
  timeRangeLabel: string;
  tokenTotal: number;
  severity: "normal" | "warning" | "error";
  outcome: string;
}

export function buildRunReviewEventGroups(
  eventRows: RunReviewEventRowData[],
  taskLabelById: Map<string, string> = new Map(),
): RunReviewEventGroupData[] {
  const grouped = new Map<string, { key: string; label: string; taskId?: string; events: RunReviewEventRowData[] }>();
  for (const event of eventRows) {
    const key = event.taskId
      ? `task:${event.taskId}`
      : `system:${event.rawSourceLabel || event.kind}:${event.sourceLabel || event.object || event.id}`;
    const existing = grouped.get(key);
    if (existing) {
      existing.events.push(event);
      continue;
    }
    const label = event.taskId
      ? taskLabelById.get(event.taskId) || `任务 ${shortId(event.taskId)}`
      : firstNonEmpty(event.sourceLabel, event.category, event.object, "系统事件");
    grouped.set(key, { key, label, taskId: event.taskId, events: [event] });
  }

  return [...grouped.values()]
    .map((group) => {
      const events = group.events.toSorted((left, right) => eventSortTime(left) - eventSortTime(right) || left.id.localeCompare(right.id));
      const times = events.map((event) => event.timestampMs).filter((value) => value > 0);
      const startMs = times.length ? Math.min(...times) : null;
      const endMs = times.length ? Math.max(...times) : null;
      const severity = groupSeverity(events);
      return {
        ...group,
        events,
        startMs,
        endMs,
        timeRangeLabel: formatEventGroupTimeRange(startMs, endMs),
        tokenTotal: events.reduce((total, event) => total + event.tokenTotal, 0),
        severity,
        outcome: severity === "error" ? "异常线索" : severity === "warning" ? "需关注" : "正常",
      };
    })
    .toSorted((left, right) => eventGroupSortTime(left) - eventGroupSortTime(right) || left.label.localeCompare(right.label));
}

function buildEventTaskLabelById(timelineNodes: IssueTimelineNode[]) {
  const labels = new Map<string, string>();
  for (const node of timelineNodes) {
    if (node.node_type !== "agent_task") continue;
    const taskId = node.node_id.replace(/^task:/, "");
    if (!taskId) continue;
    labels.set(taskId, firstNonEmpty(node.agent_name, node.summary, `任务 ${shortId(taskId)}`));
  }
  return labels;
}

function eventSortTime(event: RunReviewEventRowData) {
  return event.timestampMs > 0 ? event.timestampMs : Number.MAX_SAFE_INTEGER;
}

function eventGroupSortTime(group: RunReviewEventGroupData) {
  return group.startMs ?? Number.MAX_SAFE_INTEGER;
}

function groupSeverity(events: RunReviewEventRowData[]): RunReviewEventGroupData["severity"] {
  if (events.some((event) => event.severity === "error")) return "error";
  if (events.some((event) => event.severity === "warning")) return "warning";
  return "normal";
}

function formatEventGroupTimeRange(startMs: number | null, endMs: number | null) {
  if (startMs === null) return "";
  if (endMs === null || endMs === startMs) return formatDateTime(startMs);
  return `${formatDateTime(startMs)} - ${formatDateTime(endMs)}`;
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

export function buildRunReviewNodeXlsxSheets(
  _issue: Issue,
  summary: IssueExecutionTreeResponse["issue_summary"] | undefined,
  agentRows: ReturnType<typeof buildAgentNodeRows>,
  _childLanes: ReturnType<typeof buildChildLanes>,
): XlsxSheetSpec[] {
  const summaryInputTokens = summary?.total_input_tokens ?? 0;
  const summaryOutputTokens = summary?.total_output_tokens ?? 0;
  const summaryCacheReadTokens = summary?.total_cache_read_tokens ?? 0;
  const summaryCacheWriteTokens = summary?.total_cache_write_tokens ?? 0;
  const totalToken = (summary?.total_input_tokens ?? 0) +
    (summary?.total_output_tokens ?? 0) +
    (summary?.total_cache_read_tokens ?? 0) +
    (summary?.total_cache_write_tokens ?? 0);
  const agentExecutionDurationMs = summary?.agent_execution_duration_ms ?? summary?.total_duration_ms ?? 0;
  const humanConfirmationDuration = summary?.human_confirmation_duration_ms == null
    ? "未记录"
    : formatDuration(summary.human_confirmation_duration_ms);
  const rows: XlsxCellValue[][] = [
    [
      "总耗时",
      "Agent 执行耗时",
      "人工/等待耗时",
      "总 Token",
      "输入 Token",
      "输出 Token",
      "缓存读 Token",
      "缓存写 Token",
      "缓存命中率",
      "执行轮次",
    ],
    [
      formatDuration(runReviewTotalDurationMs(summary)),
      formatDuration(agentExecutionDurationMs),
      humanConfirmationDuration,
      formatNumber(totalToken),
      formatNumber(summaryInputTokens),
      formatNumber(summaryOutputTokens),
      formatNumber(summaryCacheReadTokens),
      formatNumber(summaryCacheWriteTokens),
      formatPercent(cacheReuseRate(summaryCacheReadTokens, summaryCacheWriteTokens)),
      formatNumber(summary?.agent_turn_count ?? 0),
    ],
    [],
    [
      "节点",
      "Agent",
      "开始时间",
      "结束时间",
      "执行耗时",
      "Token 合计",
      "输入 Token",
      "输出 Token",
      "缓存读 Token",
      "缓存写 Token",
      "缓存命中率",
      "执行轮次",
      "产物",
    ],
  ];
  const hyperlinks: XlsxHyperlink[] = [];
  const artifactRows: XlsxCellValue[][] = [["节点", "Agent", "产物", "链接"]];
  const artifactHyperlinks: XlsxHyperlink[] = [];

  for (const row of agentRows) {
    const node = row.node;
    const artifacts = dedupeArtifacts(node.artifacts ?? []);
    const baseCells: XlsxCellValue[] = [
      agentNodeDisplayLabel(row),
      node.agent_name ?? row.key,
      formatDateTime(node.started_at),
      formatDateTime(node.completed_at),
      formatDuration(node.duration_ms ?? 0),
      formatNumber(nodeTokenTotal(node)),
      formatNumber(node.input_tokens ?? 0),
      formatNumber(node.output_tokens ?? 0),
      formatNumber(node.cache_read_tokens ?? 0),
      formatNumber(node.cache_write_tokens ?? 0),
      formatPercent(cacheReuseRate(node.cache_read_tokens ?? 0, node.cache_write_tokens ?? 0)),
      formatNumber(node.agent_turn_count ?? 0),
    ];
    if (!artifacts.length) {
      rows.push([...baseCells, "-"]);
      continue;
    }
    const nodeRowIndex = rows.length;
    rows.push([...baseCells, artifacts.map(artifactDisplayName).join("\n")]);
    const firstArtifact = artifacts[0];
    const firstArtifactHref = firstArtifact ? artifactXlsxHyperlinkHref(firstArtifact) : "";
    if (firstArtifact && firstArtifactHref) {
      hyperlinks.push({
        row: nodeRowIndex,
        col: 12,
        target: firstArtifactHref,
        tooltip: artifactDisplayName(firstArtifact),
      });
    }
    for (const artifact of artifacts) {
      const artifactRowIndex = artifactRows.length;
      const artifactName = artifactDisplayName(artifact);
      const href = artifactXlsxHyperlinkHref(artifact);
      artifactRows.push([agentNodeDisplayLabel(row), node.agent_name ?? row.key, artifactName, href]);
      if (href) {
        artifactHyperlinks.push({
          row: artifactRowIndex,
          col: 2,
          target: href,
          tooltip: artifactName,
        });
      }
    }
  }

  return [{
    name: "节点数据",
    rows,
    hyperlinks,
    columnWidths: [24, 22, 18, 18, 12, 14, 14, 14, 14, 14, 14, 12, 32],
  }, {
    name: "产物链接",
    rows: artifactRows,
    hyperlinks: artifactHyperlinks,
    columnWidths: [24, 22, 28, 72],
  }];
}

export function buildRunReviewRawEventsXlsxSheets(eventRows: RunReviewEventRowData[]): XlsxSheetSpec[] {
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
  const rows: XlsxCellValue[][] = [
    headers,
    ...eventRows.map((event): XlsxCellValue[] => [
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
  ];
  return [{
    name: "RAW 交互信息",
    rows,
    columnWidths: [28, 14, 16, 18, 14, 28, 18, 26, 24, 16, 12, 14, 14, 42, 52, 52, 18, 60, 60],
  }];
}

function toolMessageKey(taskId: string, seq: number) {
  return `${taskId}:${seq}`;
}

function artifactDisplayName(artifact: AgentTaskArtifact) {
  return artifact.title || artifact.filename || artifact.id;
}

function dedupeArtifacts(artifacts: AgentTaskArtifact[]) {
  const byKey = new Map<string, AgentTaskArtifact>();
  for (const artifact of artifacts) {
    const key = artifactSemanticKey(artifact);
    const existing = byKey.get(key);
    if (!existing || artifactCreatedMs(artifact) >= artifactCreatedMs(existing)) {
      byKey.set(key, artifact);
    }
  }
  return [...byKey.values()].sort(compareArtifactsForDisplay);
}

function artifactSemanticKey(artifact: AgentTaskArtifact) {
  return [
    artifact.kind || "",
    artifact.title || "",
    artifact.filename || "",
  ].join(":").toLowerCase();
}

function artifactCreatedMs(artifact: AgentTaskArtifact) {
  const parsed = Date.parse(artifact.created_at);
  return Number.isFinite(parsed) ? parsed : 0;
}

function compareArtifactsForDisplay(left: AgentTaskArtifact, right: AgentTaskArtifact) {
  const leftName = left.title || left.filename || left.id;
  const rightName = right.title || right.filename || right.id;
  if (leftName !== rightName) return leftName.localeCompare(rightName, "zh-CN");
  return artifactCreatedMs(right) - artifactCreatedMs(left);
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
  const primaryNode = left.node_type === "agent_task" ? left : right.node_type === "agent_task" ? right : left;
  return {
    ...primaryNode,
    status: mergeNodeStatus(left.status, right.status),
    started_at: earliestTime(left.started_at, right.started_at),
    actual_started_at: earliestTime(left.actual_started_at, right.actual_started_at),
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
    artifacts: dedupeArtifacts([...(left.artifacts ?? []), ...(right.artifacts ?? [])]),
    evidence_refs: [...(left.evidence_refs ?? []), ...(right.evidence_refs ?? [])],
    summary: rightCompleted !== null && (leftCompleted === null || rightCompleted >= leftCompleted)
      ? right.summary
      : left.summary,
    node_id: primaryNode.node_id,
    node_type: primaryNode.node_type,
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
  const backendFailure = chain.failure_signal && !semantic.suppressFailureSignal;
  const detailParts = [
    chain.tool ? `raw_tool: ${chain.tool}` : "",
    chain.input ? `input:\n${formatJSON(chain.input)}` : "",
    chain.output ? `output:\n${chain.output}` : "",
    chain.failure_reason && backendFailure ? `failure:\n${chain.failure_reason}` : "",
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
    summary: backendFailure ? conciseEventSummary(firstNonEmpty(chain.failure_reason, semantic.summary), true) : semantic.summary,
    detail: detailParts.join("\n\n"),
    metadataDetail: "",
    durationMs: chain.duration_ms ?? 0,
    tokenTotal: 0,
    severity: backendFailure ? "error" : chain.status === "缺少结果" || chain.status === "孤立结果" ? "warning" : semantic.severity,
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
  suppressFailureSignal?: boolean;
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
    const outputSignal = output ? outputOutcome(output) : null;
    if (outputSignal?.severity === "error") {
      return {
        category: action.category,
        sourceLabel: action.sourceLabel,
        object: shortPath(path),
        title: `${action.titlePrefix}：${shortPath(path)}`,
        outcome: outputSignal.outcome,
        summary: outputSignal.summary,
        severity: outputSignal.severity,
      };
    }
    return {
      category: action.category,
      sourceLabel: action.sourceLabel,
      object: shortPath(path),
      title: `${action.titlePrefix}：${shortPath(path)}`,
      outcome: "已记录",
      summary: "",
      severity: "normal",
      suppressFailureSignal: true,
    };
  }

  const query = firstNonEmpty(stringFromUnknown(input?.query), stringFromUnknown(input?.pattern));
  if (query) {
    const outputSignal = output ? outputOutcome(output) : null;
    if (outputSignal?.severity === "error") {
      return {
        category: "搜索",
        sourceLabel: "搜索",
        object: truncateText(query, 96),
        title: `搜索：${truncateText(query, 96)}`,
        outcome: outputSignal.outcome,
        summary: outputSignal.summary,
        severity: outputSignal.severity,
      };
    }
    return {
      category: "搜索",
      sourceLabel: "搜索",
      object: truncateText(query, 96),
      title: `搜索：${truncateText(query, 96)}`,
      outcome: "已记录",
      summary: "",
      severity: "normal",
      suppressFailureSignal: true,
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
      suppressFailureSignal: outputSignal?.suppressFailureSignal,
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
    suppressFailureSignal: outputSignal?.suppressFailureSignal,
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
      ...semanticOutputState(output, segment),
    };
  }

  if (["sed", "cat", "nl", "ls", "head", "tail"].includes(executable) || isGitReadCommand(segment)) {
    return {
      category: "查看",
      sourceLabel: "读取上下文",
      object: readTargetFromCommand(segment) || truncateText(segment, 96),
      title: `${readCommandTitlePrefix(segment, executable)}：${readTargetFromCommand(segment) || truncateText(segment, 96)}`,
      ...semanticOutputState(output, segment),
    };
  }

  if (isPnpmTypecheck(segment)) {
    return {
      category: "验证",
      sourceLabel: "类型检查",
      object: pnpmFilterFromCommand(segment) || "TypeScript",
      title: `运行类型检查：${pnpmFilterFromCommand(segment) || "TypeScript"}`,
      ...semanticOutputState(output, segment),
    };
  }

  if (isPnpmTest(segment)) {
    return {
      category: "验证",
      sourceLabel: "前端测试",
      object: testTargetFromCommand(segment) || pnpmFilterFromCommand(segment) || "Vitest",
      title: `运行前端单测：${testTargetFromCommand(segment) || pnpmFilterFromCommand(segment) || "Vitest"}`,
      ...semanticOutputState(output, segment),
    };
  }

  if (isGoTest(segment)) {
    const target = goTestTargetFromCommand(segment);
    return {
      category: "验证",
      sourceLabel: "后端测试",
      object: target,
      title: `运行后端单测：${target}`,
      ...semanticOutputState(output, segment),
    };
  }

  if (isBuildCommand(segment)) {
    return {
      category: "构建",
      sourceLabel: "构建",
      object: truncateText(segment, 96),
      title: `运行构建：${truncateText(segment, 96)}`,
      ...semanticOutputState(output, segment),
    };
  }

  if (executable === "curl" || executable === "http" || executable === "wget") {
    return {
      category: "接口",
      sourceLabel: "接口检查",
      object: httpTargetFromCommand(segment) || truncateText(segment, 96),
      title: `检查接口：${httpTargetFromCommand(segment) || truncateText(segment, 96)}`,
      ...semanticOutputState(output, segment),
    };
  }

  if (executable === "make" && (segment.includes("dev") || segment.includes("start") || segment.includes("server"))) {
    return {
      category: "服务",
      sourceLabel: "运行服务",
      object: truncateText(segment, 96),
      title: `运行服务：${truncateText(segment, 96)}`,
      ...semanticOutputState(output, segment),
    };
  }

  if (executable === "make" && (segment.includes("check") || segment.includes("verify") || segment.includes("test"))) {
    return {
      category: "验证",
      sourceLabel: "验证",
      object: truncateText(segment, 96),
      title: `运行验证：${truncateText(segment, 96)}`,
      ...semanticOutputState(output, segment),
    };
  }

  return {
    category: "命令",
    sourceLabel: "命令执行",
    object: truncateText(segment || normalized, 96),
    title: `执行命令：${truncateText(segment || normalized, 96)}`,
    ...semanticOutputState(output, segment),
  };
}

function semanticOutputState(
  output: string | undefined,
  command?: string,
): Pick<SemanticToolAction, "outcome" | "summary" | "severity" | "suppressFailureSignal"> {
  if (!output) return { outcome: "已记录", summary: "", severity: "normal" };
  return outputOutcome(output, command);
}

function outputOutcome(
  output: string,
  command?: string,
): Pick<SemanticToolAction, "outcome" | "summary" | "severity" | "suppressFailureSignal"> {
  const normalizedOutput = toolOutputText(output);
  if (toolOutputHasToolUseError(normalizedOutput)) {
    return { outcome: "异常线索", summary: conciseEventSummary("工具调用返回错误", true), severity: "error" };
  }
  const exitCode = toolOutputExitCode(normalizedOutput);
  if (exitCode !== null) {
    if (exitCode === 0) {
      return { outcome: "已返回", summary: conciseEventSummary(summarizeToolOutput(normalizedOutput), false), severity: "normal", suppressFailureSignal: true };
    }
    return {
      outcome: "异常线索",
      summary: conciseEventSummary(`Exit Code: ${exitCode}`, true),
      severity: "error",
    };
  }
  if (
    toolOutputHasSuccessfulExitCode(normalizedOutput) ||
    outputHasOnlyBenignFailureCounters(normalizedOutput) ||
    outputIsReadOnlyCommandContent(command, normalizedOutput)
  ) {
    return { outcome: "已返回", summary: conciseEventSummary(summarizeToolOutput(normalizedOutput), false), severity: "normal", suppressFailureSignal: true };
  }
  const errorLine = extractErrorLine(normalizedOutput);
  if (errorLine) {
    return { outcome: "异常线索", summary: conciseEventSummary(errorLine, true), severity: "error" };
  }
  const httpStatus = toolOutputHTTPStatusCode(normalizedOutput);
  if (httpStatus !== null) {
    return {
      outcome: "异常线索",
      summary: conciseEventSummary(`HTTP ${httpStatus}`, true),
      severity: "error",
    };
  }
  return {
    outcome: "已返回",
    summary: conciseEventSummary(summarizeToolOutput(normalizedOutput), false),
    severity: "normal",
    suppressFailureSignal: true,
  };
}

function toolOutputText(output: string) {
  const trimmed = output.trim();
  if (!trimmed.startsWith("[")) return output;
  try {
    const parts = JSON.parse(trimmed) as unknown;
    if (!Array.isArray(parts)) return output;
    const texts = parts
      .map((part) => part && typeof part === "object" && "text" in part ? stringFromUnknown((part as { text?: unknown }).text).trim() : "")
      .filter(Boolean);
    return texts.length ? texts.join("\n") : output;
  } catch {
    return output;
  }
}

function toolOutputHasSuccessfulExitCode(output: string) {
  return /\bExit Code:\s*0\b/i.test(output) || /\bexit\s+(?:status|code)\s*[:=]?\s*0\b/i.test(output);
}

function toolOutputExitCode(output: string) {
  const patterns = [
    /\bExit Code:\s*(\d+)\b/i,
    /\bexit\s+(?:status|code)\s*[:=]?\s*(\d+)\b/i,
    /\bexited\s+with\s+(?:status|code)\s*[:=]?\s*(\d+)\b/i,
  ];
  for (const pattern of patterns) {
    const match = pattern.exec(output);
    if (!match?.[1]) continue;
    const value = Number.parseInt(match[1], 10);
    if (Number.isFinite(value)) return value;
  }
  return null;
}

function toolOutputHTTPStatusCode(output: string) {
  const patterns = [
    /\bhttp(?:\/[\d.]+)?\s*(?:status\s*)?([45]\d{2})\b/i,
    /\bstatus(?:\s*code)?\s*[:=]?\s*([45]\d{2})\b/i,
  ];
  for (const pattern of patterns) {
    const match = pattern.exec(output);
    if (!match?.[1]) continue;
    const value = Number.parseInt(match[1], 10);
    if (Number.isFinite(value)) return value;
  }
  return null;
}

function toolOutputHasToolUseError(output: string) {
  return output.toLowerCase().includes("<tool_use_error>");
}

function outputHasOnlyBenignFailureCounters(output: string) {
  const lower = output.toLowerCase();
  if (/(?:\berror:|\bexception\b|\bpanic:|\bfatal\b|\bpermission denied\b|\bprovider timeout\b|\btimed out\b|\bhttp\s+[45]\d\d\b|\bstatus\s+[45]\d\d\b|错误|异常|超时|无权限|权限拒绝)/i.test(output)) {
    return false;
  }
  return isBenignFailureCounterLine(lower);
}

function outputIsReadOnlyCommandContent(command: string | undefined, output: string) {
  const normalizedCommand = meaningfulCommandSegment(command ?? "").toLowerCase();
  if (!normalizedCommand) return false;
  const isReadOnlyGitCommand = normalizedCommand.startsWith("git diff") ||
    normalizedCommand.startsWith("git branch") ||
    normalizedCommand.startsWith("git show") ||
    normalizedCommand.startsWith("git status") ||
    normalizedCommand.startsWith("git log");
  const executable = commandExecutable(normalizedCommand);
  const isReadOnlyShellCommand = ["cat", "sed", "nl", "ls", "head", "tail", "rg", "grep", "find"].includes(executable);
  const isCommentListCommand = normalizedCommand.startsWith("multica issue comment list");
  const isLocalArtifactRead = ["curl", "wget"].includes(executable) &&
    (normalizedCommand.includes("/uploads/") || normalizedCommand.includes("/api/attachments/"));
  if (!isReadOnlyGitCommand && !isReadOnlyShellCommand && !isCommentListCommand && !isLocalArtifactRead) return false;
  return !toolOutputHasNonEmptyStderr(output);
}

function toolOutputHasNonEmptyStderr(output: string) {
  return output.split("\n").some((line) => {
    const match = /^\s*stderr:\s*(.*)$/i.exec(line);
    if (!match) return false;
    const value = match[1]?.trim() ?? "";
    return value !== "" && value !== "(empty)";
  });
}

function extractErrorLine(output: string) {
  const patterns = [
    /^\s*Error:\s*.+/i,
    /^\s*Traceback\b.*/i,
    /^\s*RuntimeError:\s*.+/i,
    /^\s*Exception\b.*/i,
    /^\s*--- FAIL[:\s].+/i,
    /^\s*FAIL(?:\s|:).+/i,
    /^\s*panic:\s*.+/i,
    /^\s*FATAL\b.+/i,
    /^\s*make(?:\[\d+\])?: \*\*\* .*\bError\s+\d+\b.*/i,
    /^\s*command failed\b.*/i,
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
  return ["show", "diff", "status", "log", "branch"].includes(subcommand ?? "");
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
    case "human_confirmation":
      return "人工确认";
    case "dispatch_wait":
      return "调度等待";
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
    const backendFailure = chain.failure_signal && !semantic.suppressFailureSignal;
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
      failure_signal: backendFailure || semantic.severity === "error",
      failure_reason: firstNonEmpty(backendFailure ? chain.failure_reason : "", semantic.severity === "error" ? extractErrorLine(toolOutputText(chain.output ?? "")) : ""),
      input_summary: truncateText(chain.input ? formatJSON(chain.input) : "", 420),
      output_summary: truncateText(firstNonEmpty(semantic.summary, summarizeToolOutput(toolOutputText(chain.output ?? ""))), 420),
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
        failure_reason: extractErrorLine(toolOutputText(message.output ?? "")),
        input_summary: truncateText(message.input ? formatJSON(message.input) : "", 420),
        output_summary: truncateText(firstNonEmpty(semantic.summary, summarizeToolOutput(toolOutputText(message.output ?? ""))), 420),
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

export function buildXlsxWorkbook(sheets: XlsxSheetSpec[]) {
  const workbook = XLSX.utils.book_new();
  for (const sheet of sheets) {
    const worksheet = XLSX.utils.aoa_to_sheet(sheet.rows);
    if (sheet.columnWidths?.length) {
      worksheet["!cols"] = sheet.columnWidths.map((wch) => ({ wch }));
    }
    for (const hyperlink of sheet.hyperlinks ?? []) {
      const address = XLSX.utils.encode_cell({ r: hyperlink.row, c: hyperlink.col });
      const cell = worksheet[address] ?? { t: "s", v: "" };
      cell.l = {
        Target: hyperlink.target,
        Tooltip: hyperlink.tooltip ?? hyperlink.target,
      };
      worksheet[address] = cell;
    }
    XLSX.utils.book_append_sheet(workbook, worksheet, sanitizeSheetName(sheet.name));
  }
  return workbook;
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

function sanitizeSheetName(name: string) {
  return (name || "Sheet1").replace(/[\\/:?*[\]]/g, "_").slice(0, 31) || "Sheet1";
}

function sanitizeFilename(filename: string) {
  return filename.replace(/[^\w.-]+/g, "_");
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
