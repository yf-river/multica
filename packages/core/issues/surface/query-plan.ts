// 把"议题视图作用域"编译成缓存身份、遗留列表接口参数与新建默认值，作为查询计划。
import type { CreateIssueRequest } from "../../types";
import type { MyIssuesFilter } from "../queries";
import {
  assigneeTypesForActorKind,
  issueScopeKey,
  UnsupportedIssueScopeError,
  type IssueScope,
} from "./scope";

/**
 * 作用域中不被"表格通道（Table）"覆盖的剩余部分。每个列表形态的视图
 * （表格/列表/看板/泳道）的行成员关系都由 surface 控制器编译成
 * IssueTableQuerySpec、并由服务端拥有的 Table 通道来回答——这份计划只携带
 * 该通道覆盖不到的东西：
 *
 * - `scopeKey`: 视图在缓存/持久化里的身份标识。
 * - `queryFilter`: 把作用域表达成遗留列表接口（list-API）的参数，仅供 Gantt
 *   投影使用（它"只看已排期"的窗口在 Table spec 里表达不了）。
 * - `createDefaults`: 在此视图上新建议题时继承的默认字段。
 */
export interface IssueSurfaceQueryPlan {
  scopeKey: string;
  queryFilter: MyIssuesFilter;
  createDefaults: Partial<CreateIssueRequest>;
}

/** 为"我的"关系作用域（assigned/created/involved/all）编译查询计划。 */
function buildMyRelationPlan(
  scope: Extract<IssueScope, { type: "my" }>,
  scopeKey: string,
): IssueSurfaceQueryPlan {
  switch (scope.relation) {
    case "assigned":
      // 我负责的：列表只显示我负责的议题，新建时默认指派给我自己。
      return {
        scopeKey,
        queryFilter: { assignee_id: scope.userId },
        createDefaults: {
          assignee_type: "member",
          assignee_id: scope.userId,
        },
      };
    case "created":
      return {
        scopeKey,
        queryFilter: { creator_id: scope.userId },
        createDefaults: {},
      };
    case "involved":
      return {
        scopeKey,
        queryFilter: { involves_user_id: scope.userId },
        createDefaults: {},
      };
    case "all":
      return { scopeKey, queryFilter: {}, createDefaults: {} };
  }
}

/**
 * 把视图作用域编译成查询计划：scopeKey 用于缓存身份、queryFilter 供 Gantt
 * 等遗留通道使用、createDefaults 决定此视图新建议题的默认归属。
 * @param scope 当前议题视图作用域（工作区/我的/项目/actor/团队）。
 * @returns 编译出的查询计划。
 * @throws 对不受支持的团队（team）作用域抛出 UnsupportedIssueScopeError。
 */
export function buildIssueSurfaceQueryPlan(
  scope: IssueScope,
): IssueSurfaceQueryPlan {
  const scopeKey = issueScopeKey(scope);

  switch (scope.type) {
    case "workspace": {
      // 按底部"成员/Agent"标签收窄负责人类型；"全部"标签不传该参数（即不限）。
      const assigneeTypes = assigneeTypesForActorKind(scope.actorKind);
      return {
        scopeKey,
        queryFilter: assigneeTypes ? { assignee_types: assigneeTypes } : {},
        createDefaults: {},
      };
    }
    case "project": {
      const assigneeTypes = assigneeTypesForActorKind(scope.actorKind);
      return {
        scopeKey,
        queryFilter: assigneeTypes
          ? { project_id: scope.projectId, assignee_types: assigneeTypes }
          : { project_id: scope.projectId },
        // 在项目视图里新建议题，默认归属该项目。
        createDefaults: { project_id: scope.projectId },
      };
    }
    case "my":
      return buildMyRelationPlan(scope, scopeKey);
    case "actor":
      return {
        scopeKey,
        // actor 视图只看"指派给该 actor"或"由该 actor 创建"两种关系。
        queryFilter:
          scope.relation === "assigned"
            ? { assignee_id: scope.actorId }
            : { creator_id: scope.actorId },
        createDefaults:
          scope.relation === "assigned"
            ? {
                assignee_type: scope.actorType,
                assignee_id: scope.actorId,
              }
            : {},
      };
    case "team":
      throw new UnsupportedIssueScopeError(scope, "issue surface query plan");
  }
}
