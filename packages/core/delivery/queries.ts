import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
export const deliveryKeys = { detail: (wsId: string, issueId: string) => ["workspaces", wsId, "issues", issueId, "deliveries"] as const };
export function deliveryOptions(wsId: string, issueId: string) {
  return queryOptions({ queryKey: deliveryKeys.detail(wsId, issueId), queryFn: () => api.getIssueDeliveries(issueId), refetchInterval: 10_000 });
}
