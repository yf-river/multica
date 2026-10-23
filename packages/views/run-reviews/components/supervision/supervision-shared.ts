// Shared helpers for the quality supervision tab: date formatting, error text,
// and the single review-state mapping used by both the list and the overview.

import { formatDateTime } from "../run-review-format";

/** Convert an unknown thrown value into a readable message. */
export function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

/** Format a timestamp for supervision copy; missing values render as a dash. */
export function moment(value?: string | null) {
  return value ? formatDateTime(value) : "—";
}

/**
 * Review state of one task. Automatic supervision only runs after a task reaches
 * its done category, so a task that is still in progress is "waiting" and a
 * finished task is "covered" once its completion-round materials are read.
 */
export type ReviewState = "waiting" | "manual_running" | "manual_checked" | "no_runs" | "running" | "stopped" | "failed" | "retrying" | "pending" | "covered";

/** Subject shape needed to derive the review badge, without binding to the full API type. */
export type ReviewSubject = {
  business_status: string;
  status_category?: string;
  run_count: number;
  running: boolean;
  manual_running?: boolean;
  manual_checked?: boolean;
  stopped: boolean;
  failures: number;
  round_sequence?: number | null;
  pending_materials?: number;
  desired_version: number;
  reviewed_version: number;
  last_checked_at: string | null;
};

/**
 * Single source of truth for the per-task review state. The order matters:
 * unfinished tasks never look supervised, a running batch beats every stored
 * conclusion, and coverage is reported separately from findings.
 */
export function reviewState(item: ReviewSubject): ReviewState {
  const done = (item.status_category || "done") === "done";
  if (!done) {
    if (item.running) return "manual_running";
    if (item.manual_checked) return "manual_checked";
    return "waiting";
  }
  if (item.running) return "running";
  if (item.round_sequence == null) return "waiting";
  if (item.stopped) return "stopped";
  if (item.failures >= 3) return "failed";
  if (item.failures > 0 && (item.pending_materials ?? 0) > 0) return "retrying";
  if ((item.pending_materials ?? 0) > 0) return "pending";
  if (item.run_count === 0) return "no_runs";
  return "covered";
}
