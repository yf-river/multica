"use client";
import { Suspense, useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import { WorkspaceSlugProvider } from "@multica/core/paths";
import { getCurrentSlug, setCurrentWorkspace } from "@multica/core/platform";
import { DashboardLayout } from "@multica/views/layout";
import { InsightsPage } from "@multica/views/insights";
import { useNavigation } from "@multica/views/navigation";
function PlatformInsights() {
  const { searchParams, replace, pathname } = useNavigation();
  const user = useAuthStore((s) => s.user);
  const loading = useAuthStore((s) => s.isLoading);
  const list = useQuery({ ...workspaceListOptions(), enabled: Boolean(user) });
  const preferred = searchParams.get("workspace") ?? getCurrentSlug();
  const workspace =
    list.data?.find((w) => w.slug === preferred) ?? list.data?.[0];
  useEffect(() => {
    if (!loading && !user) replace("/login");
  }, [loading, user, replace]);
  if (workspace && pathname === "/insights")
    setCurrentWorkspace(workspace.slug, workspace.id);
  if (!workspace) return null;
  return (
    <WorkspaceSlugProvider slug={workspace.slug}>
      <DashboardLayout>
        <InsightsPage />
      </DashboardLayout>
    </WorkspaceSlugProvider>
  );
}
export default function Page() {
  return (
    <Suspense fallback={null}>
      <PlatformInsights />
    </Suspense>
  );
}
