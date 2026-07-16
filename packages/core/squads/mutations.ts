import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { executePendingCreateMutation } from "../api/transport";
import { useWorkspaceId } from "../paths";
import { createWorkspacePendingCreateStore } from "../platform/recoverable-operation-store";
import type { CreateSquadRequest, Squad } from "../types";
import { workspaceKeys } from "../workspace/queries";

const useSquadPendingOperationStore =
  createWorkspacePendingCreateStore<CreateSquadRequest>(
    "multica_squad_pending_operations",
  );

export function useCreateSquad() {
  const queryClient = useQueryClient();
  const workspaceId = useWorkspaceId();
  return useMutation({
    mutationFn: async (request: CreateSquadRequest) => {
      return executePendingCreateMutation(
        useSquadPendingOperationStore,
        request,
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
