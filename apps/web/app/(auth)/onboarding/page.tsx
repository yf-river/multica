"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { useAuthStore } from "@multica/core/auth";
import { paths, resolvePostAuthDestination } from "@multica/core/paths";
import { workspaceListOptions } from "@multica/core/workspace/queries";

/**
 * The team edition no longer shows the product onboarding questionnaire.
 * Keep the route as a compatibility shim for old links and send users to
 * the first useful workspace destination immediately.
 */
export default function OnboardingPage() {
  const router = useRouter();
  const user = useAuthStore((s) => s.user);
  const isLoading = useAuthStore((s) => s.isLoading);
  const { data: workspaces = [], isFetched: workspacesFetched } = useQuery({
    ...workspaceListOptions(),
    enabled: !!user,
  });

  useEffect(() => {
    if (isLoading || !user) {
      if (!isLoading && !user) router.replace(paths.login());
      return;
    }
    if (!workspacesFetched) return;
    router.replace(resolvePostAuthDestination(workspaces));
  }, [isLoading, user, workspacesFetched, workspaces, router]);

  return null;
}
