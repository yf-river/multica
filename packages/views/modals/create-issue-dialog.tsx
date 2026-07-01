"use client";

import { useState } from "react";
import { cn } from "@multica/ui/lib/utils";
import { Dialog, DialogContent } from "@multica/ui/components/ui/dialog";
import type { CreateMode } from "@multica/core/issues/stores/create-mode-store";
import { AgentCreatePanel } from "./quick-create-issue";

/**
 * Shell for the single create-issue flow. Legacy callers may still pass
 * initialMode="manual" through the modal registry, but the product surface is
 * now always agent-create; pinned fields are passed in `data`.
 */
export function CreateIssueDialog({
  onClose,
  initialMode: _initialMode,
  data,
}: {
  onClose: () => void;
  initialMode: CreateMode;
  data?: Record<string, unknown> | null;
}) {
  void _initialMode;
  const [isExpanded, setIsExpanded] = useState(false);

  const className = cn(
    "p-0 gap-0 flex flex-col overflow-hidden",
    "!top-1/2 !left-1/2 !-translate-x-1/2 !-translate-y-1/2",
    "!transition-all !duration-300 !ease-out",
    isExpanded
      ? "!max-w-4xl !w-full !h-5/6"
      : "!max-w-xl !w-full !max-h-[80vh]",
  );

  return (
    <Dialog open onOpenChange={(v) => { if (!v) onClose(); }}>
      <DialogContent
        finalFocus={false}
        showCloseButton={false}
        className={className}
      >
        <AgentCreatePanel
          onClose={onClose}
          data={data}
          isExpanded={isExpanded}
          setIsExpanded={setIsExpanded}
        />
      </DialogContent>
    </Dialog>
  );
}
