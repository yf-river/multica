"use client";

import type { ReactNode } from "react";
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

export type SettingsConfirmAction = {
  title: string;
  description: string;
  variant?: "destructive";
  onConfirm: () => Promise<void>;
};

export function SettingsConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  cancelLabel,
  actionLabel,
  pendingActionLabel,
  pending = false,
  variant = "default",
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description: ReactNode;
  cancelLabel: ReactNode;
  actionLabel: ReactNode;
  pendingActionLabel?: ReactNode;
  pending?: boolean;
  variant?: "default" | "destructive";
  onConfirm: () => void | Promise<void>;
}) {
  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>{cancelLabel}</AlertDialogCancel>
          <AlertDialogAction variant={variant} onClick={onConfirm} disabled={pending}>
            {pending ? pendingActionLabel ?? actionLabel : actionLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
