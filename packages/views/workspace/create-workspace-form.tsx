"use client";

import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import type { Workspace } from "@multica/core/types";
import { isImeComposing } from "@multica/core/utils";
import { useT } from "../i18n";
import { useConfigStore } from "@multica/core/config";
import { workspaceUrlHost } from "@multica/core/workspace/workspace-url";
import { useWorkspaceCreateController } from "./use-workspace-create-controller";

export interface CreateWorkspaceFormProps {
  onSuccess: (workspace: Workspace) => void | Promise<void>;
}

export function CreateWorkspaceForm({ onSuccess }: CreateWorkspaceFormProps) {
  const { t } = useT("workspace");
  const urlHost = workspaceUrlHost(useConfigStore((s) => s.daemonAppUrl));
  const {
    name,
    slug,
    slugError,
    canSubmit,
    isPending,
    handleNameChange,
    handleSlugChange,
    handleCreate,
  } = useWorkspaceCreateController({
    onSuccess,
    messages: {
      slugFormat: t(($) => $.create_form.errors.slug_format),
      slugReserved: t(($) => $.create_form.errors.slug_reserved),
      slugTaken: t(($) => $.create_form.errors.slug_taken),
      slugConflictToast: t(($) => $.create_form.errors.slug_conflict_toast),
      createFailed: t(($) => $.create_form.errors.create_failed),
    },
  });

  return (
    <Card className="w-full">
      <CardContent className="space-y-4 pt-6">
        <div className="space-y-1.5">
          <Label htmlFor="ws-name">{t(($) => $.create_form.name_label)}</Label>
          <Input
            id="ws-name"
            autoFocus
            type="text"
            value={name}
            onChange={(e) => handleNameChange(e.target.value)}
            placeholder={t(($) => $.create_form.name_placeholder)}
            onKeyDown={(e) => {
              if (isImeComposing(e)) return;
              if (e.key === "Enter") handleCreate();
            }}
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="ws-slug">{t(($) => $.create_form.url_label)}</Label>
          <div className="flex items-center gap-0 rounded-md border bg-background focus-within:ring-2 focus-within:ring-ring">
            <span className="pl-3 text-sm text-muted-foreground select-none">
              {`${urlHost}/`}
            </span>
            <Input
              id="ws-slug"
              type="text"
              value={slug}
              onChange={(e) => handleSlugChange(e.target.value)}
              placeholder={t(($) => $.create_form.url_placeholder)}
              className="border-0 shadow-none focus-visible:ring-0"
              onKeyDown={(e) => {
                if (isImeComposing(e)) return;
                if (e.key === "Enter") handleCreate();
              }}
            />
          </div>
          {slugError && (
            <p className="text-xs text-destructive">{slugError}</p>
          )}
        </div>
        <Button
          className="w-full"
          size="lg"
          onClick={handleCreate}
          disabled={isPending || !canSubmit}
        >
          {isPending
            ? t(($) => $.create_form.submitting)
            : t(($) => $.create_form.submit)}
        </Button>
      </CardContent>
    </Card>
  );
}
