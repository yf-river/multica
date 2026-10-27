"use client";

// 本文件是 Agent 详情页的“活动”标签页（含 Overview 侧栏的迷你性能摘要）：
// 三个区段依次回答“现在在跑什么 / 最近 30 天表现如何 / 最近完成了什么”。
// 全部读页面上其它部分已填充的缓存（任务快照、单 Agent 任务列表、7 天活动桶），
// 页面水合后打开本 Tab 不会多发请求。

import type { ReactNode } from "react";
import { useMemo, useState } from "react";
import {
  ArrowUpRight,
  CircleHelp,
  Hash,
  MessageSquare,
  Workflow,
  X,
} from "lucide-react";
import { toast } from "sonner";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { Button } from "@multica/ui/components/ui/button";
import { NumberFlow } from "@multica/ui/components/ui/number-flow";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useInfiniteQuery, useQueries, useQuery } from "@tanstack/react-query";
import type { Agent, AgentTask, Issue } from "@multica/core/types";
import {
  type AgentActivity,
  agentTaskSnapshotOptions,
  agentTasksOptions,
  summarizeActivityWindow,
  useWorkspaceActivityMap,
} from "@multica/core/agents";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { issueDetailOptions } from "@multica/core/issues/queries";
import { AppLink } from "../../../navigation";
import { TranscriptButton } from "../../../common/task-transcript";
import { AttributionBadge } from "../../../issues/components/attribution-badge";
import { taskStatusConfig } from "../../config";
import { cancelReasonLabel, failureReasonLabel } from "./task-failure";
import { Sparkline } from "../sparkline";
import { useT, useTimeAgo } from "../../../i18n";

// Reveal cached rows in small batches and fetch another page only on demand.
const RECENT_INITIAL = 10;
const RECENT_PAGE = 20;
// 懒加载的单 Agent 任务列表在途时先占几行骨架，让本 Tab 首屏是骨架而不是
// “还没有完成任何任务”的空态（后者读起来像错误的结论）。
const RECENT_SKELETON_ROWS = 4;

interface ActivityTabProps {
  /** 要展示的 Agent。 */
  agent: Agent;
  /** 是否展示“最近 30 天”性能区段（概览侧栏的迷你摘要会传 false 用自家卡片）。 */
  showPerformance?: boolean;
}

/**
 * Agent 详情页的活动标签页。三个区段围绕用户的三个诊断问题、按扫读顺序排布：
 *
 *   现在       —— 它这一秒在干什么？
 *   最近 7 天   —— 它总体干得怎么样？
 *   最近完成   —— 它刚干完了什么？
 *
 * 三处都读页面其余部分已填充的缓存（“现在”读工作区任务快照、“最近”读单 Agent
 * 任务列表、趋势读工作区 7 天活动桶），页面水合后打开本 Tab 不再产生额外请求。
 */
