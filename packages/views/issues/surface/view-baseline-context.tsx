"use client";

// 已打开视图的基线（baseline）上下文：把当前打开的已保存视图的基线条件提供给
// IssueSurface 内部的一切深消费方——看板列菜单、「筛选后为空」状态、泳道等。
// 单独成一个 context，是为了让深处组件不必重新跑 active-view hook（及其列表查询）。
import { createContext, use, type ReactNode } from "react";
import type { IssueViewBaseline } from "@multica/core/issue-views/baseline";

/** 当前已打开视图的基线条件；null 表示没有打开的视图。 */
const ViewBaselineContext = createContext<IssueViewBaseline | null>(null);

/**
 * 提供当前已打开视图的基线上下文。
 * @param props.baseline - 已打开视图的基线条件（可为 null）。
 * @param props.children - 需要消费基线的子内容。
 */
export function ViewBaselineProvider({
  baseline,
  children,
}: {
  baseline: IssueViewBaseline | null;
  children: ReactNode;
}) {
  return (
    <ViewBaselineContext.Provider value={baseline}>
      {children}
    </ViewBaselineContext.Provider>
  );
}

/** 读取当前已打开视图的基线条件；没有打开的视图时返回 null。 */
export function useViewBaseline(): IssueViewBaseline | null {
  return use(ViewBaselineContext);
}
