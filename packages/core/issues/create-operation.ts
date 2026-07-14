import { api, type ApiClient } from "../api";
import { executeRecoverableIntent, sameMutationRequest } from "../api/transport";
import type { CreateIssueRequest, Issue } from "../types";
import { generateUUID } from "../utils";
import { useIssueCreatePendingStore } from "./issue-create-pending-store";

type IssueCreateClient = Pick<ApiClient, "createIssue">;

export async function createIssueWithRecovery(
  request: CreateIssueRequest,
  client: IssueCreateClient = api,
): Promise<Issue> {
  const pending = useIssueCreatePendingStore.getState().pendingCreate;
  return executeRecoverableIntent(
    pending,
    (operation) => sameMutationRequest(operation.request, request),
    () => ({ request, requestKey: generateUUID() }),
    (operation) => useIssueCreatePendingStore.getState().setPendingCreate(operation),
    (operation) => client.createIssue(operation.request, operation.requestKey),
  );
}
