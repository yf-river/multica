"use client";

import { Globe, Lock } from "lucide-react";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";

export type ResourceScope = "workspace" | "personal";

export function agentResourceScope(value: "workspace" | "personal"): ResourceScope {
  return value;
}

export function squadResourceScope(value: "workspace" | "personal"): ResourceScope {
  return value;
}

export function runtimeResourceScope(value: "workspace" | "personal" | null | undefined): ResourceScope {
  return value === "workspace" ? "workspace" : "personal";
}

export function resourceSegmentedOptionClass(active: boolean) {
  return active
    ? "rounded-md bg-background px-2 py-1.5 text-xs font-medium shadow-xs"
    : "rounded-md px-2 py-1.5 text-xs text-muted-foreground hover:bg-background/60";
}

export function ResourceScopeBadge({
  scope,
  label,
  tooltip,
  compact = false,
  className = "",
}: {
  scope: ResourceScope;
  label: string;
  tooltip?: string;
  compact?: boolean;
  className?: string;
}) {
  const Icon = scope === "personal" ? Lock : Globe;
  const badge = (
    <span
      className={`inline-flex shrink-0 items-center gap-0.5 rounded px-1.5 py-0.5 text-[10px] font-medium ${
        scope === "personal"
          ? "bg-muted text-muted-foreground"
          : "bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300"
      } ${className}`}
      aria-label={tooltip ?? label}
    >
      <Icon className="h-2.5 w-2.5 shrink-0" />
      {!compact && <span className="truncate">{label}</span>}
    </span>
  );

  if (!tooltip) return badge;
  return (
    <Tooltip>
      <TooltipTrigger render={badge} />
      <TooltipContent>{tooltip}</TooltipContent>
    </Tooltip>
  );
}
