import { cache } from "react";
import type { SupportedLocale } from "@multica/core/i18n";

export const getRequestLocale = cache(
  async (): Promise<SupportedLocale> => "zh-Hans",
);
