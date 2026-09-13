// 本文件：thread-utils —— 评论线程（root + 回复树）的纯派生工具。
// 负责把按 parent_id 组织的回复图摊平成时间线顺序、从已解决标记推导线程的
// 解决状态，并统计哪些线程根需要折叠展示。不含组件，供 CommentCard、
// ResolvedThreadBar 等时间线 UI 共用。

import type { TimelineEntry } from "@multica/core/types";
import { sortTimelineEntriesAsc } from "@multica/core/issues/timeline-sort";

/**
 * 从 `rootId` 出发走完 parent_id 图，按"时间先后"（created_at 升序、同刻
 * 用 id 兜底）返回所有后代。CommentCard（渲染展开的线程）与
 * ResolvedThreadBar（显示折叠计数 + 作者列表）共用它，两个视图才始终
 * 同步——一旦存在嵌套回复，"只数直接子节点"的计数就会分叉
 * （见 PR #2300 的 Emacs review）。
 *
 * 是"时间序"而非"深度优先"：agent 回复被迫挂在触发它的评论之下，
 * 若深度优先遍历，慢 agent 的迟到回复会排在更早的兄弟回复*之前*
 *（#3691）。服务端给 agent 读的 --thread 输出本就按时间序
 *（comment.sql 的 ListThreadCommentsForIssue）；这里让 UI 保持同一顺序。
 */
export function collectThreadReplies(
  rootId: string,
  repliesByParent: Map<string, TimelineEntry[]>,
): TimelineEntry[] {
  const out: TimelineEntry[] = [];
  const walk = (id: string) => {
    const children = repliesByParent.get(id) ?? [];
    for (const child of children) {
      out.push(child);
      walk(child.id);
    }
  };
  walk(rootId);
  return sortTimelineEntriesAsc(out);
}

/**
 * 一个线程的解决状态，仅从 `resolved_at` 纯推导。两个用户动作写同一字段：
 *   - "解决线程"在 ROOT 上写 resolved_at → 整条线程折叠；
 *   - "带评论解决"在某条 REPLY 上写 resolved_at → 那条回复就是解决点，
 *     其余回复绕它折叠。
 *
 * 该推导是全函数，UI 永不出现两个解决点，也绝不会因任意组合崩溃
 *（更旧/并发写入可能解决多处）：root 优先；否则取 resolved_at 最新的一条
 * 回复作为"那个"解决点。写入侧无需"清掉其他"——展示时挑一个即可。
 */
export type ThreadResolution =
  | { kind: "none" }
  | { kind: "root" }
  | { kind: "reply"; resolutionId: string };

/**
 * 由 root + 回复推导线程解决状态：root 已解决 → "root"；否则在回复里挑
 * `resolved_at` 最新的那条；都没有 → "none"。
 */
export function deriveThreadResolution(
  root: TimelineEntry,
  replies: TimelineEntry[],
): ThreadResolution {
  if (root.resolved_at) return { kind: "root" };
  let chosen: TimelineEntry | null = null;
  for (const reply of replies) {
    if (!reply.resolved_at) continue;
    if (!chosen || reply.resolved_at > chosen.resolved_at!) chosen = reply;
  }
  return chosen ? { kind: "reply", resolutionId: chosen.id } : { kind: "none" };
}

/**
 * 时间线里所有线程根（顶层评论）的 id —— 逐评论折叠 store 按这些单元折叠。
 * 与 issue-detail 的 `timelineView` 分组用同一个 root/reply 切分法：
 * 没有 `parent_id` 的评论即根。
 */
export function rootCommentIds(entries: readonly TimelineEntry[]): string[] {
  return entries
    .filter((e) => e.type === "comment" && !e.parent_id)
    .map((e) => e.id);
}

/**
 * 带有解决标记（在 root 自身或某条回复上）的线程根 id —— 也就是渲染成
 * 折叠在横条后、直到经 `useResolvedExpandStore` 展开的那些线程。未解决的根
 * 被有意排除：若把它们种进展开集合，之后即便解决了也仍然保持展开。
 */
export function resolvedThreadRootIds(entries: readonly TimelineEntry[]): string[] {
  const roots: TimelineEntry[] = [];
  const repliesByParent = new Map<string, TimelineEntry[]>();
  for (const e of entries) {
    if (e.type !== "comment") continue;
    if (!e.parent_id) {
      roots.push(e);
    } else {
      const list = repliesByParent.get(e.parent_id) ?? [];
      list.push(e);
      repliesByParent.set(e.parent_id, list);
    }
  }
  return roots
    .filter(
      (root) =>
        deriveThreadResolution(root, collectThreadReplies(root.id, repliesByParent)).kind !==
        "none",
    )
    .map((root) => root.id);
}
