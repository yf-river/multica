"use client";

/**
 * my-issues 模块：我的 issue 页面。
 * 基于当前用户与所选范围（指派给我/我创建的/与我相关等）渲染 issue 列表，
 * 复用 issues 模块的 IssueSurface（board/list/table/swimlane 多种视图）。
 */

import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { workspaceKeys, workspaceListOptions } from "@multica/core/workspace/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { issueKeys } from "@multica/core/issues/queries";
import { useWS } from "@multica/core/realtime";
import { useStore } from "zustand";
import { ListTodo } from "lucide-react";
import { useAuthStore } from "@multica/core/auth";
import {
  myIssuesRelationFromScope,
  myIssuesViewStore,
} from "@multica/core/issues/stores/my-issues-view-store";
import { PageHeader } from "../../layout/page-header";
import { IssueSurface } from "../../issues/surface/issue-surface";
import { useT } from "../../i18n";
import { MyIssuesHeader } from "./my-issues-header";

/** 我的 issue 页主体：把当前用户 + 范围喂给通用的 IssueSurface。 */
export function MyIssuesPage() {
  const { t } = useT("my-issues");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const { subscribeWorkspace } = useWS();
  const { data: workspaces = [] } = useQuery(workspaceListOptions());
  useEffect(() => {
    const cleanups = workspaces.filter(w => w.id !== wsId).map(w =>
      subscribeWorkspace(w.slug, message => {
        if (message.type === "member:removed" || message.type === "workspace:deleted") {
          qc.invalidateQueries({ queryKey: workspaceKeys.list() });
          qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });
        }
        if (message.type.startsWith("issue:") || message.type.startsWith("issue_")) {
          qc.invalidateQueries({ queryKey: issueKeys.tableAll(wsId) });
        }
      }),
    );
    return () => cleanups.forEach(cleanup => cleanup());
  }, [workspaces, wsId, qc, subscribeWorkspace]);

  const user = useAuthStore((s) => s.user);
  const scope = useStore(myIssuesViewStore, (s) => s.scope);
  const setScope = useStore(myIssuesViewStore, (s) => s.setScope);

  return (
    <div className="flex flex-1 min-h-0 flex-col">
      <PageHeader>
        <ListTodo className="h-4 w-4 text-muted-foreground" />
        <h1 className="text-body font-medium">{t(($) => $.page.breadcrumb)}</h1>
      </PageHeader>

      {user ? (
        <IssueSurface
          scope={{
            type: "my",
            userId: user.id,
            relation: myIssuesRelationFromScope(scope),
          }}
          modes={["board", "list", "table", "swimlane"]}
          batchToolbar="list"
          renderHeader={({ controller }) => (
            <MyIssuesHeader
              allIssues={controller.surfaceIssues}
              workingAgents={controller.workingAgents}
              scope={scope}
              onScopeChange={setScope}
              isRefreshing={controller.isRefreshing}
              facetCountsExact={controller.facetCountsExact}
              tableFacetCounts={controller.tableFacetCounts}
              onTableFacetChange={controller.setActiveTableFacet}
            />
          )}
          renderEmpty={() => (
            <div className="flex flex-1 min-h-0 flex-col items-center justify-center gap-2 text-muted-foreground">
              <ListTodo className="h-10 w-10 text-faint-foreground" />
              <p className="text-body">{t(($) => $.page.empty_title)}</p>
              <p className="text-caption">{t(($) => $.page.empty_description)}</p>
            </div>
          )}
        />
      ) : null}
    </div>
  );
}
