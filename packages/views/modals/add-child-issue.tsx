// 本文件职责：给某个 issue 添加子 issue 的弹窗，本质是一个预配置好的 issue 选择器。
"use client";

import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  issueDetailOptions,
  childIssuesOptions,
} from "@multica/core/issues/queries";
import { useUpdateIssue } from "@multica/core/issues/mutations";
import { IssuePickerModal } from "./issue-picker-modal";
import { useT } from "../i18n";

/**
 * 「添加子 issue」弹窗。
 *
 * 打开一个 issue 选择器，让用户挑一个已存在的 issue 设为当前 issue 的子 issue。
 * 排除掉不能选的：当前 issue 自己、它的父 issue，以及它已经有的所有子 issue。
 *
 * @param onClose 关闭弹窗的回调
 * @param data    弹窗打开时传入的上下文，这里只取 `issueId`（要挂子 issue 的那个 issue）
 */
export function AddChildIssueModal({
  onClose,
  data,
}: {
  onClose: () => void;
  data: Record<string, unknown> | null;
}) {
  const { t } = useT("modals");
  const issueId = (data?.issueId as string) || "";
  const wsId = useWorkspaceId();
  const updateIssue = useUpdateIssue();

  const { data: issue = null } = useQuery({
    ...issueDetailOptions(wsId, issueId),
    enabled: !!issueId,
  });
  const { data: children = [] } = useQuery({
    ...childIssuesOptions(wsId, issueId),
    enabled: !!issueId,
  });

  // 排除项：自己、父 issue、已有的子 issue，避免选成环或重复挂载。
  const excludeIds = [
    issueId,
    ...(issue?.parent_issue_id ? [issue.parent_issue_id] : []),
    ...children.map((c) => c.id),
  ];

  return (
    <IssuePickerModal
      open
      onOpenChange={(v) => {
        if (!v) onClose();
      }}
      title={t(($) => $.add_child.title)}
      description={t(($) => $.add_child.description)}
      excludeIds={excludeIds}
      onSelect={(selected) => {
        updateIssue.mutate(
          {
            id: selected.id,
            parent_issue_id: issueId,
          },
          {
            onSuccess: () =>
              toast.success(
                t(($) => $.add_child.toast_success, {
                  identifier: selected.identifier,
                }),
              ),
            onError: (err) =>
              toast.error(
                err instanceof Error && err.message
                  ? err.message
                  : t(($) => $.add_child.toast_failed),
              ),
          },
        );
      }}
    />
  );
}
