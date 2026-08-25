"use client";

import { useNavigation } from "../navigation";
import { ArrowLeft } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@multica/ui/components/ui/dialog";
import { paths } from "@multica/core/paths";
import { useConfigStore } from "@multica/core/config";
import { CreateWorkspaceForm } from "../workspace/create-workspace-form";
import { useT } from "../i18n";

export function CreateWorkspaceModal({ onClose }: { onClose: () => void }) {
  const { t } = useT("modals");
  const tWorkspace = useT("workspace").t;
  const router = useNavigation();
  const workspaceCreationDisabled = useConfigStore((s) => s.workspaceCreationDisabled);

  return (
    <Dialog
      open
      onOpenChange={(v) => {
        if (!v) onClose();
      }}
    >
      <DialogContent
        finalFocus={false}
        showCloseButton={false}
        className="inset-0 flex h-full w-full max-w-none sm:max-w-none translate-0 flex-col rounded-none bg-background ring-0 shadow-none"
      >
        <Button
          variant="ghost"
          size="sm"
          className="absolute top-4 left-4 text-muted-foreground"
          onClick={onClose}
        >
          <ArrowLeft className="h-4 w-4" />
          {t(($) => $.common.back)}
        </Button>

        <div className="flex flex-1 flex-col items-center justify-center px-6 pb-12">
          <div className="flex w-full max-w-md flex-col items-center gap-6">
            {workspaceCreationDisabled ? (
              <div className="text-center">
                <DialogTitle className="text-2xl font-semibold">
                  {tWorkspace(($) => $.creation_disabled.title)}
                </DialogTitle>
                <DialogDescription className="mt-2">
                  {tWorkspace(($) => $.creation_disabled.description)}
                </DialogDescription>
              </div>
            ) : (
              <>
                <div className="text-center">
                  <DialogTitle className="text-2xl font-semibold">
                    {t(($) => $.create_workspace.title)}
                  </DialogTitle>
                  <DialogDescription className="mt-2">
                    {t(($) => $.create_workspace.description)}
                  </DialogDescription>
                </div>
                <CreateWorkspaceForm
                  onSuccess={(newWs) => {
                    onClose();
                    // Navigate INTO the new workspace. The mutation's own onSuccess
                    // (in core/workspace/mutations.ts) runs before this callback and
                    // has already seeded the workspace list cache, so the destination
                    // [workspaceSlug]/layout will resolve newWs.slug → workspace
                    // synchronously without a loading flash.
                    router.push(paths.workspace(newWs.slug).issues());
                  }}
                />
              </>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
