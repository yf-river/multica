import { useAuthStore } from "@multica/core/auth";
import { browserTimezone, isValidTimeZone } from "./timezone-select";

// Viewer's IANA tz: stored user preference, else browser-detected, else UTC.
export function useViewingTimezone(): string {
  const stored = useAuthStore((s) => s.user?.timezone ?? null);
  if (stored && stored.trim() !== "" && isValidTimeZone(stored)) return stored;
  return browserTimezone();
}
