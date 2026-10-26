"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { KnowledgeImport } from "../types/knowledge";
import { api } from "../api";

export const knowledgeKeys = {
  all: (wsId: string) => ["knowledge", wsId] as const,
};

export function useKnowledge(
  wsId: string,
  searchText: string,
  directory?: string,
) {
  const client = useQueryClient();
  const key = knowledgeKeys.all(wsId);
  const status = useQuery({
    queryKey: [...key, "status"],
    queryFn: () => api.getKnowledgeStatus(wsId),
    enabled: !!wsId,
  });
  const enabled = !!wsId && status.data?.healthy === true;
  const resources = useQuery({
    queryKey: [...key, "resources", directory],
    queryFn: () => api.listKnowledgeResources(wsId, directory),
    enabled,
  });
  const search = useQuery({
    queryKey: [...key, "search", searchText],
    queryFn: () => api.searchKnowledge(wsId, searchText, 20),
    enabled: enabled && !!searchText,
  });
  const imports = useQuery({
    queryKey: [...key, "imports"],
    queryFn: async () => {
      const next = await api.listKnowledgeImports(wsId);
      const previous = client.getQueryData<KnowledgeImport[]>([
        ...key,
        "imports",
      ]);
      if (
        next.some(
          (item) =>
            item.status === "completed" &&
            previous?.some(
              (old) => old.id === item.id && old.status !== "completed",
            ),
        )
      ) {
        void client.invalidateQueries({ queryKey: [...key, "resources"] });
        void client.invalidateQueries({ queryKey: [...key, "search"] });
      }
      return next;
    },
    enabled: !!wsId,
    refetchInterval: (query) =>
      query.state.data?.some(
        (item) => item.status === "fetching" || item.status === "indexing",
      )
        ? 2000
        : false,
  });
  const invalidate = () => client.invalidateQueries({ queryKey: key });
  const upload = useMutation({
    mutationFn: (file: File) => api.uploadKnowledgeResource(wsId, file),
    onSuccess: invalidate,
  });
  const importURL = useMutation({
    mutationFn: ({ url, requestId }: { url: string; requestId: string }) =>
      api.importKnowledgeURL(wsId, url, requestId),
    onSuccess: invalidate,
  });
  const remove = useMutation({
    mutationFn: ({ uri, recursive }: { uri: string; recursive: boolean }) =>
      api.deleteKnowledgeResource(wsId, uri, recursive),
    onSuccess: invalidate,
  });
  return { status, resources, search, imports, upload, importURL, remove };
}

export { knowledgeEntries } from "./resources";
export type { KnowledgeEntry } from "./resources";
