"use client";

import type { ReactNode } from "react";
import { useDashboardGuard } from "./use-dashboard-guard";

interface DashboardGuardProps {
  children: ReactNode;
  /** Rendered when auth or workspace is loading */
  loadingFallback?: ReactNode;
}

/**
 * Shared guard for dashboard layouts.
 *
 * Handles: auth check → workspace check → render children.
 * Both web and desktop layouts compose their own UI structure inside this.
 *
 * The guard waits for the route slug to resolve to a workspace before children
 * render, so downstream components can safely call useWorkspaceId().
 */
export function DashboardGuard({
  children,
  loadingFallback = null,
}: DashboardGuardProps) {
  const { user, isLoading, workspace } = useDashboardGuard();

  if (isLoading || !workspace) return <>{loadingFallback}</>;
  if (!user) return null;

  return <>{children}</>;
}
