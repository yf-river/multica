"use client";

import { useCallback } from "react";
import { toast } from "sonner";
import { useUpdateIssue } from "@multica/core/issues/mutations";
import type { UpdateIssueRequest } from "@multica/core/types";

type MoveIssueUpdates = Pick<
  UpdateIssueRequest,
  "status" | "assignee_type" | "assignee_id" | "position" | "parent_issue_id"
>;

export function useMoveIssue(fallbackErrorMessage: string) {
  const mutation = useUpdateIssue();

  return useCallback(
    (
      issueId: string,
      updates: MoveIssueUpdates,
      onSettled?: () => void,
    ) => {
      mutation.mutate(
        { id: issueId, ...updates },
        {
          onError: (error) =>
            toast.error(
              error instanceof Error && error.message
                ? error.message
                : fallbackErrorMessage,
            ),
          onSettled,
        },
      );
    },
    [fallbackErrorMessage, mutation],
  );
}
