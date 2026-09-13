// 本文件提供 Hook：解析「某个界面容器当前打开的已保存视图」，并指出该 id
// 是否已在列表中消失（被删/权限被收）。

"use client";

import { useCallback } from "react";
import { useQuery } from "@tanstack/react-query";
import type { IssueView } from "../api/schemas";
import { issueViewListOptions, type IssueViewScope } from "./queries";
import {
  issueViewContainerKey,
  useActiveIssueViewStore,
} from "./active-view-store";

const EMPTY_VIEWS: IssueView[] = [];

/**
 * 某个界面容器当前打开的已保存视图，与容器的列表查询对齐后返回。
 *
 * `missing` 在「有激活 id、但已落定的列表里不再有它」（被删除 / 权限被收回）
 * 时变为 true——调用方据此退出该视图并弹 toast。
 *
 * @param wsId - 工作区 id
 * @param scope - 视图作用域；为 null 时禁用查询
 */
export function useActiveIssueView(
  wsId: string,
  scope: IssueViewScope | null,
) {
  const containerKey = scope ? issueViewContainerKey(wsId, scope) : "";
  const activeViewId = useActiveIssueViewStore((s) =>
    containerKey ? (s.active[containerKey] ?? null) : null,
  );
  const setActiveInStore = useActiveIssueViewStore((s) => s.setActive);

  const listQuery = useQuery({
    ...issueViewListOptions(wsId, scope ?? { scope_type: "workspace" }),
    enabled: !!scope && !!wsId,
  });
  const views = listQuery.data ?? EMPTY_VIEWS;
  const activeView = activeViewId
    ? (views.find((v) => v.id === activeViewId) ?? null)
    : null;
  // 「缺失」绝不能基于一份 STALE 列表判定：创建之后激活 id 会先于失效重拉的
  // 列表落地，把这段间隙当成「视图被删」会把界面弹回默认标签页。
  const missing =
    !!activeViewId &&
    listQuery.isSuccess &&
    !listQuery.isFetching &&
    !activeView;

  const setActive = useCallback(
    (viewId: string | null) => {
      if (containerKey) setActiveInStore(containerKey, viewId);
    },
    [containerKey, setActiveInStore],
  );

  return {
    containerKey,
    activeViewId,
    activeView,
    views,
    // 偏好写入会按这份列表做「剪枝」——调用方必须等它真正加载完才能持久化
    // 剪枝结果（一份空列表会读成「所有视图都被删了」）。
    viewsReady: listQuery.isSuccess,
    setActive,
    missing,
  };
}
