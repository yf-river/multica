"use client";

// 快速动作 hook：向评论输入框的 `/` 菜单提供当前工作区的快速动作目录
// （渲染为可编辑正文插入，而不是直接执行），并处理渲染失败的回显。
import { useCallback, useMemo, useRef } from "react";
import { toast } from "sonner";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useCurrentWorkspace } from "@multica/core/paths";
import { quickActionListOptions } from "@multica/core/quick-actions";

/**
 * 为评论输入框的 `/` 菜单提供当前工作区的快速动作（MUL-5465）。
 *
 * 选中一个动作是「把渲染后的正文插入进来」而不是「直接运行」——输入框这条路径
 * 存在的意义正是处理「这次不一样」的场景，让用户先编辑、再用常规快捷键发送。
 * 不经审阅直接运行是侧栏按钮该干的事。
 *
 * 菜单提供与侧栏相同的目录，不做权限预过滤。用户选了一个跑不了的动作时，
 * 渲染接口会失败、什么都插不进来，这与侧栏拒绝时的表现一致。
 *
 * 通过 `useCurrentWorkspace`（可空）而非 `useWorkspaceId`（会抛错）读取工作区身份：
 * 评论输入框也会挂载在工作区路由之外，这种增强型功能必须优雅降级成「菜单里没有
 * 快速动作」，绝不能把输入框一起拖垮。
 */
export function useQuickActionMenu(issueId: string) {
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const { data = [] } = useQuery({
    ...quickActionListOptions(wsId),
    enabled: wsId !== "",
  });

  // 输入框扩展在每次按键时通过 getter 读取动作列表；用 ref 保持读取廉价且新鲜，
  // 同时不用重建编辑器实例。
  const actionsRef = useRef<{ id: string; name: string; description?: string }[]>([]);
  actionsRef.current = useMemo(
    () =>
      data
        .filter((a) => a.status === "active")
        .map((a) => ({ id: a.id, name: a.name, description: a.description || undefined })),
    [data],
  );

  const getQuickActions = useCallback(() => actionsRef.current, []);
  const renderQuickAction = useCallback(
    (quickActionId: string) => api.renderQuickAction(issueId, quickActionId),
    [issueId],
  );

  // 输入框扩展运行在 React 树之外，所以把失败回报到这里，而不是自己渲染任何东西。
  // 没有这一步的话，一次失败的选中会把命令文本原样留在输入框里且没有任何解释。
  const onRenderError = useCallback((error: unknown) => {
    toast.error(error instanceof Error ? error.message : String(error));
  }, []);

  return useMemo(
    () => ({ getQuickActions, renderQuickAction, onRenderError }),
    [getQuickActions, renderQuickAction, onRenderError],
  );
}
