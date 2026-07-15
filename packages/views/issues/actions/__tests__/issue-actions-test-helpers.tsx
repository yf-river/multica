import type { ReactNode } from "react";
import type { Issue } from "@multica/core/types";
import { IssueTestProviders } from "../../test/issue-test-providers";

export const mockIssue: Issue = {
  id: "issue-1",
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
  position: 0,
  metadata: {},
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

export function IssueActionsQueryProvider({
  children,
}: {
  children: ReactNode;
}) {
  return <IssueTestProviders>{children}</IssueTestProviders>;
}

export function wrapIssueActionsMenu(ui: ReactNode) {
  return <IssueActionsQueryProvider>{ui}</IssueActionsQueryProvider>;
}
