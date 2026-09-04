import { DEFAULT_LOCALE, type SupportedLocale } from "./types";

export function matchLocale(_candidates: string[]): SupportedLocale {
  return DEFAULT_LOCALE;
}
