import type { CreateIssueRequest } from "../types";
import { createWorkspacePendingCreateStore } from "../platform/pending-create-store";

export const useIssueCreatePendingStore =
  createWorkspacePendingCreateStore<CreateIssueRequest>(
    "multica_issue_create_pending",
  );
