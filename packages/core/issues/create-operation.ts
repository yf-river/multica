import { api } from "../api";
import { executeRecoverableIntent, sameMutationRequest } from "../api/transport";
import { createWorkspacePendingCreateStore } from "../platform/recoverable-operation-store";
import type { CreateIssueRequest, Issue } from "../types";
import { generateUUID } from "../utils";

const useIssueCreatePendingStore =
  createWorkspacePendingCreateStore<CreateIssueRequest>(
    "multica_issue_create_pending",
  );

export async function createIssueWithRecovery(
  request: CreateIssueRequest,
): Promise<Issue> {
  const pending = useIssueCreatePendingStore.getState().pending;
  return executeRecoverableIntent(
    pending,
    (operation) => sameMutationRequest(operation.request, request),
    () => ({ request, requestKey: generateUUID(), createdAt: Date.now() }),
    (operation) => useIssueCreatePendingStore.getState().setPending(operation),
    (operation) => api.createIssue(operation.request, operation.requestKey),
  );
}
