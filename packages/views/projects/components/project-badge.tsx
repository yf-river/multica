"use client";

import { Check } from "lucide-react";
import {
  PROJECT_STATUS_CONFIG,
  PROJECT_STATUS_ORDER,
  PROJECT_PRIORITY_CONFIG,
  PROJECT_PRIORITY_ORDER
} from "@multica/core/projects/config";
import { cn } from "@multica/ui/lib/utils";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import type { Project, ProjectStatus, ProjectPriority, UpdateProjectRequest } from "@multica/core/types";
import { PriorityIcon } from "../../issues/components/priority-icon";
import { useProjectStatusLabels, useProjectPriorityLabels } from "./labels";

type ProjectBadgeProps = {
  project: Project;
  handleUpdate: (data: UpdateProjectRequest) => void;
  triggerClassName?: string;
  align?: "start" | "end" | "center";
  variant?: "badge" | "plain";
};

export function ProjectStatusBadge({
  project,
  handleUpdate,
  triggerClassName,
  align = "end",
  variant = "badge",
}: ProjectBadgeProps) {
  const statusLabels = useProjectStatusLabels();
  const statusCfg = PROJECT_STATUS_CONFIG[project.status];

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <button type="button" className={cn(
            variant === "badge"
              ? "inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-medium cursor-pointer hover:opacity-80 transition-opacity"
              : "inline-flex items-center gap-1.5 text-xs hover:text-foreground transition-colors",
            variant === "badge" && statusCfg.badgeBg,
            variant === "badge" && statusCfg.badgeText,
            triggerClassName
          )}>
            {variant === "plain" && (
              <span className={cn("size-2 rounded-full", statusCfg.dotColor)} />
            )}
            {statusLabels[project.status]}
          </button>
        }
      />
      <DropdownMenuContent align={align} className="w-44">
        {PROJECT_STATUS_ORDER.map((s) => (
          <DropdownMenuItem key={s} onClick={() => handleUpdate({ status: s as ProjectStatus })}>
            <span className={cn("size-2 rounded-full", PROJECT_STATUS_CONFIG[s].dotColor)} />
            <span>{statusLabels[s]}</span>
            {s === project.status && <Check className="ml-auto h-3.5 w-3.5" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

export function ProjectPriorityBadge({
  project,
  handleUpdate,
  triggerClassName,
  align = "end",
  variant = "badge",
}: ProjectBadgeProps) {
  const priorityLabels = useProjectPriorityLabels();
  const priorityCfg = PROJECT_PRIORITY_CONFIG[project.priority];

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <button type="button" className={cn(
            variant === "badge"
              ? "inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-medium hover:bg-accent/60 transition-colors cursor-pointer"
              : "inline-flex items-center gap-1.5 text-xs hover:text-foreground transition-colors",
            triggerClassName
          )}>
            <PriorityIcon priority={project.priority} />
            <span className={cn("text-xs", variant === "badge" && priorityCfg.color)}>
              {priorityLabels[project.priority]}
            </span>
          </button>
        }
      />
      <DropdownMenuContent align={align} className="w-44">
        {PROJECT_PRIORITY_ORDER.map((p) => (
          <DropdownMenuItem key={p} onClick={() => handleUpdate({ priority: p as ProjectPriority })}>
            <PriorityIcon priority={p} />
            <span>{priorityLabels[p]}</span>
            {p === project.priority && <Check className="ml-auto h-3.5 w-3.5" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
