"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { CodeDiffCard } from "../../common/task-transcript/code-diff-card";

/**
 * 1. Diff card shown inside the issue timeline. The snapshot stays on the run
 *    (`agent_task_queue.result.code_diff`); the comment only points back at it
 *    through `source_task_id`, so editing a comment or posting several comments
 *    from one run never forks or duplicates the diff itself.
 */
export function RunDiffCard({
  issueId,
  taskId,
  className,
}: {
  issueId: string;
  taskId: string;
  className?: string;
}) {
  // 2. Shares the execution log's task-list cache: the global `task:` WS event
  //    invalidates it when a run ends, which is how the card appears a few
  //    seconds after the comment it sits under. Any other comment edit or reply
  //    does not refetch anything.
  const query=useQuery({queryKey:["run-diff",issueId,taskId,"summary"],queryFn:()=>api.getIssueTaskRun(issueId,taskId,true),staleTime:30_000});
  return <CodeDiffCard result={query.data?.result} variant="attachment" className={className} />;
}
