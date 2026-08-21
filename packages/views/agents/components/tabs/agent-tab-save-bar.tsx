"use client";

import { Loader2, Save } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../../i18n";

export function AgentTabSaveBar({
  dirty,
  saving,
  disabled,
  onSave,
  className,
}: {
  dirty: boolean;
  saving: boolean;
  disabled: boolean;
  onSave: () => void;
  className?: string;
}) {
  const { t } = useT("agents");
  return (
    <div className={cn("flex items-center justify-end gap-3", className)}>
      {dirty && (
        <span className="text-xs text-muted-foreground">
          {t(($) => $.tab_body.common.unsaved_changes)}
        </span>
      )}
      <Button onClick={onSave} disabled={disabled} size="sm">
        {saving ? (
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
        ) : (
          <Save className="h-3.5 w-3.5" />
        )}
        {t(($) => $.tab_body.common.save)}
      </Button>
    </div>
  );
}
