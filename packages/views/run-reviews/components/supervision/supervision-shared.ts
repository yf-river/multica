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

export type ReviewState = "no_runs" | "running" | "stopped" | "failed" | "changed" | "pending" | "covered";

/** Subject shape needed to derive the review badge, without binding to the full API type. */
export type ReviewSubject = {
  run_count: number;
  running: boolean;
  stopped: boolean;
  failures: number;
  desired_version: number;
  reviewed_version: number;
  last_checked_at: string | null;
};

/**
 * Single source of truth for the per-task review state. The order matters:
 * no execution record beats running, running beats stopped, and a stopped
 * subject must not be reported as retryable.
 */
export function reviewState(item: ReviewSubject): ReviewState {
  if (item.run_count === 0) return "no_runs";
  if (item.running) return "running";
  if (item.stopped) return "stopped";
  if (item.failures >= 3) return "failed";
  if (item.desired_version > item.reviewed_version) return item.last_checked_at ? "changed" : "pending";
  return "covered";
}
