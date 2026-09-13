// 议题视图作用域：描述"当前在看哪个议题集合"，并在作用域与展示标签/缓存键之间互转。
import type { IssueAssigneeType } from "../../types";

/** 工作区/项目级视图里的底部标签类型："全部"、只看成员、只看 Agent。 */
export type WorkspaceIssueActorKind = "all" | "members" | "agents";

/**
 * 议题视图作用域的联合类型：工作区（可再按成员/Agent 收窄）、我的（all/assigned/
 * created/involved）、项目、某个具体 actor（成员或 Agent 的 assigned/created）、团队。
 */
export type IssueScope =
  | { type: "workspace"; actorKind?: WorkspaceIssueActorKind }
  | {
      type: "my";
      relation: "all" | "assigned" | "created" | "involved";
      userId: string;
    }
  | { type: "project"; projectId: string; actorKind?: WorkspaceIssueActorKind }
  | {
      type: "actor";
      actorType: Extract<IssueAssigneeType, "member" | "agent">;
      actorId: string;
      relation: "assigned" | "created";
    }
  | { type: "team"; teamId: string };

/**
 * UI 上粗粒度的"负责人类型标签"与 API 的 `assignee_types` 取值之间唯一的翻译处。
 * 所有通道（列表 GET 参数、表格 query spec、Gantt）都必须经由这个函数编译该标签——
 * 不要把下面的字面量数组内联到别的地方去。
 * @param actorKind "全部 / 成员 / Agent"标签；`undefined` 或 "all" 表示不限类型。
 * @returns 对应的 assignee_type 取值；不限类型时返回 `undefined`（不传该参数）。
 */
export function assigneeTypesForActorKind(
  actorKind: WorkspaceIssueActorKind | undefined,
): IssueAssigneeType[] | undefined {
  switch (actorKind) {
    case "members":
      return ["member"];
    case "agents":
      // Agent 与 squad（Agent 小组）都归到 Agent 一类。
      return ["agent", "squad"];
    default:
      return undefined;
  }
}

/** 把已保存视图的 scope_variant 映射回工作区标签；未知/缺省值按"全部"处理。 */
export function actorKindForViewVariant(
  variant: string | null | undefined,
): WorkspaceIssueActorKind {
  return variant === "members" || variant === "agents" ? variant : "all";
}

/** 把 My 视图的 scope_variant 映射成 my 关系；未知/缺省值按 "all" 处理。 */
export function myRelationForViewVariant(
  variant: string | null | undefined,
): Extract<IssueScope, { type: "my" }>["relation"] {
  return variant === "assigned" || variant === "created" || variant === "involved"
    ? variant
    : "all";
}

/** 对某个不受支持的作用域执行操作时抛出的错误。 */
export class UnsupportedIssueScopeError extends Error {
  constructor(scope: IssueScope, operation: string) {
    super(`Issue scope "${issueScopeKey(scope)}" is not supported for ${operation}.`);
    this.name = "UnsupportedIssueScopeError";
  }
}

/** 把作用域编译成一个字符串，作为缓存身份与持久化展示状态的 key。 */
export function issueScopeKey(scope: IssueScope): string {
  switch (scope.type) {
    case "workspace":
      return `workspace:${scope.actorKind ?? "all"}`;
    case "my":
      return `my:${scope.userId}:${scope.relation}`;
    case "project":
      // "全部"标签沿用历史 key，让已持久化的展示状态存活下来；成员/Agent
      // 标签各自独立 key（也因此有各自的按标签展示状态），与工作区标签对齐。
      return scope.actorKind === "members" || scope.actorKind === "agents"
        ? `project:${scope.projectId}:${scope.actorKind}`
        : `project:${scope.projectId}`;
    case "actor":
      return `actor:${scope.actorType}:${scope.actorId}:${scope.relation}`;
    case "team":
      return `team:${scope.teamId}`;
  }
}
