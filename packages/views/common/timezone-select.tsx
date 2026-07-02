// Curated fallback list used when the runtime lacks `Intl.supportedValuesOf`.
const COMMON_TIMEZONES = [
  "UTC",
  "America/Los_Angeles",
  "America/Denver",
  "America/Chicago",
  "America/New_York",
  "America/Sao_Paulo",
  "Europe/London",
  "Europe/Berlin",
  "Europe/Paris",
  "Europe/Moscow",
  "Africa/Cairo",
  "Asia/Dubai",
  "Asia/Kolkata",
  "Asia/Bangkok",
  "Asia/Shanghai",
  "Asia/Singapore",
  "Asia/Tokyo",
  "Australia/Sydney",
  "Pacific/Auckland",
];

let cachedBrowserTZ: string | null = null;
export function browserTimezone(): string {
  if (cachedBrowserTZ !== null) return cachedBrowserTZ;
  try {
    const detected = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
    cachedBrowserTZ = isValidTimeZone(detected) ? detected : "UTC";
  } catch {
    cachedBrowserTZ = "UTC";
  }
  return cachedBrowserTZ;
}

export function isValidTimeZone(value: string): boolean {
  try {
    Intl.DateTimeFormat("en-US", { timeZone: value }).format(0);
    return true;
  } catch {
    return false;
  }
}

type IntlWithSupportedValues = typeof Intl & {
  supportedValuesOf?: (key: "timeZone") => string[];
};

function supportedTimezones(): string[] {
  try {
    const supported = (Intl as IntlWithSupportedValues).supportedValuesOf?.(
      "timeZone",
    );
    return supported && supported.length > 0 ? supported : COMMON_TIMEZONES;
  } catch {
    return COMMON_TIMEZONES;
  }
}

export function timezoneOptions(current: string): string[] {
  const browser = browserTimezone();
  return Array.from(
    new Set([current, browser, ...COMMON_TIMEZONES, ...supportedTimezones()]),
  ).filter(Boolean);
}
