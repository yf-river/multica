import { infiniteQueryOptions, mutationOptions, queryOptions, type QueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { CreateRuntimeInput } from "../api/runtime-input-schema";

export const isNativeRuntimeCommand = (content: string) => /^(?:\/plan(?: off)?|\/goal(?: [\s\S]*)?)$/.test(content.trim());

export const runtimeInputKey = (wsId: string, taskId: string) => ["runtime-inputs", wsId, taskId] as const;

export function runtimeInputCapabilitiesOptions(wsId: string, taskId: string, active: boolean) {
  return queryOptions({
    queryKey: [...runtimeInputKey(wsId, taskId), "capabilities"],
    queryFn: () => api.getRuntimeInputCapabilities(taskId),
    enabled: Boolean(wsId && taskId && active),
    refetchInterval: active ? 2000 : false,
    retry: false,
  });
}

export function runtimeInputHistoryOptions(wsId: string, taskId: string, active: boolean, enabled = true) {
  return infiniteQueryOptions({
    queryKey: [...runtimeInputKey(wsId, taskId), "history"],
    queryFn: async ({ pageParam }) => {
      const page = await api.getRuntimeInputs(taskId, pageParam);
      if (!page.available) throw new Error("invalid runtime input history");
      return page;
    },
    initialPageParam: "",
    getNextPageParam: (page) => page.nextCursor || undefined,
    enabled: Boolean(wsId && taskId && enabled),
    refetchInterval: active ? 2000 : false,
  });
}

export function createRuntimeInputOptions(client: QueryClient, wsId: string, taskId: string) {
  return mutationOptions({
    mutationFn: async (input: CreateRuntimeInput) => {
      const response = await api.createRuntimeInput(taskId, input);
      if (!response || response.id !== input.id || response.taskId !== taskId || response.kind !== input.kind || response.content !== input.content) {
        throw new Error("invalid runtime input receipt");
      }
      return response;
    },
    retry: false,
    onSuccess: () => client.invalidateQueries({ queryKey: runtimeInputKey(wsId, taskId) }),
  });
}
