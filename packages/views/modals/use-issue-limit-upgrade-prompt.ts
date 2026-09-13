// 本文件职责：暴露一个「弹出 issue 上限恢复对话框」的 hook，让创建流程在遇到
// issue 上限错误时调用；只弹恢复对话框、不关闭当前编辑中的草稿。
"use client";

import { useCallback } from "react";
import { useWorkspaceId } from "@multica/core/hooks";
import { useModalStore } from "@multica/core/modals";

/**
 * 返回一个打开「共享 issue 上限恢复对话框」的函数。
 *
 * 在创建接口返回 issue_limit_reached 错误时调用即可；只唤起恢复对话框，
 * 不会关闭当前正在编辑的草稿，便于用户处理完上限问题后继续。
 */
export function useIssueLimitUpgradePrompt(): () => void {
  const wsId = useWorkspaceId();

  return useCallback(() => {
    useModalStore.getState().showIssueLimitRecovery(wsId);
  }, [wsId]);
}
