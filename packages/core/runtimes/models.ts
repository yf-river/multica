import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { RuntimeModelsResult } from "../types/agent";
import { pollRuntimeRequest } from "./poll-request";

const runtimeModelsKeys = {
  all: () => ["runtimes", "models"] as const,
  forRuntime: (runtimeId: string) =>
    [...runtimeModelsKeys.all(), runtimeId] as const,
};

const POLL_TIMEOUT_MS = 30_000;

// resolveRuntimeModels initiates a list-models request against the daemon
// (via heartbeat piggyback) and polls until the daemon reports back or
// the request times out. Returns both the models list and a
// `supported` flag: `supported=false` means the provider ignores
// per-agent model selection entirely (hermes today) — the UI uses
// this to disable its dropdown instead of accepting a value that
// wouldn't be honoured at runtime.
async function resolveRuntimeModels(
  runtimeId: string,
): Promise<RuntimeModelsResult> {
  const initial = await api.initiateListModels(runtimeId);
  const current = await pollRuntimeRequest(
    initial,
    (requestId) => api.getListModelsResult(runtimeId, requestId),
    POLL_TIMEOUT_MS,
    "model discovery timed out",
  );
  if (current.status === "failed" || current.status === "timeout") {
    throw new Error(current.error || "model discovery failed");
  }
  return { models: current.models ?? [], supported: current.supported };
}

export function runtimeModelsOptions(runtimeId: string | null | undefined) {
  return queryOptions({
    queryKey: runtimeId
      ? runtimeModelsKeys.forRuntime(runtimeId)
      : runtimeModelsKeys.all(),
    queryFn: () => resolveRuntimeModels(runtimeId as string),
    enabled: Boolean(runtimeId),
    // Models rarely change; cache for 60s to match the server-side
    // cache in agent.ListModels.
    staleTime: 60_000,
    retry: false,
  });
}
