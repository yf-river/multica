// 本文件封装「agent 已完成 issue 数」的查询，唯一消费方是 source-backfill 的
// 触发闸门。

import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import { issueKeys } from "../issues/queries";

/**
 * 当前工作区里被 AI 指派人（agent 或 squad）完成的 issue 数。
 *
 * 唯一消费方是 source-backfill 闸门：弹窗会等用户看过 agent 完成真实工作
 * 之后才问归因问题（阈值 SOURCE_BACKFILL_MIN_AGENT_DONE_ISSUES 在
 * `needs-backfill.ts`）。
 *
 * `limit: 1`，因为只有 `total` 有用——issue 行本身会被丢弃。刻意挂在
 * `issueKeys.all(wsId)` 前缀下：issue 变更与实时事件会失效该前缀，于是
 * agent 完成工作时计数自动刷新、弹窗无需刷新页面即可出现。预期这个查询
 * 只对「还欠一个来源回答」的小群体 enabled，所以额外的重拉不会永远跟着用户。
 *
 * @param wsId - 工作区 id
 * @returns React Query options
 */
export function agentCompletedIssueCountOptions(wsId: string) {
  return queryOptions({
    queryKey: [...issueKeys.all(wsId), "agent-done-count"] as const,
    queryFn: async () => {
      const res = await api.listIssues({
        workspace_id: wsId,
        statuses: ["done"],
        assignee_types: ["agent", "squad"],
        limit: 1,
      });
      return res.total;
    },
    staleTime: 30_000,
  });
}