export function ActivityTab({ agent, showPerformance = true }: ActivityTabProps) {
  const wsId = useWorkspaceId();

  const { data: snapshot = [] } = useQuery(agentTaskSnapshotOptions(wsId));
  // `isLoading` (pending + fetching, no cached data) is true only on the
  // very first fetch. Once the page has hydrated this cache elsewhere the
  // tab opens straight into data with no skeleton flash.
  const {
    data,
    isLoading: isLoadingRecent,
    hasNextPage,
    fetchNextPage,
    isFetchingNextPage,
    isFetchNextPageError,
  } = useInfiniteQuery(agentTasksOptions(wsId, agent.id));
  const agentTasks = useMemo(() => {
    const tasks = new Map<string, AgentTask>();
    for (const page of data?.pages ?? []) {
      for (const task of page.tasks) tasks.set(task.id, task);
    }
    return [...tasks.values()];
  }, [data]);
  const { byAgent: activityMap } = useWorkspaceActivityMap(wsId);
  const activity = activityMap.get(agent.id);

  const [recentDisplayLimit, setRecentDisplayLimit] = useState(RECENT_INITIAL);

  // 聊天任务在所有 Agent 视角的界面（列表 / 详情 / 活动）里都刻意隐藏。它们在聊天
  // 体验里有自己的 UI；混进来会模糊“这个 Agent 在替团队干什么”和“这个 Agent 在
  // 私聊里干什么”的区别。
  const isWorkflowTask = (t: AgentTask) => !t.chat_session_id;

  const activeTasks = useMemo(() => {
    const statusRank: Partial<Record<AgentTask["status"], number>> = {
      running: 0,
      dispatched: 1,
      waiting_plan: 2,
      waiting_input: 2,
      waiting_local_directory: 2,
      queued: 3,
    };
    return snapshot
      .filter(
        (t) =>
          t.agent_id === agent.id &&
          isWorkflowTask(t) &&
          (t.status === "running" ||
            t.status === "queued" ||
            t.status === "dispatched" ||
            t.status === "waiting_plan" || t.status === "waiting_input" || t.status === "waiting_local_directory"),
      )
      .sort(
        (a, b) =>
          (statusRank[a.status] ?? 99) - (statusRank[b.status] ?? 99) ||
          new Date(a.created_at).getTime() - new Date(b.created_at).getTime(),
      );
  }, [snapshot, agent.id]);

  // 最近完成的终结态任务。包含被取消的——找“刚才发生了什么”的用户想同时看到
  // 取消、完成与失败。聊天会话与上面同因被过滤。
  const recentTasksAll = useMemo(() => {
    return [...agentTasks]
      .filter(
        (t) =>
          isWorkflowTask(t) &&
          !!t.completed_at &&
          (t.status === "completed" ||
            t.status === "failed" ||
            t.status === "cancelled"),
      )
      .sort(
        (a, b) =>
          new Date(b.completed_at!).getTime() -
          new Date(a.completed_at!).getTime(),
      );
  }, [agentTasks]);

  const recentTasks = useMemo(
    () => recentTasksAll.slice(0, recentDisplayLimit),
    [recentTasksAll, recentDisplayLimit],
  );
  const hasMoreRecent = recentTasksAll.length > recentTasks.length || hasNextPage;

  const avgDurationMs = activity?.avgDurationMs ?? 0;

  // 解析我们渲染的每个任务关联的 issue 标识与标题。走 `issueDetailOptions` 与应用
  // 其它地方同一条查询，缓存共享，不会为重复数据花钱。
  const displayedTasks = useMemo(
    () => [...activeTasks, ...recentTasks],
    [activeTasks, recentTasks],
  );
  const issueIds = useMemo(
    () =>
      Array.from(
        new Set(displayedTasks.map((t) => t.issue_id).filter((id) => id !== "")),
      ),
    [displayedTasks],
  );
  const issueQueries = useQueries({
    queries: issueIds.map((id) => issueDetailOptions(wsId, id)),
  });
  const issueMap = useMemo(() => {
    const m = new Map<string, Issue>();
    issueQueries.forEach((q, i) => {
      const id = issueIds[i]!;
      if (q.data) m.set(id, q.data);
    });
    return m;
  }, [issueQueries, issueIds]);

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <NowSection tasks={activeTasks} issueMap={issueMap} agent={agent} />
      {showPerformance && (
        <Last30dSection activity={activity} avgDurationMs={avgDurationMs} />
      )}
      <RecentWorkSection
        tasks={recentTasks}
        totalCount={hasNextPage ? undefined : recentTasksAll.length}
        hasMore={hasMoreRecent}
        loading={isLoadingRecent}
        fetchingMore={isFetchingNextPage}
        fetchMoreFailed={isFetchNextPageError}
        onShowMore={() => {
          if (recentDisplayLimit < recentTasksAll.length) {
            setRecentDisplayLimit((n) => n + RECENT_PAGE);
          } else if (hasNextPage && !isFetchingNextPage) {
            void fetchNextPage({ cancelRefetch: false }).then((result) => {
              if (!result.isFetchNextPageError) {
                setRecentDisplayLimit((n) => n + RECENT_PAGE);
              }
            });
          }
        }}
        issueMap={issueMap}
        agent={agent}
      />
    </div>
  );
}

