// Server-safe i18n entry: zero React imports.
// React-side helpers (I18nProvider, createI18n) live in
// "@multica/core/i18n/react" — split because Next.js gives RSC a vendored
// React build that lacks createContext, and react-i18next's top-level
// React.createContext() call would crash any non-client load of this file.
export type {
  LocaleResources,
  SupportedLocale,
} from "./types";
export { DEFAULT_LOCALE, SUPPORTED_LOCALES } from "./types";
export { matchLocale, pickLocale } from "./pick-locale";
