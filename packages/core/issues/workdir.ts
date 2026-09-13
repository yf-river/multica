// 解析任务“复制路径”时应复制的目录：在任务工作目录和持久化项目目录之间做选择。
import type { AgentTask } from "../types/agent";

type WorkdirTask = Pick<
  AgentTask,
  | "branch_name"
  | "created_at"
  | "durable_work_dir"
  | "relative_durable_work_dir"
  | "relative_work_dir"
  | "status"
  | "work_dir"
>;

/** 任务“复制路径”要复制到的目标目录描述。 */
export interface WorkdirCopyTarget {
  /** 要复制的绝对路径。 */
  path: string;
  /** 相对路径（可选）。 */
  relativePath?: string;
  /** 分支名（可选）。 */
  branchName?: string;
  /** 路径来源：持久化项目目录 或 任务工作目录。 */
  source: "durable_project_directory" | "task_workdir";
}

const terminalStatuses = new Set(["completed", "failed", "cancelled"]);

/**
 * 解析任务复制路径时该复制的目录，且不依赖当前机器或可变项目配置去推断归属。
 *
 * `durable_work_dir` 是 daemon 上报的生命周期事实：只有在一次性 worktree 定稿、
 * 且其移除得到确认后才会被填充。在那之前（以及混合版本部署时），`work_dir` 仍是权威。
 */
export function resolveWorkdirCopyTarget(
  tasks: readonly WorkdirTask[] | undefined,
): WorkdirCopyTarget | undefined {
  const latestTask = tasks
    ?.filter((task) => {
      if (task.work_dir?.trim()) return true;
      return (
        terminalStatuses.has(task.status) &&
        Boolean(task.durable_work_dir?.trim())
      );
    })
    .reduce<WorkdirTask | undefined>(
      (latest, task) =>
        !latest || task.created_at > latest.created_at ? task : latest,
      undefined,
    );

  if (!latestTask) return undefined;

  const durablePath = latestTask.durable_work_dir?.trim();
  if (terminalStatuses.has(latestTask.status) && durablePath) {
    return {
      path: durablePath,
      relativePath: latestTask.relative_durable_work_dir?.trim() || undefined,
      branchName: latestTask.branch_name?.trim() || undefined,
      source: "durable_project_directory",
    };
  }

  const workDir = latestTask.work_dir?.trim();
  if (!workDir) return undefined;
  return {
    path: workDir,
    relativePath: latestTask.relative_work_dir?.trim() || undefined,
    branchName: latestTask.branch_name?.trim() || undefined,
    source: "task_workdir",
  };
}