/** Overview 侧栏用的紧凑性能摘要。与工作列表分开，让指标永远不压过当前任务与失败。 */
export function AgentPerformanceSummary({ agent }: { agent: Agent }) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const { byAgent: activityMap } = useWorkspaceActivityMap(wsId);
  const activity = activityMap.get(agent.id);
  const summary = summarizeActivityWindow(activity, 30);
  const avgDurationMs = activity?.avgDurationMs ?? 0;

  return (
    <section className="mt-5 border-t pt-5">
      <h2 className="text-body font-medium">
        {t(($) => $.tab_body.activity.section_last_30d)}
      </h2>
      {summary.totalRuns === 0 ? (
        <p className="mt-3 text-caption text-muted-foreground">
          {t(($) => $.tab_body.activity.empty_30d)}
        </p>
      ) : (
        <>
          <div className="mt-4 grid grid-cols-2 gap-x-4 gap-y-3">
            <Metric
              value={String(summary.totalRuns)}
              label={t(($) => $.tab_body.activity.runs, {
                count: summary.totalRuns,
              })}
            />
            <Metric
              value={<SuccessRate rate={summary.successRate} />}
              label={t(($) => $.tab_body.activity.success_label)}
            />
            <Metric
              value={avgDurationMs > 0 ? formatDurationMs(avgDurationMs) : "—"}
              label={t(($) => $.tab_body.activity.avg_duration_label)}
            />
            <Metric
              value={String(summary.totalFailed)}
              label={t(($) => $.tab_body.activity.failed_label)}
              destructive={summary.totalFailed > 0}
            />
          </div>
          {summary.totalCancelled > 0 && (
            <p className="mt-2 text-caption text-muted-foreground">
              {t(($) => $.tab_body.activity.cancelled_count, {
                count: summary.totalCancelled,
              })}
            </p>
          )}
          <Sparkline
            buckets={summary.buckets}
            width={250}
            height={36}
            className="mt-4 h-9 w-full"
          />
        </>
      )}
    </section>
  );
}

/** 单个指标块：大数字 + 小标签（destructive 时标红）。 */
function Metric({
  value,
  label,
  destructive = false,
}: {
  value: ReactNode;
  label: string;
  destructive?: boolean;
}) {
  return (
    <div className="min-w-0">
      <div
        className={`text-title font-semibold tabular-nums ${
          destructive ? "text-destructive" : "text-foreground"
        }`}
      >
        {value}
      </div>
      <div className="truncate text-micro text-muted-foreground">{label}</div>
    </div>
  );
}

function SuccessRate({
  rate,
  labelled = false,
}: {
  rate: number | null;
  labelled?: boolean;
}) {
  const { t } = useT("agents");
  return (
    <Tooltip>
      <TooltipTrigger render={<span tabIndex={0} />}>
        {rate === null
          ? "—"
          : labelled
            ? t(($) => $.tab_body.activity.success_pct, { percent: rate })
            : `${rate}%`}
      </TooltipTrigger>
      <TooltipContent>
        {rate === null
          ? t(($) => $.tab_body.activity.success_unavailable)
          : t(($) => $.tab_body.activity.success_hint)}
      </TooltipContent>
    </Tooltip>
  );
}

function NowSection({
  tasks,
  issueMap,
  agent,
}: {
  tasks: AgentTask[];
  issueMap: Map<string, Issue>;
  agent: Agent;
}) {
  const { t } = useT("agents");
  return (
    <Section
      title={t(($) => $.tab_body.activity.section_now)}
      subtitle={
        tasks.length === 0
          ? t(($) => $.tab_body.activity.subtitle_no_active)
          : t(($) => $.tab_body.activity.subtitle_active, { count: tasks.length })
      }
    >
      {tasks.length === 0 ? (
        <EmptyText>{t(($) => $.tab_body.activity.empty_now)}</EmptyText>
      ) : (
        <TaskList
          tasks={tasks}
          issueMap={issueMap}
          timeMode="active"
          agent={agent}
        />
      )}
    </Section>
  );
}

