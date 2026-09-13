// 本文件提供「当前工作区 slug」的 Context 与相关 Hook：从 URL 取 slug、按
// slug 解析工作区对象、绑定到当前工作区的路径构造器。

"use client";

import { createContext, use, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import type { Workspace } from "../types";
import { workspaceListOptions } from "../workspace/queries";
import { paths, type WorkspacePaths } from "./paths";

/**
 * 当前工作区 slug 的 Context（由平台层从 URL 读出）。
 *
 * apps/web 在 [workspaceSlug]/layout.tsx 里用 Next.js 的 `params.workspaceSlug`
 * 填充它。
 *
 * packages/core/ 不能直接 import next/navigation 或 react-router-dom，所以
 * slug 经这个 Context 送达——与 WorkspaceIdProvider 对工作区 id 的处理一致。
 */
const WorkspaceSlugContext = createContext<string | null>(null);

/** 把当前 slug 提供给子树。 */
export function WorkspaceSlugProvider({
  slug,
  children,
}: {
  slug: string | null;
  children: ReactNode;
}) {
  return (
    <WorkspaceSlugContext.Provider value={slug}>
      {children}
    </WorkspaceSlugContext.Provider>
  );
}

/** 从 URL 读当前工作区 slug；在工作区范围路由之外调用返回 null。 */
export function useWorkspaceSlug(): string | null {
  return use(WorkspaceSlugContext);
}

/** 同 useWorkspaceSlug，但在工作区路由之外调用会抛错。 */
export function useRequiredWorkspaceSlug(): string {
  const slug = useWorkspaceSlug();
  if (!slug) {
    throw new Error(
      "useRequiredWorkspaceSlug called outside a workspace-scoped route",
    );
  }
  return slug;
}

/**
 * 当前选中的工作区，由 URL slug + React Query 列表推导。
 * slug 缺失或列表里没有匹配工作区时返回 null。
 *
 * @returns 匹配的工作区对象或 null
 */
export function useCurrentWorkspace(): Workspace | null {
  const slug = useWorkspaceSlug();
  const { data: list = [] } = useQuery(workspaceListOptions());
  if (!slug) return null;
  return list.find((w) => w.slug === slug) ?? null;
}

/**
 * 绑定到当前工作区的路径构造器。在工作区路由之外调用会抛错——跨工作区的链接
 * 请直接用 paths.workspace(slug)。
 */
export function useWorkspacePaths(): WorkspacePaths {
  const slug = useRequiredWorkspaceSlug();
  return paths.workspace(slug);
}
