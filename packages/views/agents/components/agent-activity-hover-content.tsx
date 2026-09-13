"use client";

// 本文件提供 Agent 活动悬浮卡的内容：展示这些 Agent“现在在忙什么”，每行一个任务。

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ActorAvatar as ActorAvatarBase } from "@multica/ui/components/common/actor-avatar";
import { useActorName } from "@multica/core/workspace/hooks";
import { useWorkspaceId } from "@multica/core/hooks";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { agentListOptions } from "@multica/core/workspace/queries";
import { deriveAgentAvailability } from "@multica/core/agents";
import type { AgentTask, Issue } from "@multica/core/types";
import { workloadConfig } from "../presence";
import { useT } from "../../i18n";

interface AgentActivityHoverContentProps {
  // 要渲染的活动任务（running / queued / dispatched）——由调用方按 issue id
  // 或工作区范围筛选。保持传入顺序；每个任务各渲染成一行。
  tasks: readonly AgentTask[];
}

/**
 * 每秒刷新一次 `now`，让悬浮卡打开期间时长标签实时更新。setInterval 只在卡片
 * 挂载时运行（Base UI 会在关闭时卸载弹层内容），所以卡片关闭时零开销。
 */
function useActivityNow(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);
  return now;
}

/**
 * 用 Map 做 O(1) 的 agent + runtime 查找，让每行任务无需 N×M 扫描就能解析。
 * 开销很低——agent/runtime 数量最多也就几十个。
 */
function useActivityLookups() {
  const wsId = useWorkspaceId();
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));
  const agentById = new Map(agents.map((a) => [a.id, a] as const));
  const runtimeById = new Map(runtimes.map((r) => [r.id, r] as const));
  return { agentById, runtimeById };
}

type ActivityLookups = ReturnType<typeof useActivityLookups>;

/**
 * 单个任务行：Agent 头像、名字、状态圆点、状态标签、时长。
 *
 * 状态颜色遵循工作区已有的组合规则：
 *   - running                       → brand（text-brand）
 *   - queued、运行时在线             → muted 灰（短暂竞态）
 *   - queued、运行时离线等           → warning 琥珀（真正卡住）
 * 与 agent-presence-indicator.tsx 规则一致，让用户看到同一套
 * “Agent 出问题了” vs “刚入队”的表述。
 */
function AgentActivityTaskRow({
  task,
  now,
  agentById,
  runtimeById,
}: {
  task: AgentTask;
  now: number;
} & ActivityLookups) {
  const { t } = useT("issues");
  const { getActorName, getActorInitials, getActorAvatarUrl } = useActorName();

  const agent = agentById.get(task.agent_id);
  const runtime = runtimeFrom(agent?.runtime_id, runtimeById);
  const availability = deriveAgentAvailability(runtime, now);
  const isRunning = task.status === "running";
  // queued/dispatched 在面向用户的文案里都读作 “queued”——`dispatched` 是
  // queued 中被 daemon 确认的子状态，在这里对用户没有区分意义。
  const wl = isRunning ? workloadConfig.working : workloadConfig.queued;
  // queued + online → 用 muted 灰（短暂竞态，不告警）；
  // queued + offline/unstable → 保留 workloadConfig 的琥珀色。与
  // agent-presence-indicator.tsx 保持一致。
  const dotClass = isRunning
    ? "bg-brand"
    : availability === "online"
      ? "bg-muted-foreground/40"
      : "bg-warning";
  const labelClass = isRunning
    ? wl.textClass
    : availability === "online"
      ? "text-muted-foreground"
      : wl.textClass;
  const startedFrom = isRunning
    ? (task.started_at ?? task.dispatched_at ?? task.created_at)
    : task.created_at;

  return (
    <div className="flex items-center gap-2 text-caption">
      <ActorAvatarBase
        name={getActorName("agent", task.agent_id)}
        initials={getActorInitials("agent", task.agent_id)}
        avatarUrl={getActorAvatarUrl("agent", task.agent_id)}
        isAgent
        size="sm"
      />
      <span className="flex-1 truncate font-medium">
        {getActorName("agent", task.agent_id)}
      </span>
      <span className="flex shrink-0 items-center gap-1.5">
        <span className={`h-1.5 w-1.5 rounded-full ${dotClass}`} />
        <span className={labelClass}>
          {isRunning
            ? t(($) => $.agent_activity.status_running)
            : t(($) => $.agent_activity.status_queued)}
        </span>
        <span className="tabular-nums text-muted-foreground">
          {formatDuration(startedFrom, now)}
        </span>
      </span>
    </div>
  );
}

/**
 * 共用的悬浮卡主体，回答“这些 Agent 现在在忙什么？”——被 IssueAgentActivityIndicator
 * （按 issue）使用。每个任务一行。
 *
 * 工作区级的 chip 用的是下方的 WorkspaceAgentActivityHoverContent，它把同样的
 * 行按 issue 分组展示。
 */
