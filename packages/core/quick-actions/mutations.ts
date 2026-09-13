// 本文件封装「快捷动作」的各类变更：增删改目录项，以及「对某个 issue 执行
// 一次快捷动作」。

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { quickActionKeys } from "./queries";
import { useWorkspaceId } from "../hooks";
import { issueKeys } from "../issues/queries";
import type {
  Comment,
  CreateQuickActionRequest,
  UpdateQuickActionRequest,
} from "../types";

/**
 * 目录变更。没有一个带乐观更新：它们发生在设置表单里，一次往返完全可以接受；
 * 而且反正服务端会做值规范化（位置分配、prompt 裁剪、可见性派生）——乐观插入
 * 的一行会在落定前显示错误的可见性徽标。
 */
export function useCreateQuickAction() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (data: CreateQuickActionRequest) => api.createQuickAction(data),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: quickActionKeys.all(wsId) });
    },
  });
}

/** 更新一条快捷动作。 */
export function useUpdateQuickAction() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, ...data }: { id: string } & UpdateQuickActionRequest) =>
      api.updateQuickAction(id, data),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: quickActionKeys.all(wsId) });
    },
  });
}

/** 删除一条快捷动作。 */
export function useDeleteQuickAction() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.deleteQuickAction(id),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: quickActionKeys.all(wsId) });
    },
  });
}

/**
 * 对某个 issue 执行一条快捷动作。
 *
 * 返回创建的评论以及它的 `trigger_outcomes`，调用方据此区分 `queued`
 * （一次运行已启动）、`coalesced`（合并进该目标已有的待处理任务）与 `blocked`。
 * 这个区分就是全部意义：数据库对每个 (issue, agent) 只允许一个待处理任务，
 * 所以第二次点击真的不会启动第二次运行，UI 必须如实说明而不能假装它启动了。
 *
 * 会失效该 issue 的评论与任务运行缓存，让新卡片和执行日志条目无需等待一次
 * websocket 往返即可出现。
 */
export function useRunQuickAction(issueId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation<Comment, Error, { quickActionId: string }>({
    mutationFn: ({ quickActionId }) => api.runQuickAction(issueId, quickActionId),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: issueKeys.timeline(issueId) });
      qc.invalidateQueries({ queryKey: issueKeys.tasks(issueId) });
      // 运行成功时服务端已更新用量计数与 last_used_at。
      qc.invalidateQueries({ queryKey: quickActionKeys.all(wsId) });
    },
  });
}
