import type { User } from "../types";
/**
 * Kept for compatibility with older imports. Source attribution prompts
 * are disabled in the internal-team build, so the cap is no longer used
 * to decide whether a modal should appear.
 */
export const SOURCE_BACKFILL_MAX_DISMISSALS = 3;

/**
 * Source attribution is a growth prompt, not part of the internal
 * production workflow. Always return false so mounted legacy callers
 * cannot show the backfill dialog.
 */
export function needsSourceBackfill(
  _user: User | null | undefined,
  _dismissCount: number,
): boolean {
  return false;
}
