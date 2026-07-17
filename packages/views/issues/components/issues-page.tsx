"use client";

import { useEffect, useMemo } from "react";
import { ListTodo } from "lucide-react";
import type { Issue } from "@multica/core/types";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useQuery } from "@tanstack/react-query";
import { useIssueViewStore, useClearFiltersOnWorkspaceChange, type IssueDateFilter } from "@multica/core/issues/stores/view-store";
import { dateOnlyToLocalDate } from "@multica/core/issues/date";
import { ViewStoreProvider } from "@multica/core/issues/stores/view-store-context";
import { projectIssueViews } from "../utils/filter";
import { BOARD_STATUSES } from "@multica/core/issues/config";
import { useWorkspaceId } from "@multica/core/paths";
import { issueAssigneeGroupsOptions, issueListOptions, childIssueProgressOptions, type AssigneeGroupedIssuesFilter } from "@multica/core/issues/queries";
import { useIssueSelectionStore } from "@multica/core/issues/stores/selection-store";
import { PageHeader } from "../../layout/page-header";
import { IssuesHeader } from "./issues-header";
import { BoardView } from "./board-view";
import { ListView } from "./list-view";
import { SwimLaneView } from "./swimlane-view";
import { BatchActionToolbar } from "./batch-action-toolbar";
import type { ChildProgress } from "./list-row";
import { useT } from "../../i18n";
import { useRunningIssueIds } from "../hooks/use-running-issue-ids";
import { useMoveIssue } from "../hooks/use-move-issue";

const EMPTY_CHILD_PROGRESS = new Map<string, ChildProgress>();

function childProgressMapFromIssues(issues: Issue[]): Map<string, ChildProgress> {
  const map = new Map<string, ChildProgress>();
  for (const issue of issues) {
    if (!issue.child_progress || issue.child_progress.total <= 0) continue;
    map.set(issue.id, issue.child_progress);
  }
  return map;
}

function hasCompleteChildProgressSummaries(issues: Issue[]) {
  return issues.every((issue) => issue.child_progress !== undefined);
}

function issueDateFilterToApiParams(filter: IssueDateFilter | null) {
  if (!filter) return {};

  const from = dateOnlyToLocalDate(filter.from);
  const to = dateOnlyToLocalDate(filter.to);
  if (!from || !to) return {};

  const start = from <= to ? from : to;
  const endSource = from <= to ? to : from;
  const end = new Date(endSource);
  end.setDate(end.getDate() + 1);

  return {
    date_field: filter.field,
    date_start: start.toISOString(),
    date_end: end.toISOString(),
  };
}