/** “最近 30 天”区段：总运行数、成功率、平均耗时、失败数 + 吞吐/失败迷你图。 */
function Last30dSection({
  activity,
  avgDurationMs,
}: {
  activity: AgentActivity | undefined;
  avgDurationMs: number;
}) {
  const { t, i18n } = useT("agents");
  const summary = summarizeActivityWindow(activity, 30);
  const { totalRuns, totalFailed } = summary;
  const locales = i18n.resolvedLanguage ?? i18n.language;

  return (
    <Section title={t(($) => $.tab_body.activity.section_last_30d)} subtitle={t(($) => $.tab_body.activity.subtitle_performance)}>
      {totalRuns === 0 ? (
        <EmptyText>{t(($) => $.tab_body.activity.empty_30d)}</EmptyText>
      ) : (
        <div className="flex items-end justify-between gap-5">
          <div className="flex min-w-0 flex-col gap-1">
            <div className="flex items-baseline gap-1.5">
              <NumberFlow
                value={totalRuns}
                locales={locales}
                format={{ maximumFractionDigits: 0 }}
                aria-label={String(totalRuns)}
                className="text-display font-bold leading-none"
              />
              <span className="text-body text-muted-foreground">
                {t(($) => $.tab_body.activity.runs, { count: totalRuns })}
              </span>
            </div>
            <div className="text-caption text-muted-foreground">
              <SuccessRate rate={summary.successRate} labelled />
              {avgDurationMs > 0 && (
                <>
                  <Sep />
                  <span>{t(($) => $.tab_body.activity.avg_duration, { value: formatDurationMs(avgDurationMs) })}</span>
                </>
              )}
              {totalFailed > 0 && (
                <>
                  <Sep />
                  <span className="text-destructive">
                    {t(($) => $.tab_body.activity.failed_count, { count: totalFailed })}
                  </span>
                </>
              )}
              {summary.totalCancelled > 0 && (
                <>
                  <Sep />
                  <span>
                    {t(($) => $.tab_body.activity.cancelled_count, {
                      count: summary.totalCancelled,
                    })}
                  </span>
                </>
              )}
            </div>
          </div>
          {/* 点缀而非主角——足够小，稀疏的 30 天序列不会显得视觉上破碎。
              与数字底对齐，让柱状密端与数字落在同一基线上。 */}
          <Sparkline
            buckets={summary.buckets}
            width={120}
            height={32}
            className="shrink-0"
          />
        </div>
      )}
    </Section>
  );
}

/** “最近完成”区段：已终结任务列表 + “显示更多”分页。 */
function RecentWorkSection({
  tasks,
  totalCount,
  hasMore,
  loading,
  onShowMore,
  fetchingMore,
  fetchMoreFailed,
  issueMap,
  agent,
}: {
  tasks: AgentTask[];
  // Only known after the server has returned the final history page.
  totalCount?: number;
  hasMore: boolean;
  loading: boolean;
  onShowMore: () => void;
  fetchingMore: boolean;
  fetchMoreFailed: boolean;
  issueMap: Map<string, Issue>;
  agent: Agent;
}) {
  const { t } = useT("agents");
  // 首次请求还没返回时没有计数可概括，所以副标题留空，而不是声称“还没完成任何东西”。
  const subtitle = loading
    ? ""
    : tasks.length === 0
      ? hasMore
        ? ""
        : t(($) => $.tab_body.activity.subtitle_no_recent)
      : totalCount !== undefined && totalCount > tasks.length
        ? t(($) => $.tab_body.activity.subtitle_recent_progress, { shown: tasks.length, total: totalCount })
        : t(($) => $.tab_body.activity.subtitle_recent_latest, { count: tasks.length });
  return (
    <Section title={t(($) => $.tab_body.activity.section_recent)} subtitle={subtitle}>
      {loading ? (
        <RecentWorkSkeleton />
      ) : tasks.length === 0 ? (
        hasMore ? null : <EmptyText>{t(($) => $.tab_body.activity.empty_recent)}</EmptyText>
      ) : (
        <TaskList
          tasks={tasks}
          issueMap={issueMap}
          timeMode="completed"
          agent={agent}
        />
      )}
      {!loading && hasMore && (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={onShowMore}
          disabled={fetchingMore}
          aria-busy={fetchingMore}
          className="self-start"
        >
          {fetchMoreFailed ? t(($) => $.detail.try_again) : t(($) => $.tab_body.activity.show_more)}
        </Button>
      )}
    </Section>
  );
}

