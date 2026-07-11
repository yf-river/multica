import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, isMutationOutcomeUnknown } from "../api";
import { useWorkspaceId } from "../hooks";
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
      const pendingCreate = operations.pendingCreate ?? {
        requestKey: generateUUID(),
        request,
      };
      operations.setPendingCreate(pendingCreate);
      try {
        const squad = await api.createSquad(
          pendingCreate.request,
          pendingCreate.requestKey,
        );
        useSquadPendingOperationStore.getState().setPendingCreate();
        return squad;
      } catch (error) {
        if (!isMutationOutcomeUnknown(error)) {
          useSquadPendingOperationStore.getState().setPendingCreate();
        }
        throw error;
      }
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
