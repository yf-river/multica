/**
 * 官网「客户案例」列表页（服务端组件）：加载当前 locale 的案例集，
 * 按更新时间倒序排列并渲染成带头图的卡片网格。
 */
import type { Metadata } from "next";
import Link from "next/link";
import Image from "next/image";
import { LandingHeader } from "@/features/landing/components/landing-header";
import { LandingFooter } from "@/features/landing/components/landing-footer";
import { getUseCasePagesForLocale } from "@/lib/use-cases-source";
import { getUseCaseLocale, useCaseText } from "@/lib/use-cases-i18n";

/** 案例 MDX 里可选的扩展 frontmatter 字段。 */
type ExtraFrontmatter = {
  category?: string;
  updated_at?: string;
  hero_image?: string;
};

/** 生成案例列表页的 SEO 元信息（标题/描述来自 i18n 文案）。 */
export async function generateMetadata(): Promise<Metadata> {
  const locale = await getUseCaseLocale();
  const text = useCaseText["zh-Hans"];
  return {
    title: text.indexMetadataTitle,
    description: text.indexMetadataDescription,
    openGraph: {
      title: text.indexMetadataTitle,
      description: text.indexMetadataDescription,
      url: "/usecases",
    },
    alternates: {
      canonical: "/usecases",
    },
  };
}

/**
 * 案例列表页默认导出。
 *
 * @returns 深色标题区 + 案例卡片网格 + 页脚。
 */
export default async function UseCasesIndexPage() {
  const locale = await getUseCaseLocale();
  const text = useCaseText["zh-Hans"];
  const pages = getUseCasePagesForLocale(locale)
    .slice()
    .sort((a, b) => {
      // 按 updated_at 倒序；缺日期的条目用 1900-01-01 占位排到最底，
      // 而不是拿空字符串比较（否则多个条目都缺日期时排序结果不稳定）。
      const da = (a.data as ExtraFrontmatter).updated_at ?? "1900-01-01";
      const db = (b.data as ExtraFrontmatter).updated_at ?? "1900-01-01";
      return db.localeCompare(da);
    });

  return (
    <>
      <div className="relative">
        <LandingHeader variant="dark" />
        <section className="relative overflow-hidden bg-[#05070b] text-white">
          <div className="relative z-10 mx-auto max-w-[1120px] px-4 pb-20 pt-32 text-center sm:px-6 sm:pt-40 lg:px-8 lg:pb-24">
            <h1 className="mx-auto max-w-[880px] landing-serif text-[3rem] leading-[1.02] tracking-[-0.035em] drop-shadow-[0_10px_34px_rgba(0,0,0,0.32)] sm:text-[4rem] lg:text-[5rem]">
              {text.indexTitle}
            </h1>
            <p className="mx-auto mt-6 max-w-[620px] text-body-lg leading-7 text-white/84 sm:text-title">
              {text.indexSubtitle}
            </p>
          </div>
        </section>
      </div>

      <section className="bg-white py-20 text-[#0a0d12] sm:py-24">
        <div className="mx-auto max-w-[1120px] px-4 sm:px-6 lg:px-8">
          <div className="grid grid-cols-1 gap-x-6 gap-y-14 sm:grid-cols-2 lg:grid-cols-3">
            {pages.map((page) => {
              const extra = page.data as ExtraFrontmatter;
              const heroImage = extra.hero_image;
              const category = extra.category;
              // 从页面 slug 拼公开路由 URL 而不是用 page.url：Fumadocs 会给本地化
              // URL 加前缀，而我们的公开路由不带前缀、按请求动态解析 locale。
              const href = `/usecases/${page.slugs.join("/")}`;

              return (
                <Link key={href} href={href} className="group flex flex-col">
                  <div className="relative aspect-[4/3] overflow-hidden rounded-lg bg-[#f5f5f5]">
                    {heroImage ? (
                      <Image
                        src={heroImage}
                        alt=""
                        fill
                        sizes="(max-width: 640px) 100vw, (max-width: 1024px) 50vw, 360px"
                        className="object-cover transition-transform duration-300 group-hover:scale-[1.02]"
                      />
                    ) : null}
                  </div>
                  {category ? (
                    <div className="mt-5 text-caption uppercase tracking-[0.14em] text-[#0a0d12]/50">
                      {category}
                    </div>
                  ) : null}
                  <h3 className="mt-2 text-title font-semibold leading-[1.35] text-[#0a0d12] sm:text-title-lg">
                    {page.data.title}
                  </h3>
                  {page.data.description ? (
                    <p className="mt-2 text-body leading-[1.6] text-[#0a0d12]/60">
                      {page.data.description}
                    </p>
                  ) : null}
                  <span className="mt-3 text-label text-[#0a0d12]/70 transition-colors group-hover:text-[#0a0d12]">
                    {text.cardReadMore}
                  </span>
                </Link>
              );
            })}
          </div>
        </div>
      </section>

      <LandingFooter />
    </>
  );
}
