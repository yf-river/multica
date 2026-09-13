"use client";

// 问题界面的「进行中活动」工具：把工作区级的智能体任务快照按问题切分，
// 得到每个问题「是否有智能体正在干活/排队」的状态，供行级指示器和表头徽标使用。
import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { agentTaskSnapshotOptions } from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import type { AgentTask } from "@multica/core/types";

/** 单个问题当前的智能体活动状态。 */
export interface IssueActivityState {
  isWorking: boolean;
  isQueued: boolean;
  runningTasks: AgentTask[];
  queuedTasks: AgentTask[];
}

/** 问题界面整体需要的活动投影：按问题 id 分组的活动状态 + 正在干活的问题 id 集合。 */
export interface IssueSurfaceActivity {
  activityByIssueId: Map<string, IssueActivityState>;
  runningIssueIds: Set<string>;
}

/** 这些任务状态都算「排队中/待分发」，而不是正在运行。 */
function isQueuedTaskStatus(status: AgentTask["status"]) {
  return (
    status === "queued" ||
    status === "dispatched" ||
    status === "waiting_local_directory"
  );
}

/** 单个问题在快照里分到的任务，按运行中/排队中两组返回。 */
export interface IssueTaskGroups {
  running: AgentTask[];
  queued: AgentTask[];
}

/**
 * 从工作区级智能体任务快照里取出「某个问题」的那一份。用作行级
 * `useQuery(agentTaskSnapshotOptions)` 的 `select`：每行仍共享同一次快照查询，
 * 但 React Query 的结构共享会让返回值在这个问题的任务没变化时保持引用稳定——
 * 于是快照失效只重渲染「自己的任务真的动了」的那些行，而不是整列列表。
 * 终态任务被丢弃（它们该去问题历史里，而不是活在实时指示器上）。
 * @param snapshot - 工作区级任务快照。
 * @param issueId - 要切分出的问题 id。
 * @returns 该问题的运行中/排队中任务。
 */
export function selectIssueTasks(
  snapshot: readonly AgentTask[],
  issueId: string,
): IssueTaskGroups {
  const running: AgentTask[] = [];
  const queued: AgentTask[] = [];
  for (const task of snapshot) {
    if (task.issue_id !== issueId) continue;
    if (task.status === "running") running.push(task);
    else if (isQueuedTaskStatus(task.status)) queued.push(task);
  }
  return { running, queued };
}

/**
 * 由一组任务推导出整个界面的活动投影：只保留运行中/排队中的任务，
 * 按键分组，并收集正在干活的问题 id。
 * @param tasks - 任务列表（通常是整个工作区的快照）。
 * @returns 按问题分组的活动状态 + 正在干活的问题 id 集合。
 */
export function deriveIssueSurfaceActivity(
  tasks: readonly AgentTask[],
): IssueSurfaceActivity {
  const activityByIssueId = new Map<string, IssueActivityState>();

  for (const task of tasks) {
    if (!task.issue_id) continue;
    if (task.status !== "running" && !isQueuedTaskStatus(task.status)) {
      continue;
    }

    const current =
      activityByIssueId.get(task.issue_id) ?? {
        isWorking: false,
        isQueued: false,
        runningTasks: [],
        queuedTasks: [],
      };

    if (task.status === "running") {
      current.runningTasks.push(task);
      current.isWorking = true;
    } else {
      current.queuedTasks.push(task);
      current.isQueued = true;
    }

    activityByIssueId.set(task.issue_id, current);
  }

  const runningIssueIds = new Set<string>();
  for (const [issueId, activity] of activityByIssueId) {
    if (activity.isWorking) runningIssueIds.add(issueId);
  }

  return { activityByIssueId, runningIssueIds };
}

/**
 * 读取工作区智能体任务快照并推导出问题界面的活动投影（运行中/排队中、按问题分组）。
 * @returns 界面级的活动投影。
 */
export function useIssueSurfaceActivity(): IssueSurfaceActivity {
  const wsId = useWorkspaceId();
  const { data: snapshot = [] } = useQuery(agentTaskSnapshotOptions(wsId));
  return useMemo(() => deriveIssueSurfaceActivity(snapshot), [snapshot]);
}
