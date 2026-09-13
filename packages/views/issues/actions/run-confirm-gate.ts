// 运行确认门槛：判断一次问题写操作（指派/提升状态）是否可能触发智能体运行，需要弹确认框时返回确认意图。
import type { Issue, IssueStatusCategory, UpdateIssueRequest } from "@multica/core/types";
import { issueStatusCategory } from "@multica/core/issues";
import { isIssueStatusCategory, type IssueStatusCatalog } from "@multica/core/issue-statuses";

/** 该门槛会读取的问题字段子集。 */
export type GateIssue = Pick<
  Issue,
  "id" | "status" | "status_category" | "assignee_type" | "assignee_id"
>;

/** `issue-run-confirm` 弹窗的载荷；无需确认时为 null。 */
export type RunConfirmIntent =
  | {
      issueIds: [string];
      mode: "assign";
      assigneeType: "agent" | "squad";
      assigneeId: string;
    }
  | {
      issueIds: [string];
      mode: "promote";
      status: string;
      assigneeType: "agent" | "squad";
      assigneeId: string;
    };

/**
 * 解析某个状态 KEY 所属的分类——无法解析时返回 `null`。
 *
 * 这里区分三种状态而不是两种：`catalog.categoryOf` 会把「未知的自定义 key」归并成 `todo`，
 * 这和一个真正的 `todo` 无法区分，而这正是本门槛绝不能做的猜测——它要判断一次写操作
 * 是否会启动智能体，所以解析不出的 key 必须保持未解析，让调用方安全失败。(MUL-6463)
 *
 * 解析顺序与服务端（`issuestatus.Effective`）保持一致：载荷里已带的分类优先，
 * 内置 key 直接用自身作为分类，只有自定义 key 才需要查工作区的状态目录。
 * @param statusKey - 状态 key。
 * @param carriedCategory - 载荷里已带的状态分类。
 * @param catalog - 工作区状态目录（只需 entryOf 方法）。
 * @returns 解析出的分类，或无法解析时为 null。
 */
export function resolveStatusCategory(
  statusKey: string,
  carriedCategory: IssueStatusCategory | undefined,
  catalog: Pick<IssueStatusCatalog, "entryOf">,
): IssueStatusCategory | null {
  const carried = issueStatusCategory({ status: statusKey, status_category: carriedCategory });
  if (carried) return carried;
  const category = catalog.entryOf(statusKey)?.category;
  return category && isIssueStatusCategory(category) ? category : null;
}

/** 提升后不会触发运行的分区集合。 */
const NEVER_STARTS = ["backlog", "done", "cancelled"];

/**
 * 判断一次问题写操作是否需要（以及需要哪种）确认框。
 *
 * 两种会把工作交给智能体的写操作都要确认；其余的直接应用。它是纯函数，
 * 这样每个入口——详情页、右键菜单、表格行——都只依赖这一个结论，而不是各自重新推导 (MUL-6463)。
 *
 * - **assign（指派）**：给问题指定智能体/小队负责人。只有当问题已知停放在待办区时才跳过，
 *   因为指派到待办区永远不会启动运行（`server/internal/service/issue_trigger.go`），
 *   弹窗反而会承诺一件不会发生的事。
 * - **promote（提升）**：把已经有人负责的问题移出待办区。仅这一状态变化就会启动运行
 *   （`RunSourceStatus`），所以同样要弹窗——无论是内置的 `todo` 还是每个自定义的 Todo 类状态。
 *
 * 无法解析的分类一律倾向于「要确认」：用户关掉弹窗只浪费一次点击，
 * 而静默启动会白白跑一次不必要的智能体。
 * @param issue - 被操作的问题（门槛只读其中几个字段）。
 * @param updates - 拟应用的写操作字段。
 * @param catalog - 工作区状态目录（只需 entryOf 方法）。
 * @returns 需要确认时返回对应的确认意图，否则返回 null。
 */
export function runConfirmIntent(
  issue: GateIssue,
  updates: Partial<UpdateIssueRequest>,
  catalog: Pick<IssueStatusCatalog, "entryOf">,
): RunConfirmIntent | null {
  const issueCategory = resolveStatusCategory(issue.status, issue.status_category, catalog);
  const parked = issueCategory === "backlog";

  if (
    (updates.assignee_type === "agent" || updates.assignee_type === "squad") &&
    updates.assignee_id &&
    !parked
  ) {
    return {
      issueIds: [issue.id],
      mode: "assign",
      assigneeType: updates.assignee_type,
      assigneeId: updates.assignee_id,
    };
  }

  const owner = issue.assignee_type;
  if (
    updates.status &&
    updates.status !== issue.status &&
    // 未知分类视为「可能处于待办」：这次写操作可能是在提升，所以要确认。
    (parked || issueCategory === null) &&
    (owner === "agent" || owner === "squad") &&
    issue.assignee_id
  ) {
    const target = resolveStatusCategory(updates.status, undefined, catalog);
    // 无法解析的目标状态同理，也视为「可能处于活动区」。
    if (target === null || !NEVER_STARTS.includes(target)) {
      return {
        issueIds: [issue.id],
        mode: "promote",
        status: updates.status,
        assigneeType: owner,
        assigneeId: issue.assignee_id,
      };
    }
  }

  return null;
}
