"use client";

import { useState } from "react";
import { Globe, Lock } from "lucide-react";
import type { AgentScope } from "@multica/core/types";
import {
  PickerItem,
  PropertyPicker,
} from "../../../issues/components/pickers";
import { ScopeBadge } from "../scope-badge";
import { CHIP_CLASS } from "./chip";
import { useT } from "../../../i18n";

export function ScopePicker({
  value,
  canEdit = true,
  onChange,
}: {
  value: AgentScope;
  /** When false, render a read-only `<ScopeBadge>` and skip the popover. */
  canEdit?: boolean;
  onChange: (next: AgentScope) => Promise<void> | void;
}) {
  const [open, setOpen] = useState(false);
  const { t } = useT("agents");

  if (!canEdit) {
    return <ScopeBadge value={value} />;
  }

  const Icon = value === "personal" ? Lock : Globe;
  const label = t(($) => $.scope[value].label);
  const tooltip = t(($) => $.scope[value].tooltip);

  const select = async (next: AgentScope) => {
    setOpen(false);
    if (next !== value) await onChange(next);
  };

  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-auto min-w-[12rem]"
      align="start"
      tooltip={tooltip}
      triggerRender={
        <button type="button" className={CHIP_CLASS} aria-label={tooltip} />
      }
      trigger={
        <>
          <Icon className="h-3 w-3 shrink-0 text-muted-foreground" />
          <span className="truncate">{label}</span>
        </>
      }
    >
      <PickerItem
        selected={value === "workspace"}
        onClick={() => select("workspace")}
      >
        <Globe className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <div className="text-left">
          <div className="font-medium">{t(($) => $.resource_scope.workspace.label)}</div>
          <div className="text-xs text-muted-foreground">
            {t(($) => $.resource_scope.workspace.tooltip)}
          </div>
        </div>
      </PickerItem>
      <PickerItem
        selected={value === "personal"}
        onClick={() => select("personal")}
      >
        <Lock className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
        <div className="text-left">
          <div className="font-medium">{t(($) => $.resource_scope.personal.label)}</div>
          <div className="text-xs text-muted-foreground">
            {t(($) => $.resource_scope.personal.tooltip)}
          </div>
        </div>
      </PickerItem>
    </PropertyPicker>
  );
}
