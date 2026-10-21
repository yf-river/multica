import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const supervisionKeys = {
  all: (wsId: string) => ["workspaces", wsId, "supervision"] as const,
};
export function supervisionSnapshotOptions(wsId: string) {
  return queryOptions({ queryKey: [...supervisionKeys.all(wsId), "summary"], queryFn: () => api.getSupervision(), refetchInterval: 15_000 });
}
export function supervisionListOptions(wsId: string, search: string, filter: string, offset: number, project: string) {
  return queryOptions({ queryKey: [...supervisionKeys.all(wsId), "list", search, filter, offset, project],
    queryFn: () => api.listSupervision({ search, filter, offset, project }), refetchInterval: 15_000 });
}
export function supervisionDetailOptions(wsId: string, issueId: string) {
  return queryOptions({ queryKey: [...supervisionKeys.all(wsId), "detail", issueId],
    queryFn: () => api.getSupervisionSubject(issueId), enabled: Boolean(issueId), refetchInterval: 15_000 });
}
