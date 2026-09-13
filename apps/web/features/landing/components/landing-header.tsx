"use client";

/**
 * 官网顶部导航栏（客户端组件）：深色变体用于覆盖在深色 Hero 上，浅色变体用于
 * 白底页面。包含 Logo、导航链接（案例/文档/更新日志）、GitHub 星标入口，
 * 以及按登录态区分的 CTA（未登录「开始使用」/ 已登录「进入控制台」）；
 * 移动端收起为汉堡菜单。
 */
import { useState } from "react";
import Link from "next/link";
import { Menu, X } from "lucide-react";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { cn } from "@multica/ui/lib/utils";
import { useAuthStore } from "@multica/core/auth";
import { docsHrefForLocale, useLocale } from "../i18n";
import { useDashboardCtaHref } from "../utils/use-dashboard-cta";
import { formatStarCount, useGithubStars } from "../utils/use-github-stars";
import { GitHubMark, githubUrl, headerButtonClassName } from "./shared";

/**
 * 官网头部导航。
 *
 * @param props.variant - 配色变体：`dark` 适配深色 Hero（无背景、白色字），
 *   `light` 适配白底页面（带底部边框）。
 * @returns 导航栏（桌面横排 + 移动端汉堡菜单）。
 */
export function LandingHeader({
  variant = "dark",
}: {
  variant?: "dark" | "light";
}) {
  const { t, locale } = useLocale();
  const user = useAuthStore((s) => s.user);
  const stars = useGithubStars();
  const starsLabel = stars != null ? formatStarCount(stars) : null;
  const [isMenuOpen, setIsMenuOpen] = useState(false);
  const docsHref = docsHrefForLocale(locale);
  const navLinks = [
    { href: "/usecases", label: t.header.useCases },
    { href: docsHref, label: t.header.docs },
    { href: "/changelog", label: t.header.changelog },
  ];
  const ctaHref = useDashboardCtaHref();
  const ctaLabel = user ? t.header.dashboard : t.header.cta;

  return (
    <header
      className={cn(
        "relative inset-x-0 top-0 z-30",
        variant === "dark"
          ? "absolute bg-transparent"
          : "border-b border-[#0a0d12]/8 bg-white",
      )}
    >
      <div className="mx-auto flex h-[76px] max-w-[1320px] items-center justify-between px-4 sm:px-6 lg:px-8">
        <div className="flex min-w-0 items-center gap-6 lg:gap-8">
          <Link href="/" className="flex shrink-0 items-center gap-3">
            <MulticaIcon
              className={cn(
                "size-5",
                variant === "dark" ? "text-white" : "text-[#0a0d12]",
              )}
              noSpin
            />
            <span
              className={cn(
                "text-title font-semibold tracking-[0.04em] lowercase sm:text-title-lg",
                variant === "dark" ? "text-white/92" : "text-[#0a0d12]",
              )}
            >
              multica
            </span>
          </Link>

          <nav
            aria-label={t.header.navigation}
            className="hidden items-center gap-1 md:flex"
          >
            {navLinks.map((link) => (
              <Link
                key={link.href}
                href={link.href}
                className={navLinkClassName(variant)}
              >
                {link.label}
              </Link>
            ))}
          </nav>
        </div>

        <div className="flex shrink-0 items-center gap-2 sm:gap-2.5">
          <button
            type="button"
            aria-label={isMenuOpen ? t.header.closeMenu : t.header.openMenu}
            aria-expanded={isMenuOpen}
            onClick={() => setIsMenuOpen((open) => !open)}
            className={cn(
              headerButtonClassName("ghost", variant),
              "px-3 md:hidden",
            )}
          >
            {isMenuOpen ? (
              <X className="size-4" aria-hidden />
            ) : (
              <Menu className="size-4" aria-hidden />
            )}
          </button>
          <Link
            href={githubUrl}
            target="_blank"
            rel="noreferrer"
            className={cn(
              headerButtonClassName("ghost", variant),
              "hidden lg:inline-flex",
            )}
          >
            <GitHubMark className="size-3.5" />
            {t.header.github}
            {starsLabel ? <GitHubStarsBadge label={starsLabel} /> : null}
          </Link>
          <Link
            href={ctaHref}
            className={headerButtonClassName("solid", variant)}
          >
            {ctaLabel}
          </Link>
        </div>
      </div>

      {isMenuOpen ? (
        <div
          className={cn(
            "absolute left-4 right-4 top-[calc(100%+8px)] z-50 rounded-[14px] border p-2 shadow-[0_18px_60px_rgba(0,0,0,0.18)] backdrop-blur-xl md:hidden",
            variant === "dark"
              ? "border-white/14 bg-[#070a10]/95 text-white"
              : "border-[#0a0d12]/10 bg-white text-[#0a0d12]",
          )}
        >
          <nav aria-label={t.header.navigation} className="flex flex-col">
            {navLinks.map((link) => (
              <Link
                key={link.href}
                href={link.href}
                onClick={() => setIsMenuOpen(false)}
                className={mobileNavLinkClassName(variant)}
              >
                {link.label}
              </Link>
            ))}
          </nav>
          <div
            className={cn(
              "mt-2 border-t pt-2",
              variant === "dark" ? "border-white/10" : "border-[#0a0d12]/8",
            )}
          >
            <Link
              href={githubUrl}
              target="_blank"
              rel="noreferrer"
              onClick={() => setIsMenuOpen(false)}
              className={mobileNavLinkClassName(variant)}
            >
              <GitHubMark className="size-3.5" />
              {t.header.github}
              {starsLabel ? <GitHubStarsBadge label={starsLabel} /> : null}
            </Link>
          </div>
        </div>
      ) : null}
    </header>
  );
}

/** 加在 GitHub 按钮里的星标徽章：一条浅色细分隔线 + 紧凑数字（如 37.6k）。
 *  不放星星图标——放在按钮语境里，数字本身就能读作「Star 数」。
 *  文字颜色继承按钮，因此深/浅两种头部变体都自动适配。 */
function GitHubStarsBadge({ label }: { label: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 tabular-nums">
      <span aria-hidden className="h-3 w-px bg-current opacity-25" />
      {label}
    </span>
  );
}

/** 桌面端导航链接样式（随明暗变体切换配色）。 */
function navLinkClassName(variant: "dark" | "light") {
  return cn(
    "inline-flex h-9 items-center rounded-[9px] px-3 text-label font-medium transition-colors",
    variant === "dark"
      ? "text-white/72 hover:bg-white/8 hover:text-white"
      : "text-[#0a0d12]/62 hover:bg-[#0a0d12]/5 hover:text-[#0a0d12]",
  );
}

/** 移动端汉堡菜单里的链接样式。 */
function mobileNavLinkClassName(variant: "dark" | "light") {
  return cn(
    "flex min-h-11 items-center gap-2 rounded-[10px] px-3 text-body font-medium transition-colors",
    variant === "dark"
      ? "text-white/76 hover:bg-white/8 hover:text-white"
      : "text-[#0a0d12]/68 hover:bg-[#0a0d12]/5 hover:text-[#0a0d12]",
  );
}
