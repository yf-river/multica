// 项目任务指标的小工具：从项目行取出任务总数与已完成数。

import type { Project } from "@multica/core/types";

/**
 * 计算某个项目在列表/详情里展示的任务指标。
 *
 * @param project 只需要 issue_count（任务总数）与 done_count（已完成数）的项目
 * @returns 任务总数与已完成数的命名元组
 */
export function getProjectIssueMetrics(
  project: Pick<Project, "issue_count" | "done_count">,
) {
  return {
    totalCount: project.issue_count,
    completedCount: project.done_count,
  };
}