/**
 * “最近完成”列表的加载占位。镜像 completed 模式 TaskList 的外壳（带边框、
 * 分隔行）与 TaskRow 两行节奏——状态图形 + 标题 + 元信息行——骨架落位时
 * 不会产生布局跳动。宽度按行错开，读起来像内容而不是一整块。
 */
function RecentWorkSkeleton() {
  const titleWidths = ["w-3/5", "w-4/5", "w-1/2", "w-2/3"];
  const metaWidths = ["w-2/5", "w-1/3", "w-2/5", "w-1/4"];
  return (
    <div
      className="overflow-hidden rounded-lg border divide-y"
      aria-hidden="true"
    >
      {Array.from({ length: RECENT_SKELETON_ROWS }).map((_, i) => (
        <div key={i} className="flex items-center gap-3 px-3 py-3">
          <Skeleton className="h-4 w-4 shrink-0 rounded-full" />
          <div className="min-w-0 flex-1 space-y-2">
            <Skeleton className={`h-3.5 ${titleWidths[i % titleWidths.length]}`} />
            <Skeleton className={`h-3 ${metaWidths[i % metaWidths.length]}`} />
          </div>
        </div>
      ))}
    </div>
  );
}

/** 任务行列表外壳：completed 模式为“带边框分隔”的卡，active 模式为稀疏纵向间距。 */
function TaskList({
  tasks,
  issueMap,
  timeMode,
  agent,
}: {
  tasks: AgentTask[];
  issueMap: Map<string, Issue>;
  timeMode: "active" | "completed";
  agent: Agent;
}) {
  return (
    <div
      className={
        timeMode === "completed"
          ? "overflow-hidden rounded-lg border divide-y"
          : "space-y-2"
      }
    >
      {tasks.map((task) => (
        <TaskRow
          key={task.id}
          task={task}
          issueMap={issueMap}
          timeMode={timeMode}
          agent={agent}
        />
      ))}
    </div>
  );
}