export function IssuesPage() {
  const { t } = useT("issues");
  const wsId = useWorkspaceId();

  const scope = useIssueViewStore((s) => s.scope);
  const viewMode = useIssueViewStore((s) => s.viewMode);
  const dateFilter = useIssueViewStore((s) => s.dateFilter);
  const setDateFilter = useIssueViewStore((s) => s.setDateFilter);
  const grouping = useIssueViewStore((s) => s.grouping);
  const statusFilters = useIssueViewStore((s) => s.statusFilters);
  const priorityFilters = useIssueViewStore((s) => s.priorityFilters);
  const assigneeFilters = useIssueViewStore((s) => s.assigneeFilters);
  const includeNoAssignee = useIssueViewStore((s) => s.includeNoAssignee);
  const creatorFilters = useIssueViewStore((s) => s.creatorFilters);
  const projectFilters = useIssueViewStore((s) => s.projectFilters);
  const includeNoProject = useIssueViewStore((s) => s.includeNoProject);
  const labelFilters = useIssueViewStore((s) => s.labelFilters);
  const sortBy = useIssueViewStore((s) => s.sortBy);
  const sortDirection = useIssueViewStore((s) => s.sortDirection);
  const agentRunningFilter = useIssueViewStore((s) => s.agentRunningFilter);
  const usesAssigneeBoard = viewMode === "board" && grouping === "assignee";

  const sort = useMemo(
    () => ({
      sort_by: sortBy,
      sort_direction: sortBy !== "position" ? sortDirection : undefined,
    } as const),
    [sortBy, sortDirection],
  );
  const dateParams = useMemo(
    () => issueDateFilterToApiParams(dateFilter),
    [dateFilter],
  );
  const queryParams = useMemo(
    () => ({ ...sort, ...dateParams }),
    [dateParams, sort],
  );

  const assigneeGroupFilter = useMemo<AssigneeGroupedIssuesFilter>(() => {
    const filter: AssigneeGroupedIssuesFilter = {
      statuses: statusFilters.length > 0 ? statusFilters : [...BOARD_STATUSES],
      priorities: priorityFilters,
      assignee_filters: assigneeFilters,
      include_no_assignee: includeNoAssignee,
      creator_filters: creatorFilters,
      project_ids: projectFilters,
      include_no_project: includeNoProject,
      label_ids: labelFilters,
    };
    if (scope === "members") filter.assignee_types = ["member"];
    if (scope === "agents") filter.assignee_types = ["agent", "squad"];
    return filter;
  }, [assigneeFilters, creatorFilters, includeNoAssignee, includeNoProject, labelFilters, priorityFilters, projectFilters, scope, statusFilters]);

  const assigneeGroupsOptions = issueAssigneeGroupsOptions(wsId, assigneeGroupFilter, queryParams);
  const statusIssuesQuery = useQuery({
    ...issueListOptions(wsId, queryParams),
    enabled: !usesAssigneeBoard,
  });
  const assigneeGroupsQuery = useQuery({
    ...assigneeGroupsOptions,
    enabled: usesAssigneeBoard,
  });
  const allIssues = useMemo(
    () => statusIssuesQuery.data ?? [],
    [statusIssuesQuery.data],
  );
  const loading = usesAssigneeBoard
    ? assigneeGroupsQuery.isLoading
    : statusIssuesQuery.isLoading;
  const visibleAllIssues = allIssues;
  const visibleAssigneeGroups = useMemo(() => {
    return assigneeGroupsQuery.data?.groups ?? [];
  }, [assigneeGroupsQuery.data]);
  const visibleAssigneeIssues = useMemo(
    () => visibleAssigneeGroups.flatMap((group) => group.issues),
    [visibleAssigneeGroups],
  );

  // Clear filter state when switching between workspaces (URL-driven).
  useClearFiltersOnWorkspaceChange(useIssueViewStore, wsId);

  useEffect(() => {
    useIssueSelectionStore.getState().clear();
  }, [viewMode, scope]);

  // Scope pre-filter: narrow by assignee type
  const scopedIssues = useMemo(() => {
    if (scope === "members")
      return visibleAllIssues.filter((i) => i.assignee_type === "member");
    if (scope === "agents")
      return visibleAllIssues.filter((i) => i.assignee_type === "agent" || i.assignee_type === "squad");
    return visibleAllIssues;
  }, [visibleAllIssues, scope]);

	const headerIssues = usesAssigneeBoard ? visibleAssigneeIssues : scopedIssues;

	const agentActivitySourceIssues = usesAssigneeBoard ? visibleAssigneeIssues : visibleAllIssues;
	const runningIssueIds = useRunningIssueIds(agentActivitySourceIssues, wsId);

  const {
    issues,
    swimlaneIssues,
    activeFilters,
    visibleStatuses,
    hiddenStatuses,
  } = useMemo(
    () =>
      projectIssueViews(scopedIssues, {
        statusFilters,
        priorityFilters,
        assigneeFilters,
        includeNoAssignee,
        creatorFilters,
        projectFilters,
        includeNoProject,
        labelFilters,
        agentRunningFilter,
        runningIssueIds,
      }),
    [
      scopedIssues,
      statusFilters,
      priorityFilters,
      assigneeFilters,
      includeNoAssignee,
      creatorFilters,
      projectFilters,
      includeNoProject,
      labelFilters,
      agentRunningFilter,
      runningIssueIds,
    ],
  );

  const childProgressSourceIssues = usesAssigneeBoard ? visibleAssigneeIssues : visibleAllIssues;
  const hasListChildProgress = hasCompleteChildProgressSummaries(childProgressSourceIssues);
  const listChildProgressMap = useMemo(
    () => childProgressMapFromIssues(childProgressSourceIssues),
    [childProgressSourceIssues],
  );
  const { data: fallbackChildProgressMap = EMPTY_CHILD_PROGRESS } = useQuery({
    ...childIssueProgressOptions(wsId),
    enabled: !hasListChildProgress,
  });
  const childProgressMap = hasListChildProgress ? listChildProgressMap : fallbackChildProgressMap;

  const handleMoveIssue = useMoveIssue(t(($) => $.page.move_failed));

  const contentSkeleton = viewMode === "list" ? (
    <div className="flex-1 min-h-0 overflow-y-auto p-2 space-y-1">
      {Array.from({ length: 4 }).map((_, i) => (
        <Skeleton key={i} className="h-10 w-full rounded-lg" />
      ))}
    </div>
  ) : (
    <div className="flex flex-1 min-h-0 gap-4 overflow-x-auto p-4">
      {Array.from({ length: 5 }).map((_, i) => (
        <div key={i} className="flex min-w-52 flex-1 flex-col gap-2">
          <Skeleton className="h-4 w-20" />
          <Skeleton className="h-24 w-full rounded-lg" />
          <Skeleton className="h-24 w-full rounded-lg" />
        </div>
      ))}
    </div>
  );

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <PageHeader className="gap-2">
        <ListTodo className="h-4 w-4 text-muted-foreground" />
        <h1 className="text-sm font-medium">{t(($) => $.page.breadcrumb_title)}</h1>
      </PageHeader>

      <ViewStoreProvider store={useIssueViewStore}>
        <IssuesHeader
          scopedIssues={headerIssues}
          dateFilter={dateFilter}
          onDateFilterChange={setDateFilter}
        />

        {loading ? contentSkeleton : headerIssues.length === 0 ? (
          <div className="flex flex-1 min-h-0 flex-col items-center justify-center gap-2 text-muted-foreground">
            <ListTodo className="h-10 w-10 text-muted-foreground/40" />
            <p className="text-sm">{t(($) => $.page.empty_title)}</p>
            <p className="text-xs">{t(($) => $.page.empty_hint)}</p>
          </div>
        ) : (
          <div className="flex flex-col flex-1 min-h-0">
            {viewMode === "board" ? (
              <BoardView
                issues={usesAssigneeBoard ? visibleAssigneeIssues : issues}
                assigneeGroups={usesAssigneeBoard ? visibleAssigneeGroups : undefined}
                assigneeGroupQueryKey={usesAssigneeBoard ? assigneeGroupsOptions.queryKey : undefined}
                assigneeGroupFilter={usesAssigneeBoard ? assigneeGroupFilter : undefined}
                visibleStatuses={visibleStatuses}
                hiddenStatuses={hiddenStatuses}
                onMoveIssue={handleMoveIssue}
                childProgressMap={childProgressMap}
                sort={queryParams}
              />
            ) : viewMode === "swimlane" ? (
              <SwimLaneView
                issues={issues}
                unfilteredIssues={swimlaneIssues}
                activeFilters={activeFilters}
                visibleStatuses={visibleStatuses}
                hiddenStatuses={hiddenStatuses}
                onMoveIssue={handleMoveIssue}
                childProgressMap={childProgressMap}
                sort={queryParams}
              />
            ) : (
              <ListView issues={issues} visibleStatuses={visibleStatuses} childProgressMap={childProgressMap} sort={queryParams} onMoveIssue={handleMoveIssue} />
            )}
          </div>
        )}
        {viewMode === "list" && <BatchActionToolbar />}
      </ViewStoreProvider>
    </div>
  );
}
