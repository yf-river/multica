"use client";

// 问题操作 hook：提供对单个问题的一整套操作（改字段、钉选、复制链接、增删子问题、删除等），供各种菜单复用。
import { useCallback } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import type { Issue, UpdateIssueRequest } from "@multica/core/types";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssuePaths } from "@multica/core/paths";
import { useModalStore } from "@multica/core/modals";
import { useUpdateIssue } from "@multica/core/issues/mutations";
import { useIssueStatuses } from "@multica/core/issue-statuses/hooks";
import { errorCode } from "@multica/core/api";
import { pinListOptions, useCreatePin, useDeletePin } from "@multica/core/pins";
import { copyText } from "@multica/ui/lib/clipboard";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { runConfirmIntent } from "./run-confirm-gate";
import { useIssueSurfaceActionsOptional } from "../surface/actions-context";
import type { IssueSurfaceMutationOptions } from "../surface/actions-context";

/** useIssueActions 返回的操作集合。 */
export interface UseIssueActionsResult {
  /** 当前问题是否已被钉选到侧栏。 */
  isPinned: boolean;
  /** 更新问题字段；可能先弹确认框（触发智能体运行时）。 */
  updateField: (
    updates: Partial<UpdateIssueRequest>,
    options?: IssueSurfaceMutationOptions,
  ) => void;
  /** 在新标签页打开该问题。 */
  openInNewTab: () => void;
  /** 切换钉选状态。 */
  togglePin: () => void;
  /** 复制该问题的分享链接。 */
  copyLink: () => Promise<void>;
  /** 打开「创建子问题」弹窗。 */
  openCreateSubIssue: () => void;
  /** 打开「设置父问题」弹窗。 */
  openSetParent: () => void;
  /** 解除与父问题的关联。 */
  removeParent: () => void;
  /** 打开「添加子问题」弹窗。 */
  openAddChild: () => void;
  /** 打开删除确认弹窗。 */
  openDeleteConfirm: (opts?: { onDeletedFallbackPath?: string }) => void;
}

/**
 * 接受可为 null 的问题，这样调用方可以在因问题缺失而提前 return 之前就调用本 hook。
 * 当 `issue` 为 null 时，返回的所有处理器都是安全的空操作。
 * @param issue - 操作目标问题，可为 null。
 * @returns 一整套问题操作集合。
 */
