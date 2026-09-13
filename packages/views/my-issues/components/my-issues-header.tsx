"use client";

/**
 * my-issues 模块：「我的 issue」页的头部。
 * 包含范围（内置视图）切换、保存/切换视图、agent 运行中过滤、显示控制与
 * 筛选条件条。移动端范围选择退化为下拉。
 */

import { useEffect, useState } from "react";
import { ChevronDown } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import type {
  Issue,
  IssueTableFacetSpec,
  IssueTableFacetsResponse,
  WorkingAgentSummary,
} from "@multica/core/types";
import { type MyIssuesScope } from "@multica/core/issues/stores/my-issues-view-store";
import { useViewStore } from "@multica/core/issues/stores/view-store-context";
import { useT } from "../../i18n";
import { WorkspaceAgentWorkingChip } from "../../issues/components/workspace-agent-working-chip";
import {
  IssueDisplayControls,
  ViewRefreshIndicator,
} from "../../issues/components/issues-header";
import { cn } from "@multica/ui/lib/utils";
import { PAGE_GUTTER } from "../../layout/page-header";
import { FilterChipsBar } from "../../issues/components/filter-chips-bar";
import { toast } from "sonner";
import { SaveViewDialog, type SaveViewScope } from "../../issues/components/save-view-dialog";
import { useActiveIssueView } from "@multica/core/issue-views/use-active-view";
import { useWorkspaceId } from "@multica/core/hooks";
import { baselineFromQuery } from "@multica/core/issue-views/baseline";
import { ViewBar } from "../../issues/components/view-bar";
import type { IssueView } from "@multica/core/api/schemas";

/** My Issues 的标签页 → 已保存视图的 scope_variant（API 词汇）。 */
const SAVE_VARIANT: Record<MyIssuesScope, Extract<SaveViewScope, { kind: "my" }>["variant"]> = {
  all: "any",
  assigned: "assigned",
  created: "created",
  agents: "involved",
};

/** 我的 issue 页头部主体。 */
export function MyIssuesHeader({
  allIssues,
  workingAgents,
  scope,
  onScopeChange,
  isRefreshing = false,
  facetCountsExact = true,
  tableFacetCounts,
  onTableFacetChange,
}: {
  allIssues: Issue[];
  /** 见 IssueSurfaceController.workingAgents。My Issues 过去会为它自己的关系
   *  范围向 working-agents 端点另发一次请求；现在 surface 投影同时覆盖了
   *  关系与每个生效中的筛选条件。 */
  workingAgents: WorkingAgentSummary[] | undefined;
  scope: MyIssuesScope;
  onScopeChange: (scope: MyIssuesScope) => void;
  isRefreshing?: boolean;
  /** 见 IssueDisplayControls.facetCountsExact。 */
  facetCountsExact?: boolean;
  tableFacetCounts?: IssueTableFacetsResponse;
  onTableFacetChange: (facet: IssueTableFacetSpec | null) => void;
}) {
  const { t } = useT("my-issues");
  const { t: tIssues } = useT("issues");
  const [saveViewOpen, setSaveViewOpen] = useState(false);
  const saveScope: SaveViewScope = { kind: "my", variant: SAVE_VARIANT[scope] };
  const wsId = useWorkspaceId();
  const { activeView, views, viewsReady, setActive, missing } = useActiveIssueView(
    wsId,
    { scope_type: "my" },
  );
  useEffect(() => {
    if (missing) {
      setActive(null);
      toast.info(tIssues(($) => $.view_selector.unavailable));
    }
  }, [missing, setActive, tIssues]);
  const viewBaseline = activeView ? baselineFromQuery(activeView.query) : undefined;
  const [editTarget, setEditTarget] = useState<{
    view: IssueView;
    fromDefinition: boolean;
  } | null>(null);
  const SCOPES: { value: MyIssuesScope; label: string; description: string }[] = [
    { value: "all", label: t(($) => $.header.scope.all_label), description: t(($) => $.header.scope.all_description) },
    { value: "assigned", label: t(($) => $.header.scope.assigned_label), description: t(($) => $.header.scope.assigned_description) },
    { value: "created", label: t(($) => $.header.scope.created_label), description: t(($) => $.header.scope.created_description) },
    { value: "agents", label: t(($) => $.header.scope.agents_label), description: t(($) => $.header.scope.agents_description) },
  ];
  const agentRunningFilter = useViewStore((s) => s.agentRunningFilter);
  const toggleAgentRunningFilter = useViewStore(
    (s) => s.toggleAgentRunningFilter,
  );
  const scopeLabel = SCOPES.find((s) => s.value === scope)?.label ?? SCOPES[0]?.label;

  return (
    <>
    <div className={cn("min-h-12 shrink-0 py-2 [-webkit-overflow-scrolling:touch]", PAGE_GUTTER)}>
      <div className="flex w-full min-w-0 items-start justify-between gap-2">
        <div className="hidden min-w-0 flex-1 md:block">
          <ViewBar
            wsId={wsId}
            scope={{ scope_type: "my" }}
            builtins={SCOPES.map((s) => ({
              key: s.value,
              label: s.label,
              description: s.description,
              active: !activeView && scope === s.value,
              onSelect: () => {
                if (activeView) setActive(null);
                onScopeChange(s.value);
              },
            }))}
            views={views}
            viewsReady={viewsReady}
            activeView={activeView}
            onSelectView={(view) => setActive(view ? view.id : null)}
            onNewView={() => {
              setEditTarget(null);
              setSaveViewOpen(true);
            }}
            onEditView={(view) => {
              setEditTarget({ view, fromDefinition: true });
              setSaveViewOpen(true);
            }}
          />
        </div>

        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                variant="outline"
                size="sm"
                className="shrink-0 gap-1 text-muted-foreground md:hidden"
              >
                <span className="truncate">{scopeLabel}</span>
                <ChevronDown className="size-3 text-muted-foreground" />
              </Button>
            }
          />
          <DropdownMenuContent align="start" className="w-auto">
            <DropdownMenuRadioGroup
              value={scope}
              onValueChange={(value) => onScopeChange(value as MyIssuesScope)}
            >
              {SCOPES.map((s) => (
                <DropdownMenuRadioItem key={s.value} value={s.value}>
                  {s.label}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>

        <div className="flex shrink-0 items-center gap-1">
          {agentRunningFilter && (
            <span className="mr-1 hidden text-caption text-muted-foreground md:inline">
              {tIssues(($) => $.agent_activity.filter_active_label)}
            </span>
          )}
          <WorkspaceAgentWorkingChip
            value={agentRunningFilter}
            onToggle={toggleAgentRunningFilter}
            agents={workingAgents}
          />
          <IssueDisplayControls
            scopedIssues={allIssues}
            facetCountsExact={facetCountsExact}
            tableFacetCounts={tableFacetCounts}
            onTableFacetChange={onTableFacetChange}
            viewBaseline={viewBaseline}
          />
          <ViewRefreshIndicator active={isRefreshing} />
        </div>
      </div>
    </div>
    <FilterChipsBar
      viewBaseline={viewBaseline}
      saveLabel={activeView ? tIssues(($) => $.filters.chip_edit) : undefined}
      onSave={() => {
        setEditTarget(
          activeView ? { view: activeView, fromDefinition: false } : null,
        );
        setSaveViewOpen(true);
      }}
    />
    <SaveViewDialog
      open={saveViewOpen}
      onOpenChange={setSaveViewOpen}
      scope={saveScope}
      editView={editTarget?.view ?? null}
      seedFromDefinition={editTarget?.fromDefinition ?? false}
    />
    </>
  );
}
