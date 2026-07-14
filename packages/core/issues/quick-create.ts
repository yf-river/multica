import type { ApiClient } from "../api";
import { executeRecoverableMutation } from "../api/transport";
import type { QuickCreateIssueRequest, QuickCreateIssueResponse } from "../types";
import { generateUUID } from "../utils";
import { useQuickCreateStore } from "./stores/quick-create-store";

type QuickCreateClient = Pick<ApiClient, "quickCreateIssue">;

export async function quickCreateIssueWithRecovery(
  api: QuickCreateClient,
  request: QuickCreateIssueRequest,
): Promise<QuickCreateIssueResponse> {
  const store = useQuickCreateStore.getState();
  const existing = store.pendingOperation;
  const operation = existing ?? { request, idempotencyKey: generateUUID() };

  if (operation !== existing) store.setPendingOperation(operation);
  return executeRecoverableMutation(
    () => api.quickCreateIssue(operation.request, operation.idempotencyKey),
    () => useQuickCreateStore.getState().clearPendingOperation(operation.idempotencyKey),
  );
}