/** 单条任务行：状态图标 + 标题 + 元信息（状态/时间/耗时/失败原因/负责人）+ 悬停操作。 */
function TaskRow({
  task,
  issueMap,
  timeMode,
  agent,
}: {
  task: AgentTask;
  issueMap: Map<string, Issue>;
  timeMode: "active" | "completed";
  agent: Agent;
}) {
  const { t } = useT("agents");
  const timeAgo = useTimeAgo();
  const paths = useWorkspacePaths();
  const [cancelling, setCancelling] = useState(false);
  const cfg = taskStatusConfig[task.status] ?? taskStatusConfig.queued!;
  const Icon = cfg.icon;
  const hasIssue = task.issue_id !== "";
  const issue = hasIssue ? issueMap.get(task.issue_id) : undefined;
  const isRunning = task.status === "running";
  // 排队任务还没有任何消息——隐藏 transcript 按钮，免得一打开就撞上
  // 必然出现的 “No execution data recorded.” 弹窗。
  const showTranscript = task.status !== "queued";
  // 只有三种活动态才谈得上“取消”。终结态行（completed / failed / cancelled）
  // 完全隐藏该按钮。
  const showCancel =
    timeMode === "active" &&
    (task.status === "queued" ||
      task.status === "dispatched" ||
      task.status === "running");

  const handleCancel = async () => {
    if (cancelling) return;
    setCancelling(true);
    try {
      await api.cancelTaskById(task.id);
      // 无需手动 invalidate —— task:cancelled 的 WS 事件会经 useRealtimeSync
      // 的 `task:` 前缀路径，把快照 + 单 Agent + 单 issue 的任务列表一并失效。
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t(($) => $.tab_body.activity.cancel_failed_toast));
      setCancelling(false);
    }
  };

  const isTerminalStatus =
    task.status === "completed" ||
    task.status === "failed" ||
    task.status === "cancelled";
  const sourceFallback = !hasIssue
    ? task.kind === "quick_create"
      ? isTerminalStatus
        ? t(($) => $.tab_body.activity.source_quick_create)
        : t(($) => $.tab_body.activity.source_creating_issue)
      : task.chat_session_id
        ? t(($) => $.tab_body.activity.source_chat_session)
        : task.autopilot_run_id
          ? t(($) => $.tab_body.activity.source_autopilot_run)
          : t(($) => $.tab_body.activity.source_untracked)
    : null;

  const SourceIcon = hasIssue
    ? Hash
    : task.chat_session_id
      ? MessageSquare
      : task.autopilot_run_id
        ? Workflow
        : CircleHelp;
  const sourceLabel = hasIssue
    ? t(($) => $.tab_body.activity.source_issue)
    : task.chat_session_id
      ? t(($) => $.tab_body.activity.source_chat)
      : task.autopilot_run_id
        ? t(($) => $.tab_body.activity.source_autopilot)
        : t(($) => $.tab_body.activity.source_untracked);

  const timeText =
    timeMode === "active"
      ? activeTaskTimeText(task, t, timeAgo)
      : task.completed_at
        ? timeAgo(task.completed_at)
        : "—";

  // 失败原因。后端对非失败任务下发 ""（omitempty 在线上去掉），所以真值判断
  // 就是对的形状。failureReasonLabel 接受原始开放字符串，因为分类会不断增长，
  // 没有可强转的枚举。已取消的行只有当“服务端”因持久化原因取消它时才给标签
  //（worktree 认领门槛、保留工作投递）；用户自己取消保持朴素的 “Cancelled”。
  const failureLabel =
    task.status === "failed"
      ? failureReasonLabel(task.failure_reason, t)
      : cancelReasonLabel(task, t);

  // 只给终结态行显示耗时。活动行的时长已由时间文案隐含（“2 分钟前开始”），
  // 旁边再加一个时间泡泡只会让这行更挤。
  let durationText: string | null = null;
  if (timeMode === "completed" && task.started_at && task.completed_at) {
    const dur =
      new Date(task.completed_at).getTime() -
      new Date(task.started_at).getTime();
    if (dur > 0) durationText = formatDurationMs(dur);
  }

  const rowClass =
    timeMode === "completed"
      ? "group flex items-center gap-3 px-3 py-3 transition-colors hover:bg-muted/30"
      : `group flex items-center gap-3 rounded-md border px-3 py-3 ${
          isRunning ? "border-brand/40 bg-brand/5" : ""
        }`;

  return (
    <div className={rowClass}>
      <Icon
        className={`h-4 w-4 shrink-0 ${cfg.color} ${
          isRunning ? "animate-spin motion-reduce:animate-none" : ""
        }`}
        aria-hidden="true"
      />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          <SourceIcon
            className="h-3 w-3 shrink-0 text-faint-foreground"
            aria-label={sourceLabel}
          />
          {issue && (
            <span className="shrink-0 font-mono text-caption text-muted-foreground">
              {issue.identifier}
            </span>
          )}
          {task.trigger_summary ? (
            // 悬停揭示“这任务为何而跑”——快照让 agent 侧的行锚定在 issue.title
            //（这里的识别轴），同时允许用户停留看触发上下文。与 GitHub Actions
            // 悬停显示提交信息同款模式。
            <Tooltip>
              <TooltipTrigger
                render={
                  <span className="truncate text-body">
                    {issue?.title ??
                      (hasIssue
                        ? t(($) => $.tab_body.activity.issue_short_fallback, { prefix: task.issue_id.slice(0, 8) })
                        : (sourceFallback ?? t(($) => $.tab_body.activity.source_untracked)))}
                  </span>
                }
              />
              <TooltipContent className="max-w-md">
                <div className="text-micro font-medium uppercase tracking-wider text-muted-foreground">
                  {t(($) => $.tab_body.activity.triggered_by)}
                </div>
                <div className="mt-0.5 whitespace-pre-wrap text-caption">
                  {task.trigger_summary}
                </div>
              </TooltipContent>
            </Tooltip>
          ) : (
            <span className="truncate text-body">
              {issue?.title ??
                (hasIssue
                  ? t(($) => $.tab_body.activity.issue_short_fallback, { prefix: task.issue_id.slice(0, 8) })
                  : (sourceFallback ?? t(($) => $.tab_body.activity.source_untracked)))}
            </span>
          )}
        </div>
        <div className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-caption text-muted-foreground">
          <span className={cfg.color}>
            {taskStatusLabel(task.status, t)}
          </span>
          <Sep />
          <span>{timeText}</span>
          {durationText && (
            <>
              <Sep />
              <span>{durationText}</span>
            </>
          )}
          {failureLabel && (
            <>
              <Sep />
              {/* 本地化原因是这里面向用户的全部解释。裸 `task.error` 曾作为本元素
                  `title` 一路跟随，结果每个非英语工作区都看到未翻译的英文（以及
                  绝对路径）（#7411）；完整诊断在 transcript 的 Run 详情里。 */}
              <span className="text-destructive">{failureLabel}</span>
            </>
          )}
          {/* 责任人（MUL-4302 §9）：这次运行代表谁。前导分隔符让头像融入整行的
              中点节奏，而不是贴在耗时后面。守卫镜像徽章自身的渲染条件
              （仅头像需要发起者），不会给无归属的运行留下悬空分隔符。 */}
          {task.attribution?.initiator && (
            <>
              <Sep />
              <AttributionBadge
                attribution={task.attribution}
                variant="avatar"
              />
            </>
          )}
        </div>
      </div>

      {/* 悬停才出现的操作。行刻意不可点，不让任何一个目的地被优待——issue 详情
          与 transcript 是同等有效的后续动作。focus-within 保证键盘用户也能触达。 */}
      <div className="ml-2 flex shrink-0 items-center gap-0.5 opacity-0 transition-opacity duration-100 group-hover:opacity-100 group-focus-within:opacity-100">
        {hasIssue && (
          <Tooltip>
            <TooltipTrigger
              render={<AppLink href={paths.issueDetail(task.issue_id)} />}
              aria-label={t(($) => $.tab_body.activity.open_issue_aria)}
              className="flex items-center justify-center rounded p-1 text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors"
            >
              <ArrowUpRight className="h-3.5 w-3.5" aria-hidden="true" />
            </TooltipTrigger>
            <TooltipContent>{t(($) => $.tab_body.activity.open_issue_tooltip)}</TooltipContent>
          </Tooltip>
        )}
        {showTranscript && (
          <TranscriptButton
            task={task}
            agentName={agent.name}
            isLive={isRunning}
            title={t(($) => $.tab_body.activity.transcript_tooltip)}
          />
        )}
        {showCancel && (
          <Tooltip>
            <TooltipTrigger
              render={
                <button
                  type="button"
                  onClick={handleCancel}
                  disabled={cancelling}
                  aria-label={t(($) => $.tab_body.activity.cancel_task_aria)}
                />
              }
              className="flex items-center justify-center rounded p-1 text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive disabled:cursor-not-allowed disabled:opacity-50"
            >
              <X className="h-3.5 w-3.5" aria-hidden="true" />
            </TooltipTrigger>
            <TooltipContent>
              {cancelling ? t(($) => $.tab_body.activity.cancelling_tooltip) : t(($) => $.tab_body.activity.cancel_task_tooltip)}
            </TooltipContent>
          </Tooltip>
        )}
      </div>
    </div>
  );
}

