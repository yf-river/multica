import { source } from "@/lib/source";
import {
  DocsPage,
  DocsBody,
  DocsDescription,
  DocsTitle,
} from "fumadocs-ui/page";
import { notFound } from "next/navigation";
import defaultMdxComponents from "fumadocs-ui/mdx";
import type { Metadata } from "next";
import { docsAlternates } from "@/lib/site";
import { i18n } from "@/lib/i18n";
import { LocaleLink } from "@/components/locale-link";

export default async function Page(props: {
  params: Promise<{ lang: string; slug: string[] }>;
}) {
  const { slug } = await props.params;
  const page = source.getPage(slug, i18n.defaultLanguage);
  if (!page) notFound();

  const MDX = page.data.body;

  return (
    <DocsPage toc={page.data.toc}>
      <DocsTitle>{page.data.title}</DocsTitle>
      <DocsDescription>{page.data.description}</DocsDescription>
      <DocsBody>
        <MDX components={{ ...defaultMdxComponents, a: LocaleLink }} />
      </DocsBody>
    </DocsPage>
  );
}

export function generateStaticParams() {
  return source.generateParams().filter(({ slug }) => slug.length > 0);
}

export async function generateMetadata(props: {
  params: Promise<{ lang: string; slug: string[] }>;
}): Promise<Metadata> {
  const { slug } = await props.params;
  const page = source.getPage(slug, i18n.defaultLanguage);
  if (!page) notFound();

  return {
    title: page.data.title,
    description: page.data.description,
    alternates: docsAlternates(slug),
  };
}
