import { DEFAULT_LOCALE, type SupportedLocale } from "./types";

export function matchLocale(candidates: string[]): SupportedLocale {
  void candidates;
  return DEFAULT_LOCALE;
}

export function pickLocale(): SupportedLocale {
  return DEFAULT_LOCALE;
}