export function useIssueActions(issue: Issue | null): UseIssueActionsResult {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const paths = useIssuePaths(issue);
  const navigation = useNavigation();
  const user = useAuthStore((s) => s.user);
  const userId = user?.id;

  const { data: pinnedItems = [] } = useQuery({
    ...pinListOptions(wsId, userId ?? ""),
    enabled: !!userId,
  });

  const isPinned =
    !!issue &&
    pinnedItems.some(
      (p) => p.item_type === "issue" && p.item_id === issue.id,
    );

  const updateIssue = useUpdateIssue();
  const surfaceActions = useIssueSurfaceActionsOptional();
  const createPin = useCreatePin();
  const deletePin = useDeletePin();
  const openModal = useModalStore((s) => s.open);

  const issueId = issue?.id ?? null;
  const issueIdentifier = issue?.identifier ?? null;
  const issueProjectId = issue?.project_id ?? null;
  const issueAssigneeType = issue?.assignee_type ?? null;
  const issueAssigneeId = issue?.assignee_id ?? null;
  const { entryOf } = useIssueStatuses(wsId);
  const updateField = useCallback(
    (
      updates: Partial<UpdateIssueRequest>,
      options?: IssueSurfaceMutationOptions,
    ) => {
      if (!issueId) return;
      // 两种可能把工作交给智能体的写操作——给它指派负责人、把它从待办区提升出来——
      // 会先经过共享门槛（runConfirmIntent）弹确认框，所有单问题入口都走这条路。
      // 弹窗自己负责应用改动；其余操作直接应用。
      //
      // 没有接进拖拽和批量工具栏，它们继续直接应用。这是既有的分工而不是新加的：
      // 拖拽是卡片已经移动过的直接操作，批量改状态在 MUL-4155 里被刻意做成了无弹窗。
      const intent = issue && runConfirmIntent(issue, updates, { entryOf });
      if (intent) {
        openModal("issue-run-confirm", intent);
        return;
      }
      if (surfaceActions) {
        surfaceActions.updateIssue(issueId, updates, {
          errorMessage: t(($) => $.detail.update_failed),
          ...options,
        });
      } else {
        updateIssue.mutate(
          { id: issueId, ...updates },
          {
            onSuccess: options?.onSuccess,
            onError: (err) => {
              toast.error(
                errorCode(err) === "revision_conflict"
                  ? t(($) => $.revision.conflict)
                  : err instanceof Error && err.message
                  ? err.message
                  : t(($) => $.detail.update_failed),
              );
              options?.onError?.(err);
            },
            onSettled: () => options?.onSettled?.(),
          },
        );
      }
    },
    [issue, issueId, entryOf, surfaceActions, updateIssue, openModal, t],
  );

  // 明确的「在别处打开」入口。目标是可分享的问题 URL，这样不用第二个应用壳就能在当前浏览器里打开。
  const openInNewTab = useCallback(() => {
    if (!issueId) return;
    // 用标识符形式（和 copyLink 一样）：在 web 上这会变成真实浏览器标签页的可分享 URL，
    // 是用户看得见、也能从地址栏复制出去的链接。如果按 UUID 打开，路由会立刻重写新标签页的 URL。
    const path = paths.issueDetail(issueIdentifier || issueId);
    window.open(
      navigation.getShareableUrl(path),
      "_blank",
      "noopener,noreferrer",
    );
  }, [issueId, issueIdentifier, navigation, paths]);

  const togglePin = useCallback(() => {
    if (!issueId) return;
    if (isPinned) {
      deletePin.mutate({ itemType: "issue", itemId: issueId });
    } else {
      createPin.mutate({ item_type: "issue", item_id: issueId });
    }
  }, [isPinned, issueId, createPin, deletePin]);

  const copyLink = useCallback(async () => {
    if (!issueId) return;
    // 分享标识符形式（`/{ws}/issues/MUL-123`）：粘贴出去的链接应该能让人看出指向哪个问题。
    // UUID 形式依然有效，所以之前复制出去的链接仍能正常解析。
    const url = navigation.getShareableUrl(paths.issueDetail(issueIdentifier || issueId));
    if (await copyText(url)) {
      toast.success(t(($) => $.detail.link_copied));
    } else {
      toast.error(t(($) => $.detail.link_copy_failed));
    }
  }, [paths, issueId, issueIdentifier, navigation, t]);

  const openCreateSubIssue = useCallback(() => {
    if (!issueId) return;
    openModal("create-issue", {
      parent_issue_id: issueId,
      parent_issue_identifier: issueIdentifier,
      ...(issueProjectId ? { project_id: issueProjectId } : {}),
      // 继承父问题的指派人（成员/智能体/小队），这样从「添加子问题」入口创建的子问题
      // 一开始就和父问题同一个负责人（讨论 #1728）。弹窗依据这些字段「是否存在」来判定，
      // 而不是看值，所以传入种子会覆盖它本会回退到的「上次使用的指派人」；
      // 而对未指派的父问题两者都省略，让回退逻辑保持原样。两个字段要一起传——
      // 没有 assignee_id 的 assignee_type 没有意义。
      ...(issueAssigneeType && issueAssigneeId
        ? { assignee_type: issueAssigneeType, assignee_id: issueAssigneeId }
        : {}),
    });
  }, [
    openModal,
    issueId,
    issueIdentifier,
    issueProjectId,
    issueAssigneeType,
    issueAssigneeId,
  ]);

  const openSetParent = useCallback(() => {
    if (!issueId) return;
    openModal("issue-set-parent", { issueId });
  }, [openModal, issueId]);

  // 解除与父问题的关联、提升为独立问题。可逆（设置父问题可重新关联）、非破坏性，
  // 和清除日期类操作一致——所以直接应用而不弹确认框。`stage` 只用于给父问题下的
  // 子问题排序，所以同一次写操作里一并清掉，避免独立问题上残留一个孤零零的值。
  // 成功提示从 onSuccess 里触发，而不是在 mutate() 之后急切触发——否则一次因权限/网络/
  // 校验失败而失败的请求，会在错误提示和乐观回滚之前先闪一下「已移除」（虚假确认）。
  const removeParent = useCallback(() => {
    if (!issueId) return;
    if (surfaceActions) {
      surfaceActions.updateIssue(
        issueId,
        {
          parent_issue_id: null,
          stage: null,
        },
        {
          onSuccess: () =>
            toast.success(t(($) => $.actions.remove_parent_issue_success)),
          errorMessage: t(($) => $.detail.update_failed),
        },
      );
    } else {
      updateIssue.mutate(
        {
          id: issueId,
          parent_issue_id: null,
          stage: null,
        },
        {
          onSuccess: () =>
            toast.success(t(($) => $.actions.remove_parent_issue_success)),
          onError: (err) =>
            toast.error(
              err instanceof Error && err.message
                ? err.message
                : t(($) => $.detail.update_failed),
            ),
        },
      );
    }
  }, [issueId, surfaceActions, updateIssue, t]);

  const openAddChild = useCallback(() => {
    if (!issueId) return;
    openModal("issue-add-child", { issueId });
  }, [openModal, issueId]);

  const openDeleteConfirm = useCallback(
    (opts?: { onDeletedFallbackPath?: string }) => {
      if (!issueId) return;
      openModal("issue-delete-confirm", {
        issueId,
        identifier: issueIdentifier,
        onDeletedFallbackPath: opts?.onDeletedFallbackPath,
      });
    },
    [openModal, issueId, issueIdentifier],
  );

  return {
    isPinned,
    updateField,
    openInNewTab,
    togglePin,
    copyLink,
    openCreateSubIssue,
    openSetParent,
    removeParent,
    openAddChild,
    openDeleteConfirm,
  };
}
