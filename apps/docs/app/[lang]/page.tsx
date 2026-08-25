import { source } from "@/lib/source";
import { DocsPage, DocsBody } from "fumadocs-ui/page";
import { notFound } from "next/navigation";
import defaultMdxComponents from "fumadocs-ui/mdx";
import type { Metadata } from "next";
import { DocsHero } from "@/components/hero";
import {
  Byline,
  NumberedCards,
  NumberedCard,
  NumberedSteps,
  Step,
} from "@/components/editorial";
import { i18n } from "@/lib/i18n";
import { homeCopy } from "@/lib/translations";
import { docsAlternates } from "@/lib/site";
import { LocaleLink } from "@/components/locale-link";

// A layout's `generateStaticParams` does NOT cascade — every page that
// wants SSG must declare its own. Without this, both `/docs/` and
// `/docs/zh` (the busiest URLs on the site) render dynamically on every
// request.
export function generateStaticParams() {
  return i18n.languages.map((lang) => ({ lang }));
}

export default function Page() {
  const lang = i18n.defaultLanguage;
  const page = source.getPage([], lang);
  if (!page) notFound();

  const MDX = page.data.body;
  const copy = homeCopy[lang];

  return (
    <DocsPage toc={page.data.toc}>
      <DocsHero
        eyebrow={copy.eyebrow}
        title={
          <>
            {copy.titleLead}
            <em className="font-medium not-italic text-[var(--primary)]">
              {copy.titleAccent}
            </em>
          </>
        }
        subtitle={page.data.description}
      />
      <Byline items={[...copy.byline]} />
      <DocsBody>
        <MDX
          components={{
            ...defaultMdxComponents,
            a: LocaleLink,
            NumberedCards,
            NumberedCard,
            NumberedSteps,
            Step,
          }}
        />
      </DocsBody>
    </DocsPage>
  );
}

export function generateMetadata(): Metadata {
  const page = source.getPage([], i18n.defaultLanguage);
  if (!page) notFound();

  return {
    title: page.data.title,
    description: page.data.description,
    alternates: docsAlternates([]),
  };
}
