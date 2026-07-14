"use client";

import { useCallback, useMemo } from "react";
import { useQuery, useMutationState } from "@tanstack/react-query";
import { issueReactionsOptions } from "@multica/core/issues/queries";
import { useToggleIssueReaction, type ToggleIssueReactionVars } from "@multica/core/issues/mutations";

export function useIssueReactions(issueId: string, userId?: string) {
  const { data: serverReactions = [], isLoading: loading } = useQuery(
    issueReactionsOptions(issueId),
  );

  const toggleMutation = useToggleIssueReaction(issueId);

  // --- Optimistic UI derivation ---
  // Instead of writing temp data into the cache (which races with WS events),
  // derive optimistic state at render time from pending mutation variables.

  const pendingVars = useMutationState({
    filters: {
      mutationKey: ["toggleIssueReaction", issueId],
      status: "pending",
    },
    select: (m) =>
      m.state.variables as ToggleIssueReactionVars | undefined,
  });

  const reactions = useMemo(() => {
    if (pendingVars.length === 0) return serverReactions;

    let result = [...serverReactions];
    for (const vars of pendingVars) {
      if (!vars) continue;
      if (vars.existing) {
        // Pending removal
        result = result.filter((r) => r.id !== vars.existing!.id);
      } else {
        // Pending add — skip if server already has it (WS arrived first)
        const alreadyExists = result.some(
          (r) =>
            r.emoji === vars.emoji &&
            r.actor_type === "member" &&
            r.actor_id === userId,
        );
        if (!alreadyExists) {
          result = [
            ...result,
            {
              id: `optimistic-${vars.emoji}`,
              actor_type: "member",
              actor_id: userId ?? "",
              emoji: vars.emoji,
            },
          ];
        }
      }
    }
    return result;
  }, [serverReactions, pendingVars, userId]);

  // --- Mutation ---

  const toggleReaction = useCallback(
    async (emoji: string) => {
      if (!userId) return;
      const existing = serverReactions.find(
        (r) =>
          r.emoji === emoji &&
          r.actor_type === "member" &&
          r.actor_id === userId,
      );
      toggleMutation.mutate({ emoji, existing });
    },
    [userId, serverReactions, toggleMutation],
  );

  return { reactions, loading, toggleReaction };
}