/** 区段容器：大标题 + 右侧小副标题。 */
function Section({
  title,
  subtitle,
  children,
}: {
  title: string;
  subtitle: string;
  children: ReactNode;
}) {
  return (
    <section className="flex flex-col gap-3 border-b pb-6 last:border-b-0 last:pb-0">
      <div className="flex items-baseline gap-2">
        <h2 className="text-body font-semibold text-foreground">
          {title}
        </h2>
        <span className="text-micro text-muted-foreground">{subtitle}</span>
      </div>
      {children}
    </section>
  );
}

/** 斜体空态文案。 */
function EmptyText({ children }: { children: ReactNode }) {
  return <p className="text-caption italic text-muted-foreground">{children}</p>;
}

function Sep() {
  // mx-1 给圆点两侧留出可见空白；不加的话内联 JSX 会把相邻 token 挤成
  // “100% 成功率·平均 30s”，一眼扫过去像 “successdotavg”。
  return <span className="mx-1 text-faint-foreground">·</span>;
}

type AgentsT = ReturnType<typeof useT<"agents">>["t"];
type TimeAgoFn = (dateStr: string) => string;

/** 把任务状态翻译成界面文案。 */
function taskStatusLabel(status: AgentTask["status"], t: AgentsT): string {
  switch (status) {
    case "waiting_plan": return t(($) => $.tab_body.activity.status.waiting_plan);
    case "waiting_input": return t(($) => $.tab_body.activity.status.waiting_input);
    case "queued":
      return t(($) => $.tab_body.activity.status.queued);
    case "dispatched":
      return t(($) => $.tab_body.activity.status.dispatched);
    case "waiting_local_directory":
      return t(($) => $.tab_body.activity.status.waiting_local_directory);
    case "running":
      return t(($) => $.tab_body.activity.status.running);
    case "completed":
      return t(($) => $.tab_body.activity.status.completed);
    case "failed":
      return t(($) => $.tab_body.activity.status.failed);
    case "cancelled":
      return t(($) => $.tab_body.activity.status.cancelled);
    default: return status;
  }
}

