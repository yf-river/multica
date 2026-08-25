import { api } from "../api";
import { executePendingCreateMutation } from "../api/transport";
import type { QuickCreateIssueRequest } from "../types";
import { useQuickCreateStore } from "./stores/quick-create-store";

export async function quickCreateIssueWithRecovery(
  request: QuickCreateIssueRequest,
): Promise<void> {
  return executePendingCreateMutation(
    useQuickCreateStore,
    request,
    (operation) => api.quickCreateIssue(operation.request, operation.requestKey),
  );
}
