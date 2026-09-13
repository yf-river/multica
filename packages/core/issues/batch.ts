// 批量选中议题时，计算它们共享的状态 / 优先级 / 负责人，供批量操作工具栏展示。
import type {
  Issue,
  IssueStatus,
  IssuePriority,
  IssueAssigneeType,
} from "../types";

/**
 * 一批选中议题共享的负责人。`{ type: null, id: null }` 表示选中的所有议题都“未分配”——
 * 这是一个真实的公共值，区别于“混合选择”（{@link commonIssueFields} 用 `assignee: null` 表示）。
 */
export interface CommonAssignee {
  type: IssueAssigneeType | null;
  id: string | null;
}

/**
 * 批量选择中所有议题共享的状态 / 优先级 / 负责人。
 * 当选择为空、或各议题取值不一致（“混合”）时，对应字段为 `null`。
 * 批量属性选择器用它反映真实的公共值，取值不一时回退到空态（不打勾），而不是硬编码一个默认值。
 */
export interface CommonIssueFields {
  status: IssueStatus | null;
  priority: IssuePriority | null;
  assignee: CommonAssignee | null;
}

/**
 * 返回所有项都相同的那个值；列表为空或各项不一致时返回 `null`。
 * 比较用的是原始值相等，所以调用方需要传入标量 key（复合值要先折成字符串再调用）。
 */
function sharedValue<T>(values: readonly T[]): T | null {
  if (values.length === 0) return null;
  const first = values[0]!;
  return values.every((v) => v === first) ? first : null;
}

const ASSIGNEE_KEY_SEP = "\u0000";

/**
 * 把多形态的负责人（type + id，两者都可能为 null）折成一个可比较的 key，
 * 让“全部未分配”的议题彼此相等、并区别于任何已分配的人员。
 */
function assigneeKey(type: IssueAssigneeType | null, id: string | null): string {
  return `${type ?? ""}${ASSIGNEE_KEY_SEP}${id ?? ""}`;
}

/**
 * 推导所选议题的共同状态 / 优先级 / 负责人。
 * @param issues 已经过滤好的选中议题（即真正被勾选的议题），
 *   与技能列表先按 `selectedIds` 过滤行、再交给批量工具栏的做法保持一致。
 * @returns 各字段的共同值；不一致或为空时为 `null`。
 */
export function commonIssueFields(issues: readonly Issue[]): CommonIssueFields {
  const status = sharedValue(issues.map((i) => i.status));
  const priority = sharedValue(issues.map((i) => i.priority));

  const sharedAssigneeKey = sharedValue(
    issues.map((i) => assigneeKey(i.assignee_type, i.assignee_id)),
  );
  const assignee =
    sharedAssigneeKey !== null && issues.length > 0
      ? { type: issues[0]!.assignee_type, id: issues[0]!.assignee_id }
      : null;

  return { status, priority, assignee };
}
