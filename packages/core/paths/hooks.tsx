"use client";

import { createContext, use, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import type { Workspace } from "../types";
import { workspaceListOptions } from "../workspace/queries";
import { paths, type WorkspacePaths } from "./paths";

/**
 * Context for the current workspace slug (read from URL by the platform layer).
 *
 * apps/web populates this from Next.js `params.workspaceSlug` in
 * [workspaceSlug]/layout.tsx.
 *
 * packages/core/ cannot import next/navigation or react-router-dom directly,
 * so the platform adapters provide the slug through this narrow Context.
 */
const WorkspaceSlugContext = createContext<string | null>(null);

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

/** Current workspace slug from URL, or null outside workspace-scoped routes. */
export function useWorkspaceSlug(): string | null {
  return use(WorkspaceSlugContext);
}

/** Same as useWorkspaceSlug, but throws if called outside a workspace route. */
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
 * The currently-selected workspace, derived from URL slug + React Query list.
 * Returns null if slug is missing or doesn't match any workspace in the list.
 */
export function useCurrentWorkspace(): Workspace | null {
  const slug = useWorkspaceSlug();
  const { data: list = [] } = useQuery(workspaceListOptions());
  if (!slug) return null;
  return list.find((w) => w.slug === slug) ?? null;
}

/** Current workspace UUID, derived from the route slug and workspace list. */
export function useWorkspaceId(): string {
  const workspace = useCurrentWorkspace();
  if (!workspace) {
    throw new Error(
      "useWorkspaceId called outside a resolved workspace route",
    );
  }
  return workspace.id;
}

/**
 * Path builder bound to the current workspace. Throws if called outside a
 * workspace route — for cross-workspace links use paths.workspace(slug) directly.
 */
export function useWorkspacePaths(): WorkspacePaths {
  const slug = useRequiredWorkspaceSlug();
  return paths.workspace(slug);
}
