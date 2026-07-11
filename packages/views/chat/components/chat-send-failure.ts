import {
  ApiError,
  ApiResponseValidationError,
  ApiTransportError,
} from "@multica/core/api";

export type ChatSendFailureDisposition = "failed" | "outcome-unknown";

export function isOutcomeUnknownMutationError(error: unknown): boolean {
  return (
    (error instanceof ApiError && error.status >= 500) ||
    ((error instanceof ApiResponseValidationError || error instanceof ApiTransportError) &&
      error.mayHaveCommitted)
  );
}

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
  if (isOutcomeUnknownMutationError(error)) {
    actions.refreshServerState();
    return "outcome-unknown";
  }

  actions.rollbackOptimisticState();
  return "failed";
}
