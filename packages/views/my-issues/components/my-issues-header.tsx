"use client";

import { useMemo } from "react";
import { useStore } from "zustand";
import type { Issue } from "@multica/core/types";
import { myIssuesViewStore, type MyIssuesScope } from "@multica/core/issues/stores/my-issues-view-store";
import { useT } from "../../i18n";
import { WorkspaceAgentWorkingChip } from "../../issues/components/workspace-agent-working-chip";
import {
  IssueDisplayControls,
  IssueScopeSelector,
  type IssueScopeOption,
} from "../../issues/components/issues-header";

export function MyIssuesHeader({ allIssues }: { allIssues: Issue[] }) {
  const { t } = useT("my-issues");
  const { t: tIssues } = useT("issues");
  const SCOPES: IssueScopeOption<MyIssuesScope>[] = [
    { value: "all", label: t(($) => $.header.scope.all_label), description: t(($) => $.header.scope.all_description) },
    { value: "assigned", label: t(($) => $.header.scope.assigned_label), description: t(($) => $.header.scope.assigned_description) },
    { value: "created", label: t(($) => $.header.scope.created_label), description: t(($) => $.header.scope.created_description) },
    { value: "agents", label: t(($) => $.header.scope.agents_label), description: t(($) => $.header.scope.agents_description) },
  ];
  const scope = useStore(myIssuesViewStore, (s) => s.scope);
  const agentRunningFilter = useStore(myIssuesViewStore, (s) => s.agentRunningFilter);
  const act = myIssuesViewStore.getState();
  const scopedIssueIds = useMemo(
    () => new Set(allIssues.map((i) => i.id)),
    [allIssues],
  );
  const scopeLabel =
    SCOPES.find((s) => s.value === scope)?.label ?? SCOPES[0]?.label ?? "";

  return (
    <div className="h-12 shrink-0 overflow-x-auto px-4 [-webkit-overflow-scrolling:touch]">
      <div className="flex h-full w-max min-w-full items-center justify-between gap-2">
        <IssueScopeSelector
          options={SCOPES}
          value={scope}
          activeLabel={scopeLabel}
          onChange={act.setScope}
        />

        <div className="flex shrink-0 items-center gap-1">
          {agentRunningFilter && (
            <span className="mr-1 hidden text-xs text-muted-foreground md:inline">
              {tIssues(($) => $.agent_activity.filter_active_label)}
            </span>
          )}
          <WorkspaceAgentWorkingChip
            value={agentRunningFilter}
            onToggle={act.toggleAgentRunningFilter}
            scopedIssueIds={scopedIssueIds}
            scopedIssues={allIssues}
          />
          <IssueDisplayControls scopedIssues={allIssues} />
        </div>
      </div>
    </div>
  );
}
