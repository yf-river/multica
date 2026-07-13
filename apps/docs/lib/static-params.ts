import { isDocsLang, type Lang } from "@/lib/i18n";

export type DocsStaticParam = {
  lang: Lang;
  slug: string[];
};

type SourceStaticParam = {
  lang: string;
  slug: string[];
};

function paramKey(param: DocsStaticParam): string {
  return `${param.lang}/${param.slug.join("/")}`;
}

export function docsSlugStaticParams(
  params: SourceStaticParam[],
): DocsStaticParam[] {
  const slugParams = params.filter(
    (param): param is DocsStaticParam =>
      param.slug.length > 0 && isDocsLang(param.lang),
  );
  const output: DocsStaticParam[] = [];
  const seen = new Set<string>();

  const addParam = (param: DocsStaticParam) => {
    const key = paramKey(param);
    if (seen.has(key)) return;
    seen.add(key);
    output.push(param);
  };

  for (const param of slugParams) addParam(param);

  return output;
}
