import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { agentTaskSnapshotOptions } from "@multica/core/agents";

type IssueAgentActivity = {
  id: string;
  agent_activity?: { running_count: number };
};

export function useRunningIssueIds(issues: IssueAgentActivity[], workspaceId: string) {
  const hasCompleteSummaries = issues.every(
    (issue) => issue.agent_activity !== undefined,
  );
  const summarizedIds = useMemo(() => {
    const ids = new Set<string>();
    for (const issue of issues) {
      if ((issue.agent_activity?.running_count ?? 0) > 0) ids.add(issue.id);
    }
    return ids;
  }, [issues]);
  const { data: taskSnapshot = [] } = useQuery({
    ...agentTaskSnapshotOptions(workspaceId),
    enabled: !hasCompleteSummaries,
  });
  const snapshotIds = useMemo(() => {
    const ids = new Set<string>();
    for (const task of taskSnapshot) {
      if (task.status === "running" && task.issue_id) ids.add(task.issue_id);
    }
    return ids;
  }, [taskSnapshot]);

  return hasCompleteSummaries ? summarizedIds : snapshotIds;
}
