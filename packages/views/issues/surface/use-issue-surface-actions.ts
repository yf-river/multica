"use client";

// 问题界面的写操作实现 hook：把单条更新、拖拽移动、批量更新/删除、打开创建弹窗
// 等全部封装好，组装成供 actions-context 注入的整组动作，并对失败统一提示。
import { useCallback, useMemo } from "react";
import { toast } from "sonner";
import type { UpdateIssueRequest } from "@multica/core/types";
import {
  useBatchDeleteIssues,
  useBatchUpdateIssues,
  useUpdateIssue,
} from "@multica/core/issues/mutations";
import { errorCode } from "@multica/core/api";
import { useModalStore } from "@multica/core/modals";
import {
  type IssueSurfaceActions,
  type IssueSurfaceMutationOptions,
} from "./actions-context";
import type { IssueCreateDefaults } from "./types";
import { useT } from "../../i18n";

/** 拖拽移动问题时的一次完整写操作：字段 + 相对谁（before_id / after_id）。 */
export type MoveIssueUpdates = Pick<
  UpdateIssueRequest,
  | "status"
  | "assignee_type"
  | "assignee_id"
  | "position"
  | "parent_issue_id"
  | "project_id"
> & {
  before_id: string | null;
  after_id: string | null;
};

/** useIssueSurfaceActions 返回的控制器：整组动作 + 创建弹窗 + 移动方法。 */
export interface IssueSurfaceActionController {
  actions: IssueSurfaceActions;
  openCreateIssue: (defaults?: IssueCreateDefaults) => void;
  moveIssue: (
    issueId: string,
    updates: MoveIssueUpdates,
    onSettled?: () => void,
  ) => void;
}

/**
 * 实现问题界面的整组写操作：单条更新、拖拽移动（带 before/after 定位）、
 * 批量更新/删除、打开创建弹窗，并统一错误提示。
 * @param options.createDefaults - 创建问题弹窗的默认值。
 * @returns 动作集合与创建/移动入口。
 */
export function useIssueSurfaceActions({
  createDefaults,
}: {
  createDefaults: IssueCreateDefaults;
}): IssueSurfaceActionController {
  const { t } = useT("projects");
  const { t: tIssues } = useT("issues");
  const updateIssueMutation = useUpdateIssue();
  const batchUpdateMutation = useBatchUpdateIssues();
  const batchDeleteMutation = useBatchDeleteIssues();

  const updateIssue = useCallback(
    (
      issueId: string,
      updates: Partial<UpdateIssueRequest>,
      options?: IssueSurfaceMutationOptions,
    ) => {
      updateIssueMutation.mutate(
        { id: issueId, ...updates },
        {
          onSuccess: (issue) => options?.onSuccess?.(issue),
          onError: (err) => {
            toast.error(
              errorCode(err) === "revision_conflict"
                ? tIssues(($) => $.revision.conflict)
                : err instanceof Error && err.message
                ? err.message
                : (options?.errorMessage ??
                    t(($) => $.detail.toast_move_issue_failed)),
            );
            options?.onError?.(err);
          },
          onSettled: () => options?.onSettled?.(),
        },
      );
    },
    [t, tIssues, updateIssueMutation],
  );

  const moveIssue = useCallback(
    (
      issueId: string,
      updates: MoveIssueUpdates,
      onSettled?: () => void,
    ) => {
      const { before_id, after_id, ...optimisticUpdates } = updates;
      updateIssueMutation.mutate(
        {
          id: issueId,
          ...optimisticUpdates,
          move_intent: { before_id, after_id },
        },
        {
          onError: (err) => {
            toast.error(
              errorCode(err) === "revision_conflict"
                ? tIssues(($) => $.revision.conflict)
                : err instanceof Error && err.message
                ? err.message
                : t(($) => $.detail.toast_move_issue_failed),
            );
          },
          onSettled,
        },
      );
    },
    [t, tIssues, updateIssueMutation],
  );

  const openCreateIssue = useCallback(
    (defaults?: IssueCreateDefaults) => {
      useModalStore
        .getState()
        .open("create-issue", { ...createDefaults, ...defaults });
    },
    [createDefaults],
  );

  const actions = useMemo<IssueSurfaceActions>(
    () => ({
      isPending:
        updateIssueMutation.isPending ||
        batchUpdateMutation.isPending ||
        batchDeleteMutation.isPending,
      createIssue: openCreateIssue,
      updateIssue,
      moveIssue: (issueId, updates, options) =>
        updateIssue(issueId, updates, {
          errorMessage: t(($) => $.detail.toast_move_issue_failed),
          ...options,
        }),
      batchUpdate: async (issueIds, updates) => {
        await batchUpdateMutation.mutateAsync({ ids: issueIds, updates });
      },
      batchDelete: async (issueIds) => {
        await batchDeleteMutation.mutateAsync(issueIds);
      },
    }),
    [
      batchDeleteMutation,
      batchUpdateMutation,
      openCreateIssue,
      t,
      updateIssue,
      updateIssueMutation.isPending,
    ],
  );

  return { actions, openCreateIssue, moveIssue };
}
