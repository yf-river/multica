import type { IssueStatus, IssuePriority, IssueAssigneeType, Attachment } from "../../types";
import { createWorkspaceDraftStore } from "../../platform/workspace-storage";

interface IssueDraft {
  title: string;
  description: string;
  status: IssueStatus;
  priority: IssuePriority;
  assigneeType?: IssueAssigneeType;
  assigneeId?: string;
  startDate: string | null;
  dueDate: string | null;
  attachments: Attachment[];
}

const EMPTY_DRAFT: IssueDraft = {
  title: "",
  description: "",
  status: "todo",
  priority: "none",
  assigneeType: undefined,
  assigneeId: undefined,
  startDate: null,
  dueDate: null,
  attachments: [],
};

export const useIssueDraftStore = createWorkspaceDraftStore(
  "multica_issue_draft",
  EMPTY_DRAFT,
);
