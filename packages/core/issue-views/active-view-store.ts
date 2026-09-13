// 本文件记录「每个界面容器当前打开的是哪个已保存视图」，并派生视图查询里
// 锁定的过滤维度。

"use client";

import { create } from "zustand";
import type { FilterDimension } from "../issues/stores/view-store";
import type { IssueViewScope } from "./queries";

/**
 * 为「每个界面容器当前打开的视图」生成存储 key：`${wsId}:${scope_type}
 * [:${scope_id}]`。
 *
 * @param wsId - 工作区 id
 * @param scope - 视图作用域
 * @returns 容器的稳定 key
 */
export function issueViewContainerKey(wsId: string, scope: IssueViewScope): string {
  return scope.scope_id
    ? `${wsId}:${scope.scope_type}:${scope.scope_id}`
    : `${wsId}:${scope.scope_type}`;
}

/** store 内部状态：容器 key → 当前激活的视图 id。 */
interface ActiveIssueViewState {
  active: Record<string, string>;
  setActive: (containerKey: string, viewId: string | null) => void;
}

/**
 * 「当前激活视图」的客户端/视图状态（按仓库状态规则用 zustand）：不持久化——
 * Web 端以 URL 的 `?view=` 参数作为持久载体；从存储里复活一个已被删除的视图
 * 会把用户困在不存在的东西上。
 */
export const useActiveIssueViewStore = create<ActiveIssueViewState>((set) => ({
  active: {},
  setActive: (containerKey, viewId) =>
    set((state) => {
      const active = { ...state.active };
      if (viewId) active[containerKey] = viewId;
      else delete active[containerKey];
      return { active };
    }),
}));

/**
 * 被某视图已保存查询「固定」住的维度。
 *
 * 这些维度渲染成锁定的 chip（没有移除入口），对应区块在过滤器菜单里被禁用
 * ——视图的身份不允许原地修改，只能在它周围继续收窄。
 *
 * @param query - 已保存视图的查询
 * @returns 被锁定的维度集合（属性维度形态为 `property:${定义id}`）
 */
export function lockedDimensionsFromQuery(
  query: Record<string, unknown>,
): Set<FilterDimension> {
  const locked = new Set<FilterDimension>();
  const nonEmptyArray = (v: unknown) => Array.isArray(v) && v.length > 0;
  if (nonEmptyArray(query.statusFilters)) locked.add("status");
  if (nonEmptyArray(query.priorityFilters)) locked.add("priority");
  if (nonEmptyArray(query.assigneeFilters) || query.includeNoAssignee === true) {
    locked.add("assignee");
  }
  if (nonEmptyArray(query.creatorFilters)) locked.add("creator");
  if (nonEmptyArray(query.projectFilters) || query.includeNoProject === true) {
    locked.add("project");
  }
  if (nonEmptyArray(query.labelFilters)) locked.add("label");
  const propertyFilters = query.propertyFilters;
  if (propertyFilters && typeof propertyFilters === "object") {
    for (const [id, selected] of Object.entries(propertyFilters as Record<string, unknown>)) {
      if (nonEmptyArray(selected)) locked.add(`property:${id}`);
    }
  }
  return locked;
}
