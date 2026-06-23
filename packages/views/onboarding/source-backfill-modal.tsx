"use client";

/**
 * Compatibility export for older shells that still import the source
 * backfill component. The internal-team build does not collect acquisition
 * source data, so this component deliberately renders nothing.
 */
export function SourceBackfillModal() {
  return null;
}

SourceBackfillModal.displayName = "SourceBackfillModal";
