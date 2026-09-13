"use client";

/**
 * 官网「关于」页（客户端组件）：标题 + 逐字拆解的「Multica」名字梗文案 +
 * 若干段落 + GitHub CTA，整体包在官网导航与页脚之间。
 */
import Link from "next/link";
import { LandingHeader } from "./landing-header";
import { LandingFooter } from "./landing-footer";
import { GitHubMark, githubUrl } from "./shared";
import { useLocale } from "../i18n";

/**
 * 关于页内容主体。
 *
 * @returns 关于页整页布局。
 */
export function AboutPageClient() {
  const { t } = useLocale();
  const n = t.about.nameLine;

  return (
    <>
      <LandingHeader variant="light" />
      <main className="bg-white text-[#0a0d12]">
        <div className="mx-auto max-w-[720px] px-4 py-16 sm:px-6 sm:py-20 lg:py-24">
          <h1 className="landing-serif text-[2.6rem] leading-[1.05] tracking-[-0.03em] sm:text-[3.4rem]">
            {t.about.title}
          </h1>
          <div className="mt-8 space-y-6 text-body-lg leading-[1.8] text-[#0a0d12]/70 sm:text-title-sm">
            <p>
              {n.prefix}
              <strong className="font-semibold text-[#0a0d12]">
                {n.mul}
              </strong>
              {n.tiplexed}
              <strong className="font-semibold text-[#0a0d12]">
                {n.i}
              </strong>
              {n.nformationAnd}
              <strong className="font-semibold text-[#0a0d12]">
                {n.c}
              </strong>
              {n.omputing}
              <strong className="font-semibold text-[#0a0d12]">
                {n.a}
              </strong>
              {n.gent}
            </p>
            {t.about.paragraphs.map((p, i) => (
              <p key={i}>{p}</p>
            ))}
          </div>

          <div className="mt-12">
            <Link
              href={githubUrl}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-2.5 rounded-[12px] bg-[#0a0d12] px-5 py-3 text-body font-semibold text-white transition-colors hover:bg-[#0a0d12]/88"
            >
              <GitHubMark className="size-4" />
              {t.about.cta}
            </Link>
          </div>
        </div>
      </main>
      <LandingFooter />
    </>
  );
}