export function AgentActivityHoverContent({
  tasks,
}: AgentActivityHoverContentProps) {
  const { t } = useT("issues");
  const now = useActivityNow();
  const { agentById, runtimeById } = useActivityLookups();

  if (tasks.length === 0) return null;

  return (
    <div className="flex flex-col gap-2">
      <div className="text-caption font-medium text-muted-foreground">
        {/* 每行对应一个任务，所以这里统计的是任务数而非 Agent 数。一个 Agent
            可以同时跑多个任务，若用“Agent”措辞的标题会和下面的行数对不上。 */}
        {t(($) => $.agent_activity.hover_header_tasks, { count: tasks.length })}
      </div>
      <div className="flex flex-col gap-1.5">
        {tasks.map((task) => (
          <AgentActivityTaskRow
            key={task.id}
            task={task}
            now={now}
            agentById={agentById}
            runtimeById={runtimeById}
          />
        ))}
      </div>
    </div>
  );
}

interface WorkspaceAgentActivityHoverContentProps {
  /** 工作筛选后仍留在屏幕上的 issue，按列表顺序。每个至少有一个正在运行的任务。 */
  issues: readonly Issue[];
  /** 这些 issue 对应的运行中任务，按 issue id 建立映射。 */
  tasksByIssueId: ReadonlyMap<string, readonly AgentTask[]>;
  /** `issues` 范围内运行中任务的总数——标题里的第二个数字。 */
  taskCount: number;
}

/**
 * 工作区 working chip（MUL-4884）的悬浮卡主体。
 *
 * chip 回答“谁在工作”（"N agents working"）；这张卡片回答“在哪工作”。
 * 标题带上 chip 没有的两个数字——这些工作落在多少个 issue 上、需要多少个任务——
 * 而行按 issue 分组，与点击 chip 后对列表做的筛选一致。
 *
 * 它不解释被排除在外的工作。Chat/autopilot 的运行没有关联 issue，也不会在
 * 本页留下任何痕迹：没有行、没有头像、没有指示器。为它们加脚注，只会解释
 * 一个用户从未察觉到的“缺失”——凭空制造差异，而不是解决差异。被当前筛选或
 * 当前页排除掉的 issue 上的任务同理。
 *
 * 刻意不做成仪表盘：只有两个数字 + 分组的行。
 */
export function WorkspaceAgentActivityHoverContent({
  issues,
  tasksByIssueId,
  taskCount,
}: WorkspaceAgentActivityHoverContentProps) {
  const { t } = useT("issues");
  const now = useActivityNow();
  const { agentById, runtimeById } = useActivityLookups();

  if (issues.length === 0) {
    return (
      <p className="text-caption text-muted-foreground">
        {t(($) => $.agent_activity.empty_hover)}
      </p>
    );
  }

  return (
    <div className="flex flex-col gap-2.5">
      <div className="text-caption font-medium text-muted-foreground">
        {`${t(($) => $.agent_activity.issues_count, {
          count: issues.length,
        })} · ${t(($) => $.agent_activity.tasks_count, { count: taskCount })}`}
      </div>
      <div className="flex flex-col gap-2.5">
        {issues.map((issue) => (
          <div key={issue.id} className="flex flex-col gap-1.5">
            <div className="flex items-baseline gap-1.5 text-caption">
              <span className="shrink-0 font-mono text-micro text-muted-foreground">
                {issue.identifier}
              </span>
              <span className="truncate">{issue.title}</span>
            </div>
            <div className="flex flex-col gap-1.5">
              {(tasksByIssueId.get(issue.id) ?? []).map((task) => (
                <AgentActivityTaskRow
                  key={task.id}
                  task={task}
                  now={now}
                  agentById={agentById}
                  runtimeById={runtimeById}
                />
              ))}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function runtimeFrom<T extends { id: string }>(
  id: string | undefined,
  byId: Map<string, T>,
): T | null {
  if (!id) return null;
  return byId.get(id) ?? null;
}

// 把给定 ISO 时间到当前时刻的时长格式化成紧凑的 `2m 14s` / `45s` / `1h 03m`。
// 最大只到小时——一个运行中的任务超过一天，通常是运行时卡住的信号，但悬浮卡
// 不是争论这个的地方；行会读作 `26h 12m`，用户自会处理。
//
// 导出是为了让 issue 详情头部的 live chip 用同样的 `2m 14s` / `1h 03m` 规则
// 格式化它折叠的单 Agent 耗时。
export function formatDuration(fromIso: string, nowMs: number): string {
  const start = new Date(fromIso).getTime();
  if (!Number.isFinite(start)) return "";
  const sec = Math.max(0, Math.round((nowMs - start) / 1000));
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  const remSec = sec % 60;
  if (min < 60) return `${min}m ${pad2(remSec)}s`;
  const hr = Math.floor(min / 60);
  const remMin = min % 60;
  return `${hr}h ${pad2(remMin)}m`;
}

function pad2(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}
