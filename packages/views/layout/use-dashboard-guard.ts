"use client";

import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigationStore } from "@multica/core/navigation";
import { useAuthStore } from "@multica/core/auth";
import {
  paths,
  resolvePostAuthDestination,
  useCurrentWorkspace,
} from "@multica/core/paths";
import { workspaceListOptions } from "@multica/core/workspace";
import { useRecentIssuesStore } from "@multica/core/issues/stores";
import { useRecentContextStore } from "@multica/core/chat";
import { useNavigation } from "../navigation";

/**
 * Auth + workspace gate for the dashboard.
 *
 * Redirect logic:
 *  - Auth still loading → wait
 *  - Not logged in → /login
 *  - Logged in but workspace list not yet loaded → wait (don't bounce prematurely)
 *  - Logged in but URL slug doesn't resolve to any workspace → first available
 *    workspace, or workspace creation when the list is empty
 *
 * We read the workspace list query state directly (rather than relying on
 * useCurrentWorkspace's null return) so we can distinguish "list loading"
 * from "slug not found". Otherwise users could see a transient redirect
 * before their workspace list arrives.
 */
export function useDashboardGuard() {
  const { pathname, replace } = useNavigation();
  const user = useAuthStore((s) => s.user);
  const isLoading = useAuthStore((s) => s.isLoading);
  const workspace = useCurrentWorkspace();
  const { data: workspaces = [], isFetched: workspaceListFetched } = useQuery({
    ...workspaceListOptions(),
    enabled: !!user,
  });

  useEffect(() => {
    if (isLoading) return;
    if (!user) {
      replace(paths.login());
      return;
    }
    if (!workspaceListFetched) return;
    if (!workspace) {
      replace(resolvePostAuthDestination(workspaces));
    }
  }, [user, isLoading, workspaceListFetched, workspace, workspaces, replace]);

  useEffect(() => {
    useNavigationStore.getState().onPathChange(pathname);
  }, [pathname]);

  // Drop recent buckets for workspaces the user no longer belongs to.
  // Runs once the workspace list resolves, and again whenever membership
  // changes (workspace deleted, user kicked, user left).
  useEffect(() => {
    if (!workspaceListFetched) return;
    useRecentIssuesStore
      .getState()
      .pruneWorkspaces(workspaces.map((w) => w.id));
    useRecentContextStore
      .getState()
      .pruneWorkspaces(workspaces.map((w) => w.id));
  }, [workspaceListFetched, workspaces]);

  return { user, isLoading, workspace };
}
