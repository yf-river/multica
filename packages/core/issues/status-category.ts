// 把议题的“状态 key”解析到“状态分类”，并处理列归属、补丁归一化、过滤列解析等逻辑。
import type { Issue, IssueStatusCategory } from "../types";
import { isIssueStatusCategory } from "../issue-statuses";
import type { IssueStatusCatalog } from "../issue-statuses";
import { ALL_STATUSES } from "./config";

/**
 * 议题的状态属于哪个分类——也就是它在看板上占的桶（MUL-6243）。
 *
 * 特意做成纯函数：调用它的缓存辅助函数运行在 React 之外，不能去取目录。
 * 服务器下发了 `status_category` 就读它；否则回退到“让该字段可空”的那条规则——
 * 内置状态 key 本身就是它自己的分类。由于自定义状态只有在管理员创建后才存在，
 * 所以对任何没有自定义状态的工作区，这个回退都是精确的。
 *
 * 当状态是“该响应没解析出来的自定义 key”时返回 null，让调用方跳过入桶，
 * 而不是猜一个错误的列。
 */
export function issueStatusCategory(issue: Pick<Issue, "status" | "status_category">): IssueStatusCategory | null {
  const fromServer = issue.status_category;
  if (fromServer && isIssueStatusCategory(fromServer)) return fromServer;
  if (isIssueStatusCategory(issue.status)) return issue.status;
  return null;
}

/**
 * 只拿到“状态 key 字符串”的渲染路径，用它求分类。
 *
 * 对 8 个内置状态是精确的（在管理员定义自定义状态之前，它们就是全部状态）。
 * 自定义 key 返回 `todo`，让展示查找总能解析出一个可渲染的值；必须显示真实状态的
 * 视图改用目录（`useIssueStatuses`）。（MUL-6243）
 */
export function statusCategoryOfKey(statusKey: string): IssueStatusCategory {
  return isIssueStatusCategory(statusKey) ? statusKey : "todo";
}

/**
 * 议题在“看板/列表/泳道”里渲染到哪一列——永远有答案，绝不返回 null（MUL-6409）。
 *
 * 列是分类，而 `issue.status` 是具体的 KEY。用 key 对着“分类列”去入桶，
 * 正是自定义状态让卡片“消失”的根源：`status:awaiting_response` 匹配不到任何列 id，
 * 于是服务器正确返回的行被丢在地上。按该状态过滤看板会让它整个消失——唯一可见的那列
 * 里每张卡都是自定义状态，于是列渲染成空，旁边的表头计数却是非零。
 *
 * 未解析的自定义 key 回退到 `todo` 而不是“无处可去”：一张落在可能错误列里的卡片
 * 还能被找回，而一张不在任何列的卡片是看不见的。实际上这个回退根本到不了——
 * 服务器在每个议题载荷里都下发分类，而内置 key 本身就是它自己的分类。
 */
export function issueColumnCategory(
  issue: Pick<Issue, "status" | "status_category">,
): IssueStatusCategory {
  return issueStatusCategory(issue) ?? statusCategoryOfKey(issue.status);
}

/**
 * 在补丁进入任何缓存之前，把它的 `status_category` 改写成与 `status` 一致（MUL-6243）。
 *
 * 服务器现在每个议题都下发分类，所以缓存实体形如 `{status: "todo", status_category: "todo"}`。
 * 乐观补丁只带 `{status: "done"}`，裸 `{...issue, ...patch}` 会让卡片移到 done 桶时
 * 还留着过期的 `status_category: "todo"`。单条更新在完整服务器响应落地时会自愈，
 * 但批量 API 只返回 `{updated: n}`、不会重取分桶列表——所以没有这一步，实体会和它
 * 所坐的桶永久不一致，下一次窗口外计数会减错桶。
 *
 * 不涉及 `status` 的补丁原样返回。没有权威分类的自定义 key 也原样保留：它无法解析，
 * `patchNeedsInvalidation` 会把它导向重取而不是猜测——但继承来的过期值会被丢弃，
 * 避免下游信任它。
 */
