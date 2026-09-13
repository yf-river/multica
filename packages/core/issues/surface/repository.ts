// 议题视图仓库：把查询计划解析成具体的 queryOptions。
import type { IssueSurfaceQueryPlan } from "./query-plan";
import { projectGanttIssuesOptions } from "../queries";

/**
 * 把查询计划解析成具体的查询配置。每个列表形态的视图（表格/列表/看板/泳道）
 * 都由服务端拥有的 Table 查询通道供给数据；下面的 Gantt 投影是仅剩的一处按作用域
 * 拉取的查询，它从同一个计划里编译负责人类型的收窄，与其它通道保持一致。
 * @param wsId 工作区 id。
 * @param projectId Gantt 所在的项目 id。
 * @param plan 编译好的作用域查询计划。
 * @returns Gantt 议题的 queryOptions。
 */
export function issueSurfaceGanttOptions(
  wsId: string,
  projectId: string,
  plan: IssueSurfaceQueryPlan,
) {
  return projectGanttIssuesOptions(
    wsId,
    projectId,
    plan.queryFilter.assignee_types,
  );
}
