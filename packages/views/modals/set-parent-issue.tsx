// 本文件职责：给某个 issue「设置父 issue」的弹窗，本质是一个排除了自身和现有子 issue 的
// issue 选择器，选中后更新该 issue 的父级关系。
"use client";

import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  childIssuesOptions,
} from "@multica/core/issues/queries";
import { useUpdateIssue } from "@multica/core/issues/mutations";
import { IssuePickerModal } from "./issue-picker-modal";
import { useT } from "../i18n";

/**
 * 「设置父 issue」弹窗。
 *
 * 打开一个 issue 选择器，让用户挑一个已存在的 issue 作为当前 issue 的父级。
 * 排除不能选的：当前 issue 自己，以及它现有的所有子 issue（避免形成环）。
 * 选中后调用更新接口改写父级并 toast 提示结果。
 *
 * @param onClose 关闭弹窗的回调
 * @param data    弹窗上下文，只取 `issueId`（要设置父级的那条 issue）
 */
export function SetParentIssueModal({
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

  const { data: children = [] } = useQuery({
    ...childIssuesOptions(wsId, issueId),
    enabled: !!issueId,
  });

  const excludeIds = [issueId, ...children.map((c) => c.id)];

  return (
    <IssuePickerModal
      open
      onOpenChange={(v) => {
        if (!v) onClose();
      }}
      title={t(($) => $.set_parent.title)}
      description={t(($) => $.set_parent.description)}
      excludeIds={excludeIds}
      onSelect={(selected) => {
        updateIssue.mutate(
          {
            id: issueId,
            parent_issue_id: selected.id,
          },
          {
            onSuccess: () =>
              toast.success(
                t(($) => $.set_parent.toast_success, {
                  identifier: selected.identifier,
                }),
              ),
            onError: (err) =>
              toast.error(
                err instanceof Error && err.message
                  ? err.message
                  : t(($) => $.set_parent.toast_failed),
              ),
          },
        );
      }}
    />
  );
}
