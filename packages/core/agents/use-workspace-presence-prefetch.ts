"use client";

import { useQuery } from "@tanstack/react-query";
import { agentListOptions, squadListOptions } from "../workspace/queries";
import { runtimeListOptions } from "../runtimes/queries";
import { agentTaskSnapshotOptions } from "./queries";

// Warm the workspace-scoped presence and mention caches before their first UI consumer.
export function useWorkspacePresencePrefetch(wsId: string | undefined): void {
  useQuery({ ...agentListOptions(wsId ?? ""), enabled: !!wsId });
  useQuery({ ...runtimeListOptions(wsId ?? ""), enabled: !!wsId });
  useQuery({ ...agentTaskSnapshotOptions(wsId ?? ""), enabled: !!wsId });
  useQuery({ ...squadListOptions(wsId ?? ""), enabled: !!wsId });
}
