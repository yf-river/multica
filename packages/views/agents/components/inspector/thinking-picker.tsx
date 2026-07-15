"use client";

import { useState } from "react";
import type { RuntimeModelThinkingLevel } from "@multica/core/types";
import {
  PickerItem,
  PropertyPicker,
} from "../../../issues/components/pickers";
import { CHIP_CLASS } from "./chip";
import { useT } from "../../../i18n";

/**
 * Displays daemon-discovered CLI labels. An empty value leaves effort to the
 * local CLI; an unknown persisted value remains visible so it can be cleared.
 */
export function ThinkingPicker({
  value,
  levels,
  canEdit = true,
  onChange,
}: {
  /** Persisted thinking_level — "" means "follow local CLI config". */
  value: string;
  /** Supported levels for the current (runtime, model) pair. Usually
   *  non-empty when the row is shown, but the stale-orphan clear path
   *  in ThinkingPropRow mounts the picker with an empty list plus a
   *  persisted value so the user can see and clear the dangling token. */
  levels: RuntimeModelThinkingLevel[];
  /** When false, render a static read-only display and skip the popover. */
  canEdit?: boolean;
  onChange: (next: string) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);

  const selected = value ? levels.find((l) => l.value === value) : undefined;
  const triggerLabel = selected
    ? selected.label
    : value || t(($) => $.pickers.thinking_default);
  const triggerTitle = t(($) => $.pickers.thinking_tooltip, {
    value: triggerLabel,
  });

  const select = async (next: string) => {
    setOpen(false);
    if (next !== value) await onChange(next);
  };

  if (!canEdit) {
    return (
      <span
        className="min-w-0 truncate px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground"
        title={triggerTitle}
      >
        {triggerLabel}
      </span>
    );
  }

  return (
    <PropertyPicker
      open={open}
      onOpenChange={setOpen}
      width="w-auto min-w-[14rem] max-w-md"
      align="start"
      tooltip={triggerTitle}
      triggerRender={
        <button
          type="button"
          className={CHIP_CLASS}
          aria-label={triggerTitle}
        />
      }
      trigger={
        <span className="min-w-0 truncate font-mono text-[11px]">
          {triggerLabel}
        </span>
      }
    >
      {levels.map((l) => (
        <PickerItem
          key={l.value}
          selected={l.value === value}
          onClick={() => void select(l.value)}
        >
          <span className="block min-w-0 flex-1 text-left">
            <span className="truncate text-[13px] font-medium">{l.label}</span>
            {l.description && (
              <span className="mt-0.5 block text-[11px] leading-snug text-muted-foreground">
                {l.description}
              </span>
            )}
          </span>
        </PickerItem>
      ))}

      {value && (
        <button
          type="button"
          onClick={() => void select("")}
          className="mt-1 flex w-full items-center border-t px-3 py-2 text-left text-xs text-muted-foreground transition-colors hover:bg-accent/50"
          title={t(($) => $.pickers.thinking_clear_title)}
        >
          {t(($) => $.pickers.thinking_clear)}
        </button>
      )}
    </PropertyPicker>
  );
}
