"use client";

import { ArrowLeft, LogOut } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import type { Workspace } from "@multica/core/types";
import { useConfigStore } from "@multica/core/config";
import { useLogout } from "../auth";
import { useT } from "../i18n";
import { CreateWorkspaceForm } from "./create-workspace-form";

/**
 * Full-page shell for the web "create workspace" transition.
 *
 * `onBack` is optional: caller passes it only when there's somewhere to go
 * back to (user has other workspaces, or the flow was entered from an
 * existing session). On the zero-workspace entry path it's omitted, which
 * hides Back — Log out is then the only escape.
 */
export function NewWorkspacePage({
  onSuccess,
  onBack,
}: {
  onSuccess: (workspace: Workspace) => void;
  onBack?: () => void;
}) {
  const { t } = useT("workspace");
  const logout = useLogout();
  const workspaceCreationDisabled = useConfigStore((s) => s.workspaceCreationDisabled);

  return (
    <div className="relative flex min-h-svh flex-col bg-background">
      {onBack && (
        <Button
          variant="ghost"
          size="sm"
          className="absolute top-4 left-4 text-muted-foreground"
          onClick={onBack}
        >
          <ArrowLeft />
          {t(($) => $.new_page.back)}
        </Button>
      )}
      <Button
        variant="ghost"
        size="sm"
        className="absolute top-4 right-4 text-muted-foreground hover:text-destructive"
        onClick={logout}
      >
        <LogOut />
        {t(($) => $.new_page.log_out)}
      </Button>

      <div className="flex flex-1 flex-col items-center justify-center px-6 pb-12">
        <div className="flex w-full max-w-md flex-col items-center gap-6">
          {workspaceCreationDisabled ? (
            <div className="text-center">
              <h1 className="text-3xl font-semibold tracking-tight">
                {t(($) => $.creation_disabled.title)}
              </h1>
              <p className="mt-3 text-muted-foreground">
                {t(($) => $.creation_disabled.description)}
              </p>
            </div>
          ) : (
            <>
              <div className="text-center">
                <h1 className="text-3xl font-semibold tracking-tight">
                  {t(($) => $.new_page.title)}
                </h1>
                <p className="mt-3 text-muted-foreground">
                  {t(($) => $.new_page.description)}
                </p>
              </div>
              <CreateWorkspaceForm onSuccess={onSuccess} />
            </>
          )}
        </div>
      </div>
    </div>
  );
}
