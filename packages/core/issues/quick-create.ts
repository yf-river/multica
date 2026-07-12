import { isMutationOutcomeUnknown } from "../api";
import type { QuickCreateIssueRequest, QuickCreateIssueResponse } from "../types";
import { generateUUID } from "../utils";
import { useQuickCreateStore } from "./stores/quick-create-store";

export interface QuickCreateClient {
  quickCreateIssue(
    request: QuickCreateIssueRequest,
    idempotencyKey?: string,
  ): Promise<QuickCreateIssueResponse>;
}

export async function quickCreateIssueWithRecovery(
  api: QuickCreateClient,
  request: QuickCreateIssueRequest,
): Promise<QuickCreateIssueResponse> {
  const store = useQuickCreateStore.getState();
  const existing = store.pendingOperation;
  const operation = existing ?? { request, idempotencyKey: generateUUID() };

  if (operation !== existing) store.setPendingOperation(operation);
  try {
    const response = await api.quickCreateIssue(operation.request, operation.idempotencyKey);
    useQuickCreateStore.getState().clearPendingOperation(operation.idempotencyKey);
    return response;
  } catch (error) {
    if (!isMutationOutcomeUnknown(error)) {
      useQuickCreateStore.getState().clearPendingOperation(operation.idempotencyKey);
    }
    throw error;
  }
}
