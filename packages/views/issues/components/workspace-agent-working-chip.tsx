"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button } from "@multica/ui/components/ui/button";
import {
  HoverCard,
  HoverCardTrigger,
  HoverCardContent,
} from "@multica/ui/components/ui/hover-card";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentTaskSnapshotOptions } from "@multica/core/agents";
import type { AgentTask, Issue } from "@multica/core/types";
import { AgentAvatarStack } from "../../agents/components/agent-avatar-stack";
import { AgentActivityHoverContent } from "../../agents/components/agent-activity-hover-content";
import { useT } from "../../i18n";

interface WorkspaceAgentWorkingChipProps {
  // Controlled toggle binding. Different surfaces (Issues page singleton
  // hook, My Issues vanilla store) own the underlying state, so the chip
  // stays presentational and accepts both forms via plain props.
  value: boolean;
  onToggle: () => void;
  // When set, only running tasks whose issue id is in this set count
  // toward the chip — and toward the hover card. Lets the chip stay in
  // sync with the page's visible issue scope (e.g. My Issues only shows
  // "my" running tasks, not the whole workspace). When omitted, the chip
  // shows workspace-wide running agents.
  scopedIssueIds?: ReadonlySet<string>;
  // Optional list-page activity summaries. When every issue in scope carries
  // this summary, the chip can render its first screen without the
  // workspace-wide agent-task-snapshot request. Hovering an active chip still
  // loads the full snapshot for task-level details.
  scopedIssues?: readonly Pick<Issue, "id" | "agent_activity">[];
}

/**
 * Filter chip on the issues / my-issues header, sitting to the left of
 * the Filter button. Always rendered so the filter toggle never
 * disappears mid-flight (a previous design hid the chip when no agents
 * were running, which trapped users in an active-but-invisible filter
 * state).
 *
 * Two visual modes:
 *
 *   - Has running agents → avatar stack + count + "working" label,
 *     wrapped in HoverCard that lists every active task on hover.
 *     Brand-filled when the filter is on.
 *
 *   - No running agents  → "0 working" label, muted when off,
 *     brand-filled when on. No HoverCard — there is nothing to show;
 *     the label IS the state.
 *
 * Click toggles the filter in both modes. The button itself is the
 * affordance — no Tooltip wrapping (the popover IS the label when there
 * is one, and the label is self-explanatory when there isn't).
 *
 * `scopedIssueIds` lets a calling header narrow the chip to a subset of
 * issues — typically "what's visible on this page right now". My Issues
 * uses it so the chip count matches the my-scope list; the global
 * /issues page passes the All/Members/Agents-scoped set. Without it the
 * chip is workspace-wide.
 */
export function WorkspaceAgentWorkingChip({
  value,
  onToggle,
  scopedIssueIds,
  scopedIssues,
}: WorkspaceAgentWorkingChipProps) {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();
  const [open, setOpen] = useState(false);
  const hasCompleteSummaries =
    scopedIssues !== undefined &&
    scopedIssues.every((issue) => issue.agent_activity !== undefined);
  const summaryAgentIds = useMemo(() => {
    if (!hasCompleteSummaries) return [];
    const ids = new Set<string>();
    for (const issue of scopedIssues ?? []) {
      if (scopedIssueIds && !scopedIssueIds.has(issue.id)) continue;
      if ((issue.agent_activity?.running_count ?? 0) <= 0) continue;
      for (const agentId of issue.agent_activity?.agent_ids ?? []) ids.add(agentId);
    }
    return [...ids];
  }, [hasCompleteSummaries, scopedIssueIds, scopedIssues]);
  const { data: snapshot = [] } = useQuery({
    ...agentTaskSnapshotOptions(wsId),
    enabled: !hasCompleteSummaries || (open && summaryAgentIds.length > 0),
  });

  const { runningTasks, agentIds } = useMemo(() => {
    if (hasCompleteSummaries) {
      const running: AgentTask[] = [];
      if (open) {
        for (const task of snapshot) {
          if (task.status !== "running") continue;
          if (scopedIssueIds && !scopedIssueIds.has(task.issue_id)) continue;
          running.push(task);
        }
      }
      return { runningTasks: running, agentIds: summaryAgentIds };
    }
    const running: AgentTask[] = [];
    for (const task of snapshot) {
      if (task.status !== "running") continue;
      // When scoped, drop running tasks whose issue isn't in the visible
      // set — the chip's job is to summarise what the user sees, not
      // what's happening elsewhere in the workspace.
      if (scopedIssueIds && !scopedIssueIds.has(task.issue_id)) continue;
      running.push(task);
    }
    const unique = [...new Set(running.map((tk) => tk.agent_id))];
    return { runningTasks: running, agentIds: unique };
  }, [hasCompleteSummaries, open, scopedIssueIds, snapshot, summaryAgentIds]);

  const hasAgents = agentIds.length > 0;
  // Active (brand-filled) class — must explicitly re-pin text and bg in
  // every interactive state. Button's `outline` variant ships
  // `hover:text-foreground` + `aria-expanded:bg-muted aria-expanded:text-foreground`,
  // which would otherwise repaint the brand chip back to neutral on
  // hover and while the HoverCard is open.
  const activeClass = value
    ? "border-brand bg-brand text-brand-foreground hover:bg-brand/90 hover:text-brand-foreground aria-expanded:bg-brand aria-expanded:text-brand-foreground"
    : hasAgents
      ? "text-foreground"
      : "text-muted-foreground";

  const label = t(($) => $.agent_activity.chip_label);

  // Idle path: no agents in scope. Still wrap in HoverCard with a
  // single-line placeholder so the chip's hover behavior is consistent
  // with the active state — an idle chip that does nothing on hover
  // reads as broken next to an active one that pops a panel.
  if (!hasAgents) {
    return (
      <HoverCard open={open} onOpenChange={setOpen}>
        <HoverCardTrigger
          render={
            <Button
              variant="outline"
              size="sm"
              className={`h-8 px-2 md:h-7 md:px-2.5 ${activeClass}`}
              onClick={onToggle}
              aria-pressed={value}
            >
              <span className="tabular-nums">0</span>
              <span className="hidden md:inline">{label}</span>
            </Button>
          }
        />
        <HoverCardContent align="end" className="w-auto">
          <p className="text-xs text-muted-foreground">
            {t(($) => $.agent_activity.empty_hover)}
          </p>
        </HoverCardContent>
      </HoverCard>
    );
  }

  return (
    <HoverCard open={open} onOpenChange={setOpen}>
      <HoverCardTrigger
        render={
          <Button
            variant="outline"
            size="sm"
            className={`h-8 px-2 md:h-7 md:px-2.5 ${activeClass}`}
            onClick={onToggle}
            aria-pressed={value}
          >
            <AgentAvatarStack
              agentIds={agentIds}
              size={16}
              max={3}
              opacity="full"
            />
            <span className="tabular-nums">{agentIds.length}</span>
            <span className="hidden md:inline">{label}</span>
          </Button>
        }
      />
      <HoverCardContent align="end" className="w-72">
        {open ? <AgentActivityHoverContent tasks={runningTasks} /> : null}
      </HoverCardContent>
    </HoverCard>
  );
}
