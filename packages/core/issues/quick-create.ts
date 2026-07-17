import type { ApiClient } from "../api";
import { executePendingCreateMutation } from "../api/transport";
import type { QuickCreateIssueRequest } from "../types";
import { useQuickCreateStore } from "./stores/quick-create-store";

type QuickCreateClient = Pick<ApiClient, "quickCreateIssue">;

export async function quickCreateIssueWithRecovery(
  api: QuickCreateClient,
  request: QuickCreateIssueRequest,
): Promise<void> {
  return executePendingCreateMutation(
    useQuickCreateStore,
    request,
    (operation) => api.quickCreateIssue(operation.request, operation.requestKey),
  );
}
