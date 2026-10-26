import {
  mutationOptions,
  queryOptions,
  type QueryClient,
} from "@tanstack/react-query";
import { api } from "../api";
import type { InteractionAnswer } from "../types/task-interaction";

export const taskInteractionKey = (wsId: string, taskId: string) =>
  ["task-interactions", wsId, taskId] as const;

export function taskInteractionOptions(
  wsId: string,
  taskId: string,
  active: boolean,
) {
  return queryOptions({
    queryKey: taskInteractionKey(wsId, taskId),
    queryFn: () => api.getTaskInteractions(taskId),
    enabled: Boolean(wsId && taskId),
    refetchInterval: (query) =>
      active ||
      query.state.data?.interactions.some((row) => row.status === "pending")
        ? 2000
        : false,
  });
}

export function respondTaskInteractionOptions(
  client: QueryClient,
  wsId: string,
  taskId: string,
) {
  return mutationOptions({
    mutationFn: async ({
      id,
      answer,
    }: {
      id: string;
      answer: InteractionAnswer;
    }) => {
      const response = await api.respondTaskInteraction(taskId, id, answer);
      if (!response) throw new Error("invalid interaction response");
      return response;
    },
    onSuccess: () =>
      client.invalidateQueries({ queryKey: taskInteractionKey(wsId, taskId) }),
  });
}
