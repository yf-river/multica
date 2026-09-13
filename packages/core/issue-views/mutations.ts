// 本文件封装「已保存视图」的增删改变更：创建、更新、删除。
// 创建是带对话框 + toast 的确证流程，所以不做乐观更新，成功后失效让各界面
// 的选择器重新拉取。

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { IssueView } from "../api/schemas";
import type { CreateIssueViewRequest } from "../api/schemas";
import { issueViewKeys } from "./queries";

/**
 * 创建一个已保存视图。
 *
 * 创建是确证流程（对话框 + toast）——不做乐观更新，等成功后再失效，让每个
 * 界面的选择器都重新拉取。
 */
export function useCreateIssueView(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: CreateIssueViewRequest) => api.createIssueView(data),
    onSuccess: (created) => {
      // 先把它塞进所在作用域的列表、再失效：调用方会立刻激活它，而一个
      // 不在（旧）列表里的 id 会被读成「视图被删了」，把界面弹回默认标签页。
      // 失效仍然会拉取规范的列表。
      if (created) {
        queryClient.setQueryData<IssueView[]>(
          issueViewKeys.list(wsId, {
            scope_type: created.scope_type as "workspace" | "my" | "project",
            scope_id: created.scope_id,
          }),
          (old) =>
            old && !old.some((v) => v.id === created.id)
              ? [...old, created]
              : old,
        );
      }
      void queryClient.invalidateQueries({ queryKey: issueViewKeys.all(wsId) });
    },
  });
}

/** 更新视图所需的输入。 */
export interface UpdateIssueViewInput {
  id: string;
  name?: string;
  visibility?: "private" | "workspace";
  scope_variant?: string | null;
  query?: Record<string, unknown>;
  display?: Record<string, unknown>;
  expected_revision: number;
}

/**
 * 更新一个已保存视图。
 *
 * 设计上不做乐观更新：编辑是确证对话框流程，且 409 冲突路径（别人先改了）
 * 需要服务端的裁决。
 */
export function useUpdateIssueView(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...data }: UpdateIssueViewInput) =>
      api.updateIssueView(id, data),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: issueViewKeys.all(wsId) });
    },
  });
}

/** 删除一个已保存视图，成功后失效视图列表缓存。 */
export function useDeleteIssueView(wsId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteIssueView(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: issueViewKeys.all(wsId) });
    },
  });
}
