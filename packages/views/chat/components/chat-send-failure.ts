import { ApiResponseValidationError } from "@multica/core/api";

export type ChatSendFailureDisposition = "failed" | "outcome-unknown";

/**
 * A malformed 2xx mutation response does not prove that the mutation failed.
 * Keep the optimistic state and reconcile from the server so the user cannot
 * accidentally create a duplicate task by immediately resending the draft.
 */
export function reconcileChatSendFailure(
  error: unknown,
  actions: {
    refreshServerState: () => void;
    rollbackOptimisticState: () => void;
  },
): ChatSendFailureDisposition {
  if (error instanceof ApiResponseValidationError && error.mayHaveCommitted) {
    actions.refreshServerState();
    return "outcome-unknown";
  }

  actions.rollbackOptimisticState();
  return "failed";
}
