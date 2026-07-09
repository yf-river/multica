"use client";

import { useState } from "react";
import { cn } from "@multica/ui/lib/utils";
import { Dialog, DialogContent } from "@multica/ui/components/ui/dialog";
import type { CreateIssueSeed } from "@multica/core/issues";
import { AgentCreatePanel } from "./quick-create-issue";

export function CreateIssueDialog({
  onClose,
  data,
}: {
  onClose: () => void;
  data?: CreateIssueSeed | null;
}) {
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
