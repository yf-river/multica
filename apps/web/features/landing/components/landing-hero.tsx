"use client";

/**
 * 官网首页首屏（Hero）：深色全屏背景 + 大字标语 + CTA（主 CTA 按登录态区分）
 * + 可兼容工具的 logo 行 + 产品截图预览。
 */
import Image from "next/image";
import Link from "next/link";
import { ArrowRight, Terminal } from "lucide-react";
import { useAuthStore } from "@multica/core/auth";
import { useLocale } from "../i18n";
import { useDashboardCtaHref } from "../utils/use-dashboard-cta";
import {
  ClaudeCodeLogo,
  CodexLogo,
  GeminiCliLogo,
  OpenCodeLogo,
  heroButtonClassName,
} from "./shared";

/**
 * 首页首屏内容。
 *
 * @returns 深色 Hero 区（含背景、标语、按钮、工具列表与产品图）。
 */
export function LandingHero() {
  const { t } = useLocale();
  const user = useAuthStore((s) => s.user);
  const ctaHref = useDashboardCtaHref();

  return (
    <div className="relative min-h-full overflow-hidden bg-[#05070b] text-white">
      <LandingBackdrop />

      <main className="relative z-10">
        <section
          id="product"
          className="mx-auto max-w-[1320px] px-4 pb-16 pt-28 sm:px-6 sm:pt-32 lg:px-8 lg:pb-24 lg:pt-36"
        >
          <div className="mx-auto max-w-[1120px] text-center">
            <h1 className="landing-serif text-[3.65rem] leading-[0.93] tracking-[-0.038em] text-white drop-shadow-[0_10px_34px_rgba(0,0,0,0.32)] sm:text-[4.85rem] lg:text-[6.4rem]">
              {t.hero.headlineLine1}
              <br />
              {t.hero.headlineLine2}
            </h1>

            <p className="mx-auto mt-7 max-w-[820px] text-body-lg leading-7 text-white/84 sm:text-title">
              {t.hero.subheading}
            </p>

            <div className="mt-8 flex flex-wrap items-center justify-center gap-3">
              <Link href={ctaHref} className={heroButtonClassName("solid")}>
                {user ? t.header.dashboard : t.hero.cta}
              </Link>
              <Link
                href="/download"
                className={heroButtonClassName("ghost")}
              >
                <Terminal className="size-4" aria-hidden />
                {t.hero.installCli}
              </Link>
              <Link
                href="/contact-sales"
                className="group inline-flex items-center justify-center gap-1.5 rounded-[12px] px-3 py-3 text-body font-semibold text-white/80 transition-colors hover:text-white"
              >
                {t.hero.talkToSales}
                <ArrowRight
                  className="size-4 transition-transform group-hover:translate-x-0.5"
                  aria-hidden
                />
              </Link>
            </div>
          </div>

          <div className="mt-10 flex flex-wrap items-center justify-center gap-x-6 gap-y-3">
            <span className="text-body-lg text-white/50">
              {t.hero.worksWith}
            </span>
            <div className="flex flex-wrap items-center justify-center gap-x-5 gap-y-3">
              <div className="flex items-center gap-2.5 text-white/80">
                <ClaudeCodeLogo className="size-5" />
                <span className="text-body-lg font-medium">Claude Code</span>
              </div>
              <div className="flex items-center gap-2.5 text-white/80">
                <CodexLogo className="size-5" />
                <span className="text-body-lg font-medium">Codex</span>
              </div>
              <div className="flex items-center gap-2.5 text-white/80">
                <GeminiCliLogo className="size-5" />
                <span className="text-body-lg font-medium">Gemini CLI</span>
              </div>
              <div className="flex items-center gap-2.5 text-white/80">
                <OpenCodeLogo className="size-5" />
                <span className="text-body-lg font-medium">OpenCode</span>
              </div>
            </div>
          </div>

          <div id="preview" className="mt-10 sm:mt-12">
            <ProductImage alt={t.hero.imageAlt} />
          </div>
        </section>
      </main>
    </div>
  );
}

/** 深色背景装饰图容器。 */
function LandingBackdrop() {
  return (
    <div className="pointer-events-none absolute inset-0">
      {/* 首屏背景在首屏内可见，需要与产品图一起预加载，避免白屏闪烁。 */}
      <Image
        src="/images/landing-bg.webp"
        alt=""
        fill
        preload
        className="object-cover object-center"
        sizes="100vw"
      />
    </div>
  );
}

/** 产品界面预览大图。 */
function ProductImage({ alt }: { alt: string }) {
  return (
    <div>
      <div className="relative overflow-hidden border border-white/14">
        <Image
          src="/images/landing-hero.webp"
          alt={alt}
          width={2640}
          height={1781}
          preload
          className="block h-auto w-full"
          sizes="(max-width: 1320px) 100vw, 1320px"
          quality={85}
        />
      </div>
    </div>
  );
}