/** 活动任务的相对时间文案：running→“x 前开始”，dispatched→“x 前派发”，其余→“x 前排队”。 */
function activeTaskTimeText(task: AgentTask, t: AgentsT, timeAgo: TimeAgoFn): string {
  if (task.status === "running" && task.started_at) {
    return t(($) => $.tab_body.activity.started_prefix, { when: timeAgo(task.started_at) });
  }
  if (task.status === "dispatched" && task.dispatched_at) {
    return t(($) => $.tab_body.activity.dispatched_prefix, { when: timeAgo(task.dispatched_at) });
  }
  return t(($) => $.tab_body.activity.queued_prefix, { when: timeAgo(task.created_at) });
}

/**
 * Compact human-readable duration ("12s", "2m 04s", "1h 30m"). Pads the
 * seconds inside the minute formatter so the column stays visually
 * aligned across rows.
 */
export function formatDurationMs(ms: number): string {
  if (ms <= 0) return "—";
  if (ms < 60_000) {
    return `${Math.max(1, Math.round(ms / 1000))}s`;
  }
  if (ms < 60 * 60_000) {
    const m = Math.floor(ms / 60_000);
    const s = Math.round((ms % 60_000) / 1000);
    return `${m}m ${String(s).padStart(2, "0")}s`;
  }
  const h = Math.floor(ms / (60 * 60_000));
  const m = Math.floor((ms % (60 * 60_000)) / 60_000);
  return `${h}h ${m}m`;
}
