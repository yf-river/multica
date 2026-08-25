import type { Workspace } from "../types";
import { paths } from "./paths";

export function resolvePostAuthDestination(workspaces: Workspace[]): string {
  const first = workspaces[0];
  if (first) {
    return paths.workspace(first.slug).issues();
  }
  return paths.newWorkspace();
}
