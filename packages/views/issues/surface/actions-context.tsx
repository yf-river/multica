"use client";

// 「问题界面的写操作」上下文：把 surface 层实现好的整组增删改动作通过 Context 提供给
// 界面内各处的菜单/卡片使用。没有宿主挂载时，调用方走各自独立的 mutation 兜底。
import { createContext, useContext, type ReactNode } from "react";
import type { Issue, UpdateIssueRequest } from "@multica/core/types";
import type { IssueCreateDefaults } from "./types";

/** 一次问题写操作的通用回调选项（错误提示与成功/失败/落定钩子）。 */
export type IssueSurfaceMutationOptions = {
  errorMessage?: string;
  onSuccess?: (issue: Issue) => void;
  onError?: (err: unknown) => void;
  onSettled?: () => void;
};

/** IssueSurface 向上下文注入的全部写操作能力。 */
export interface IssueSurfaceActions {
  isPending: boolean;
  /** 打开「创建问题」弹窗，可按需带默认字段。 */
  createIssue: (defaults?: IssueCreateDefaults) => void;
  /** 更新单个问题的字段。 */
  updateIssue: (
    issueId: string,
    updates: Partial<UpdateIssueRequest>,
    options?: IssueSurfaceMutationOptions,
  ) => void;
  /** 移动单个问题（带 before/after 定位信息）。 */
  moveIssue: (
    issueId: string,
    updates: Partial<UpdateIssueRequest>,
    options?: IssueSurfaceMutationOptions,
  ) => void;
  /** 批量更新多个问题。 */
  batchUpdate: (
    issueIds: string[],
    updates: Partial<UpdateIssueRequest>,
  ) => Promise<{ updated: number } | void>;
  /** 批量删除多个问题。 */
  batchDelete: (issueIds: string[]) => Promise<void>;
}

const IssueSurfaceActionsContext = createContext<IssueSurfaceActions | null>(
  null,
);

/**
 * 提供问题界面写操作上下文。
 * @param props.actions - surface 层实现的整组写操作。
 * @param props.children - 需要消费这些写操作的子内容。
 */
export function IssueSurfaceActionsProvider({
  actions,
  children,
}: {
  actions: IssueSurfaceActions;
  children: ReactNode;
}) {
  return (
    <IssueSurfaceActionsContext.Provider value={actions}>
      {children}
    </IssueSurfaceActionsContext.Provider>
  );
}

/**
 * 读取问题界面写操作上下文；未被任何宿主包裹时返回 null，
 * 调用方据此回退到独立的 mutation 路径。
 */
export function useIssueSurfaceActionsOptional() {
  return useContext(IssueSurfaceActionsContext);
}
