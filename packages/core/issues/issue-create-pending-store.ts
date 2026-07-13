import type { CreateIssueRequest } from "../types";
import {
  createWorkspacePendingCreateStore,
  type PendingCreateOperation,
} from "../platform/pending-create-store";

export type PendingIssueCreate = PendingCreateOperation<CreateIssueRequest>;

export const useIssueCreatePendingStore =
  createWorkspacePendingCreateStore<CreateIssueRequest>(
    "multica_issue_create_pending",
  );