export function normalizeStatusPatch(patch: Partial<Issue>): Partial<Issue> {
  if (patch.status === undefined) return patch;
  const category = issueStatusCategory({
    status: patch.status,
    status_category: patch.status_category,
  });
  // 用 undefined 而不是继承值：无法解析的状态绝不能静默保留旧分类。
  return { ...patch, status_category: category ?? undefined };
}

/**
 * 精确的状态 key 过滤如何解析成看板/列表的“列”（MUL-6243）。
 *
 * 是三种状态，不是两种。内置 key 完全不需要目录就能解析，因为内置 key 本身就是
 * 它自己的分类。自定义 key 则不行——而 `categoryOf` 对任何没加载到的东西都答 `todo`，
 * 这与真正的 `todo` 无法区分。按这个猜测去路由，会让一个保存的 `qa` 过滤器去翻 todo 列，
 * 而查询仍然限制 `status=qa`。
 *
 * 对这种情况返回空列集合同样错：调用方分不清“收窄到空”和“暂时答不了”，
 * 于是它不拉任何分支，渲染出一个既没有 spinner 也没有报错的空看板。所以才有了
 * 显式的状态：
 *
 * - `resolved` — 每个 key 都有答案；`columns` 是权威结果。
 * - `pending`  — 有自定义 key 需要一个仍在飞行中的目录。保持视图的加载态；
 *                不要拉取、也不要渲染成空。
 * - `error`    — 目录请求失败。显示一个可重试的错误；没有目录就无法兑现自定义过滤。
 */
export type StatusFilterColumnsResult =
  | { state: "resolved"; columns: Set<IssueStatusCategory> }
  | { state: "pending" }
  | { state: "error" };

/**
 * 把一组状态 key 过滤解析成对应的分类列集合。
 * @param statusFilters 状态 key 列表（可能含自定义 key）。
 * @param catalog 状态目录，提供按 key 查分类的能力及加载/错误状态。
 * @returns 解析结果：resolved（含列集合）、pending 或 error 三种之一。
 */
export function statusFilterColumns(
  statusFilters: readonly string[],
  catalog: Pick<IssueStatusCatalog, "entryOf" | "isLoaded" | "isPending" | "isError">,
): StatusFilterColumnsResult {
  const columns = new Set<IssueStatusCategory>();
  for (const key of statusFilters) {
    if (isIssueStatusCategory(key)) {
      columns.add(key);
      continue;
    }
    // 自定义 key。没有权威目录就没有诚实的答案。
    if (catalog.isError) return { state: "error" };
    if (!catalog.isLoaded) return { state: "pending" };
    const category = catalog.entryOf(key)?.category;
    // 一个“已加载”却不认识该 key 的目录同样是权威的：这个状态被删了，
    // 或者属于别的工作区。不贡献任何列就是 resolved 的答案，而不是 pending。
    if (category && ALL_STATUSES.includes(category)) columns.add(category);
  }
  return { state: "resolved", columns };
}

/**
 * 判断一个议题是否“表现为”给定分类（MUL-6243）。
 *
 * 这是每个与状态耦合的产品规则真正会问的问题。把 `issue.status` 和内置 key 比较，
 * 只能对没有自定义状态的工作区给出正确答案：一个属于 `done` 分类的自定义状态也算完成，
 * 而写 `status === "done"` 的代码会悄悄得出相反结论。
 *
 * 未解析的自定义 key 答 `false`，这个方向是刻意的——它的每个调用方都因此“安全失败”。
 * “算不算 done/cancelled？”答 false 会让行保持可见而不是被隐藏；“算不算 backlog？”
 * 答 false 会保留 agent 运行确认而不是跳过它。往另一个方向猜，则会隐藏工作，
 * 或在不经询问的情况下启动 agent。
 */
export function issueBehavesAs(
  issue: Pick<Issue, "status" | "status_category">,
  category: IssueStatusCategory,
): boolean {
  return issueStatusCategory(issue) === category;
}

/** 当议题表现为给定分类中的任意一个时返回 true。 */
export function issueBehavesAsAny(
  issue: Pick<Issue, "status" | "status_category">,
  categories: readonly IssueStatusCategory[],
): boolean {
  const resolved = issueStatusCategory(issue);
  return resolved !== null && categories.includes(resolved);
}
