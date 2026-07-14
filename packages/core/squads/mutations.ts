import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { executePendingMutation } from "../api/transport";
import { useWorkspaceId } from "../paths";
import type { CreateSquadRequest, Squad } from "../types";
import { generateUUID } from "../utils";
import { workspaceKeys } from "../workspace/queries";
import { useSquadPendingOperationStore } from "./pending-operation-store";

export function useCreateSquad() {
  const queryClient = useQueryClient();
  const workspaceId = useWorkspaceId();
  return useMutation({
    mutationFn: async (request: CreateSquadRequest) => {
      const operations = useSquadPendingOperationStore.getState();
      return executePendingMutation(
        operations.pendingCreate,
        () => ({ requestKey: generateUUID(), request }),
        operations.setPendingCreate,
        (operation) => api.createSquad(operation.request, operation.requestKey),
      );
    },
    onSuccess: (squad) => {
      queryClient.setQueryData<Squad[]>(workspaceKeys.squads(workspaceId), (old) =>
        old && !old.some((item) => item.id === squad.id)
          ? [...old, squad]
          : old,
      );
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: workspaceKeys.squads(workspaceId) });
    },
  });
}
