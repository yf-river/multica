import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { inboxKeys } from "./queries";
import { useWorkspaceId } from "../paths";
import type { InboxItem } from "../types";

function useInboxMutation<Variables, Result>(
  mutationFn: (variables: Variables) => Promise<Result>,
  update?: (items: InboxItem[], variables: Variables) => InboxItem[],
) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const queryKey = inboxKeys.list(wsId);
  return useMutation<Result, Error, Variables, InboxItem[] | undefined>({
    mutationFn,
    onMutate: update
      ? async (variables) => {
          await qc.cancelQueries({ queryKey });
          const previous = qc.getQueryData<InboxItem[]>(queryKey);
          qc.setQueryData<InboxItem[]>(queryKey, (items) =>
            items ? update(items, variables) : items,
          );
          return previous;
        }
      : undefined,
    onError: update
      ? (_error, _variables, previous) => {
          if (previous !== undefined) qc.setQueryData(queryKey, previous);
        }
      : undefined,
    onSettled: () => {
      qc.invalidateQueries({ queryKey });
    },
  });
}

export function useMarkInboxRead() {
  return useInboxMutation(
    (id: string) => api.markInboxRead(id),
    (items, id) =>
      items.map((item) =>
        item.id === id ? { ...item, read: true } : item,
      ),
  );
}

export function useArchiveInbox() {
  return useInboxMutation(
    (id: string) => api.archiveInbox(id),
    (items, id) => {
      const issueId = items.find((item) => item.id === id)?.issue_id;
      return items.map((item) =>
        item.id === id || (issueId && item.issue_id === issueId)
          ? { ...item, archived: true }
          : item,
      );
    },
  );
}

export function useMarkAllInboxRead() {
  return useInboxMutation(
    () => api.markAllInboxRead(),
    (items) =>
      items.map((item) =>
        !item.archived ? { ...item, read: true } : item,
      ),
  );
}

export function useArchiveAllInbox() {
  return useInboxMutation(() => api.archiveAllInbox());
}

export function useArchiveAllReadInbox() {
  return useInboxMutation(() => api.archiveAllReadInbox());
}

export function useArchiveCompletedInbox() {
  return useInboxMutation(() => api.archiveCompletedInbox());
}
