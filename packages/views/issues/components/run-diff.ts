import type { TimelineEntry } from "@multica/core/types";

/**
 * 1. Maps each run id to the id of its newest comment on the timeline. The diff
 *    snapshot belongs to the run, not to a single comment, so only the run's last
 *    comment shows the card — one run posting several comments must not repeat it.
 */
export function lastCommentIdByTaskId(timeline: TimelineEntry[]): Map<string, string> {
  const lastByTask = new Map<string, string>();
  for (const entry of timeline) {
    // 2. Entries arrive in ascending time order, so the last write wins.
    if (entry.type !== "comment" || !entry.source_task_id) continue;
    lastByTask.set(entry.source_task_id, entry.id);
  }
  return lastByTask;
}
