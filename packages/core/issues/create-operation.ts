import { api, isMutationOutcomeUnknown } from "../api";
import type { CreateIssueRequest, Issue } from "../types";
import { generateUUID } from "../utils";
import {
  type PendingIssueCreate,
  useIssueCreatePendingStore,
} from "./issue-create-pending-store";

export interface IssueCreateClient {
  createIssue(request: CreateIssueRequest, idempotencyKey: string): Promise<Issue>;
}

async function executeIssueCreate(
  client: IssueCreateClient,
  operation: PendingIssueCreate,
): Promise<Issue> {
  try {
    const issue = await client.createIssue(operation.request, operation.requestKey);
    useIssueCreatePendingStore.getState().setPendingCreate();
    return issue;
  } catch (error) {
    if (!isMutationOutcomeUnknown(error)) {
      useIssueCreatePendingStore.getState().setPendingCreate();
    }
    throw error;
  }
}

export async function createIssueWithRecovery(
  request: CreateIssueRequest,
  client: IssueCreateClient = api,
): Promise<Issue> {
  const pending = useIssueCreatePendingStore.getState().pendingCreate;
  if (pending) {
    const recovered = await executeIssueCreate(client, pending);
    if (JSON.stringify(pending.request) === JSON.stringify(request)) {
      return recovered;
    }
  }

  const operation = { request, requestKey: generateUUID() };
  useIssueCreatePendingStore.getState().setPendingCreate(operation);
  return executeIssueCreate(client, operation);
}
