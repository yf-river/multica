"use client";

// 问题反应 hook：读取问题的表情反应列表，支持乐观切换反应，并通过 WS 同步他人操作。
import { useCallback, useMemo } from "react";
import { useQuery, useQueryClient, useMutationState } from "@tanstack/react-query";
import type { IssueReaction } from "@multica/core/types";
import type {
  IssueReactionAddedPayload,
  IssueReactionRemovedPayload,
} from "@multica/core/types";
import { issueReactionsOptions, issueKeys } from "@multica/core/issues/queries";
import { useToggleIssueReaction, type ToggleIssueReactionVars } from "@multica/core/issues/mutations";
import { useWSEvent, useWSReconnect } from "@multica/core/realtime";

/**
 * 管理某个问题的表情反应：读取列表、乐观地切换反应，并实时同步他人操作。
 * @param issueId - 问题 id。
 * @param userId - 当前用户 id（用于区分「我的」反应）。
 * @returns 反应列表、加载状态和切换反应的方法。
 */
export function useIssueReactions(issueId: string, userId?: string) {
  const qc = useQueryClient();
  const { data: serverReactions = [], isLoading: loading } = useQuery(
    issueReactionsOptions(issueId),
  );

  const toggleMutation = useToggleIssueReaction(issueId);

  // 断线重连后的恢复：让查询失效，下次渲染时重新拉取。
  useWSReconnect(
    useCallback(() => {
      qc.invalidateQueries({ queryKey: issueKeys.reactions(issueId) });
    }, [qc, issueId]),
  );

  // --- WS 事件处理器（把其他用户的操作同步进服务端缓存）---

  useWSEvent(
    "issue_reaction:added",
    useCallback(
      (payload: unknown) => {
        const { reaction, issue_id } = payload as IssueReactionAddedPayload;
        if (issue_id !== issueId) return;
        qc.setQueryData<IssueReaction[]>(
          issueKeys.reactions(issueId),
          (old) => {
            if (!old) return old;
            if (old.some((r) => r.id === reaction.id)) return old;
            return [...old, reaction];
          },
        );
      },
      [qc, issueId],
    ),
  );

  useWSEvent(
    "issue_reaction:removed",
    useCallback(
      (payload: unknown) => {
        const p = payload as IssueReactionRemovedPayload;
        if (p.issue_id !== issueId) return;
        qc.setQueryData<IssueReaction[]>(
          issueKeys.reactions(issueId),
          (old) =>
            old?.filter(
              (r) =>
                !(
                  r.emoji === p.emoji &&
                  r.actor_type === p.actor_type &&
                  r.actor_id === p.actor_id
                ),
            ),
        );
      },
      [qc, issueId],
    ),
  );

  // --- 乐观 UI 推导 ---
  // 不把临时数据写进缓存（那样会跟 WS 事件竞争），而是在渲染时根据进行中的
  // mutation 参数推导出乐观状态。

  const pendingVars = useMutationState({
    filters: {
      mutationKey: ["toggleIssueReaction", issueId],
      status: "pending",
    },
    select: (m) =>
      m.state.variables as ToggleIssueReactionVars | undefined,
  });

  const reactions = useMemo(() => {
    if (pendingVars.length === 0) return serverReactions;

    let result = [...serverReactions];
    for (const vars of pendingVars) {
      if (!vars) continue;
      if (vars.existing) {
        // 待移除的反应：先从结果里剔掉它。
        result = result.filter((r) => r.id !== vars.existing!.id);
      } else {
        // 待添加的反应——如果服务端缓存里已经有了（WS 先到），就跳过。
        const alreadyExists = result.some(
          (r) =>
            r.emoji === vars.emoji &&
            r.actor_type === "member" &&
            r.actor_id === userId,
        );
        if (!alreadyExists) {
          result = [
            ...result,
            {
              id: `optimistic-${vars.emoji}`,
              issue_id: issueId,
              actor_type: "member",
              actor_id: userId ?? "",
              emoji: vars.emoji,
              created_at: "",
            },
          ];
        }
      }
    }
    return result;
  }, [serverReactions, pendingVars, issueId, userId]);

  // --- 切换反应 ---

  const toggleReaction = useCallback(
    async (emoji: string) => {
      if (!userId) return;
      const existing = serverReactions.find(
        (r) =>
          r.emoji === emoji &&
          r.actor_type === "member" &&
          r.actor_id === userId,
      );
      toggleMutation.mutate({ emoji, existing });
    },
    [userId, serverReactions, toggleMutation],
  );

  return { reactions, loading, toggleReaction };
}
