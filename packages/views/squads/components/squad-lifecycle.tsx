import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/paths";
import type { Squad } from "@multica/core/types";
import { workspaceKeys } from "@multica/core/workspace/queries";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { useT } from "../../i18n";

function useSquadMutation<T>(mutationFn: (squadId: string) => Promise<T>) {
  const workspaceId = useWorkspaceId();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: workspaceKeys.squads(workspaceId) });
    },
  });
}

const useArchiveSquad = () =>
  useSquadMutation((squadId) => api.deleteSquad(squadId));

export const useRestoreSquad = () =>
  useSquadMutation((squadId) => api.restoreSquad(squadId));

export function SquadArchiveDialog({
  squad,
  open,
  onOpenChange,
  onArchived,
}: {
  squad: Pick<Squad, "id" | "name">;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onArchived?: () => void;
}) {
  const { t } = useT("squads");
  const archive = useArchiveSquad();
  const setOpen = (next: boolean) => {
    if (!next && archive.isPending) return;
    onOpenChange(next);
  };
  const confirm = () => archive.mutate(squad.id, {
    onSuccess: () => {
      onOpenChange(false);
      onArchived?.();
      toast.success(t(($) => $.archive_dialog.success));
    },
    onError: (error) =>
      toast.error(error instanceof Error ? error.message : String(error)),
  });

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t(($) => $.archive_dialog.title)}</AlertDialogTitle>
          <AlertDialogDescription>
            {t(($) => $.archive_dialog.description, { name: squad.name })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={archive.isPending}>
            {t(($) => $.archive_dialog.cancel)}
          </AlertDialogCancel>
          <AlertDialogAction
            disabled={archive.isPending}
            onClick={(event) => {
              event.preventDefault();
              confirm();
            }}
            className="bg-destructive text-white hover:bg-destructive/90"
          >
            {archive.isPending && <Loader2 className="size-3.5 animate-spin" />}
            {archive.isPending
              ? t(($) => $.archive_dialog.archiving)
              : t(($) => $.archive_dialog.confirm)}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
