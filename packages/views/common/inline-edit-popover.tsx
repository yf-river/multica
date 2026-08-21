"use client";

import { useEffect, useState, type MouseEvent, type ReactElement, type ReactNode } from "react";
import { Loader2 } from "lucide-react";
import { isImeComposing } from "@multica/core/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";

interface InlineEditPopoverProps {
  value: string;
  onSave: (next: string) => Promise<void>;
  title: string;
  cancelLabel: ReactNode;
  saveLabel: ReactNode;
  placeholder?: string;
  validate?: (value: string) => string | null;
  onSaveSuccess?: (next: string) => void;
  onSaveError?: (error: unknown) => void;
  children: (triggerProps: { onClick: (event: MouseEvent) => void }) => ReactNode;
}

export function InlineEditPopover({
  value,
  onSave,
  title,
  cancelLabel,
  saveLabel,
  placeholder,
  validate,
  onSaveSuccess,
  onSaveError,
  children,
}: InlineEditPopoverProps) {
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState(value);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setDraft(value);
      setError(null);
    }
  }, [open, value]);

  const commit = async () => {
    const validationError = validate?.(draft) ?? null;
    if (validationError) {
      setError(validationError);
      return;
    }
    if (draft === value) {
      setOpen(false);
      return;
    }
    setSaving(true);
    try {
      await onSave(draft);
      setOpen(false);
      onSaveSuccess?.(draft);
    } catch (saveError) {
      onSaveError?.(saveError);
    } finally {
      setSaving(false);
    }
  };

  const onChange = (next: string) => {
    setDraft(next);
    if (error) setError(null);
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={children({ onClick: () => setOpen(true) }) as ReactElement}
      />
      <PopoverContent align="start" className="w-72 p-3">
        <div className="space-y-2">
          <p className="text-xs font-medium">{title}</p>
          <Input
            autoFocus
            value={draft}
            onChange={(event) => onChange(event.target.value)}
            placeholder={placeholder}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                setOpen(false);
                return;
              }
              if (isImeComposing(event)) return;
              if (event.key === "Enter") {
                event.preventDefault();
                void commit();
              }
            }}
            className="h-8"
          />
          {error && <p className="text-xs text-destructive">{error}</p>}
          <div className="flex items-center justify-end gap-2">
            <Button variant="ghost" size="sm" onClick={() => setOpen(false)} disabled={saving}>
              {cancelLabel}
            </Button>
            <Button size="sm" onClick={() => void commit()} disabled={saving || draft === value}>
              {saving ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : saveLabel}
            </Button>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
}
