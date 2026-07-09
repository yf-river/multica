"use client";

import type { IssuePriority, IssueStatus } from "../types";
import { useModalStore } from "../modals";

/** Values that current issue-creation entry points may pin for the agent flow. */
export type CreateIssueSeed = {
  prompt?: string;
  agent_id?: string;
  squad_id?: string;
  project_id?: string | null;
  status?: IssueStatus;
  priority?: IssuePriority;
  start_date?: string | null;
  due_date?: string | null;
  parent_issue_id?: string | null;
  parent_issue_identifier?: string;
};

/** Open the only issue-creation flow supported by the current product. */
export function openCreateIssue(seed?: CreateIssueSeed | null) {
  useModalStore.getState().open("create-issue", seed ?? null);
}
