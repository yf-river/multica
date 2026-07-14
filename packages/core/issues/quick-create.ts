import type { ApiClient } from "../api";
import { executePendingMutation } from "../api/transport";
import type { QuickCreateIssueRequest } from "../types";
import { generateUUID } from "../utils";
import { useQuickCreateStore } from "./stores/quick-create-store";

type QuickCreateClient = Pick<ApiClient, "quickCreateIssue">;

export async function quickCreateIssueWithRecovery(
  api: QuickCreateClient,
  request: QuickCreateIssueRequest,
): Promise<void> {
  const store = useQuickCreateStore.getState();
  return executePendingMutation(
    store.pendingOperation,
    () => ({ request, idempotencyKey: generateUUID() }),
    (operation) => {
      if (operation) store.setPendingOperation(operation);
    },
    (operation) => api.quickCreateIssue(operation.request, operation.idempotencyKey),
    (operation) => useQuickCreateStore.getState().clearPendingOperation(operation.idempotencyKey),
  );
}
