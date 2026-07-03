import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Issue } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import zhHansCommon from "../../../locales/zh-Hans/common.json";
import zhHansIssues from "../../../locales/zh-Hans/issues.json";

const TEST_RESOURCES = {
  "zh-Hans": { common: zhHansCommon, issues: zhHansIssues },
};

export const mockIssue: Issue = {
  id: "issue-1",
  workspace_id: "ws-1",
  number: 1,
  identifier: "TES-1",
  title: "Example",
  description: null,
  status: "todo",
  priority: "medium",
  assignee_type: null,
  assignee_id: null,
  creator_type: "member",
  creator_id: "user-1",
  parent_issue_id: null,
  start_date: null,
  due_date: null,
  project_id: null,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
} as Issue;

export function IssueActionsQueryProvider({
  children,
}: {
  children: ReactNode;
}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
}

export function wrapIssueActionsMenu(ui: ReactNode) {
  return (
    <I18nProvider locale="zh-Hans" resources={TEST_RESOURCES}>
      <IssueActionsQueryProvider>{ui}</IssueActionsQueryProvider>
    </I18nProvider>
  );
}
