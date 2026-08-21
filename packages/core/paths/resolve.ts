import type { Workspace } from "../types";
import { useAuthStore } from "../auth";
import { paths } from "./paths";

/**
 * Priority:
 *   workspace[0] → /<first.slug>/issues
 *   no workspace → /workspaces/new
 *
 * The team edition does not route users through the product onboarding
 * questionnaire. `hasOnboarded` is accepted for backward-compatible callers
 * while destination choice is based only on workspace availability.
 */
export function resolvePostAuthDestination(
  workspaces: Workspace[],
  _hasOnboarded?: boolean,
): string {
  const first = workspaces[0];
  if (first) {
    return paths.workspace(first.slug).issues();
  }
  return paths.newWorkspace();
}

/**
 * Single source of truth: backed by `users.onboarded_at`, which
 * arrives with the user object on every auth response.
 */
export function useHasOnboarded(): boolean {
  return useAuthStore((s) => s.user?.onboarded_at != null);
}
