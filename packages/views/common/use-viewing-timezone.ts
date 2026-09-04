import { useAuthStore } from "@multica/core/auth";
import { browserTimezone } from "./timezone-select";

// Viewer's IANA tz: stored user preference, else browser-detected, else UTC.
export function useViewingTimezone(): string {
  const stored = useAuthStore((s) => s.user?.timezone ?? null);
  if (stored && stored.trim() !== "") {
    try {
      new Intl.DateTimeFormat(undefined, { timeZone: stored });
      return stored;
    } catch {
      // Older records can contain labels that are not valid IANA timezones.
      // Treat them like an unset preference so date bucketing cannot crash a page.
    }
  }
  return browserTimezone();
}
