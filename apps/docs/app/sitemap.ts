import type { MetadataRoute } from "next";
import { source } from "@/lib/source";
import { i18n } from "@/lib/i18n";
import { absoluteDocsUrl } from "@/lib/site";

/**
 * Dynamic sitemap — pulls the full page list from Fumadocs' source at build
 * time. Each logical page emits one Chinese entry.
 *
 * Served at `/docs/sitemap.xml` (because of basePath). The root
 * `apps/web/app/robots.ts` references this URL so crawlers discover it.
 */
export default function sitemap(): MetadataRoute.Sitemap {
  // Group pages by canonical slug.
  const bySlug = new Map<string, Map<string, string>>();

  for (const { language, pages } of source.getLanguages()) {
    for (const page of pages) {
      const slugKey = page.slugs.join("/");
      const languages = bySlug.get(slugKey) ?? new Map<string, string>();
      languages.set(language, page.url);
      bySlug.set(slugKey, languages);
    }
  }

  const entries: MetadataRoute.Sitemap = [];

  for (const languages of bySlug.values()) {
    // Canonical is the Chinese URL.
    const canonicalRelative =
      languages.get(i18n.defaultLanguage) ?? languages.values().next().value;
    if (!canonicalRelative) continue;

    const alternates: Record<string, string> = {};
    for (const [lang, relative] of languages) {
      alternates[lang] = absoluteDocsUrl(relative);
    }
    alternates["x-default"] = absoluteDocsUrl(canonicalRelative);

    entries.push({
      url: absoluteDocsUrl(canonicalRelative),
      alternates: { languages: alternates },
    });
  }

  return entries;
}
