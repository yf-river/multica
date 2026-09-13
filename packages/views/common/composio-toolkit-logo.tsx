// Composio 工具包 Logo 组件：按 slug 拼出 logo 地址，分别渲染亮/暗两种主题，加载失败时逐级回退，最后落到首字母兜底。
"use client";

import { useEffect, useState } from "react";
import { cn } from "@multica/ui/lib/utils";

const COMPOSIO_LOGO_BASE_URL = "https://logos.composio.dev/api";

/**
 * 拼出 Composio 工具包 logo 的地址。
 * @param slug 工具包的 slug 标识。
 * @param theme 主题，传 "dark" 时请求深色版 logo。
 * @returns logo 图片 URL，slug 为空时返回空字符串。
 */
export function composioToolkitLogoUrl(slug: string, theme?: "dark") {
  const normalized = slug.trim().toLowerCase();
  if (!normalized) return "";
  const base = `${COMPOSIO_LOGO_BASE_URL}/${encodeURIComponent(normalized)}`;
  return theme === "dark" ? `${base}?theme=dark` : base;
}

function uniqueNonEmpty(values: Array<string | null | undefined>) {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const value of values) {
    const normalized = value?.trim();
    if (!normalized || seen.has(normalized)) continue;
    seen.add(normalized);
    out.push(normalized);
  }
  return out;
}

/**
 * Composio 工具包 Logo。分别渲染亮色/深色两个 `<img>`，各自维护失败计数，
 * 按「兜底 logo → 动态浅色 → 动态深色」的顺序回退；全部失败后显示名称首字母。
 * @param slug 工具包 slug，用于拼动态 logo 地址。
 * @param name 显示名称，用于首字母兜底。
 * @param fallbackLogo 调用方提供的兜底 logo 地址。
 * @param className 附加样式。
 */
export function ComposioToolkitLogo({
  slug,
  name,
  fallbackLogo,
  className,
}: {
  slug: string;
  name?: string;
  fallbackLogo?: string | null;
  className?: string;
}) {
  const label = name || slug;
  const initial = label.charAt(0).toUpperCase();
  const dynamicLightLogo = composioToolkitLogoUrl(slug);
  const dynamicDarkLogo = composioToolkitLogoUrl(slug, "dark");
  const lightSources = uniqueNonEmpty([fallbackLogo, dynamicLightLogo]);
  const darkSources = uniqueNonEmpty([dynamicDarkLogo, fallbackLogo, dynamicLightLogo]);
  const [failedLightSources, setFailedLightSources] = useState(0);
  const [failedDarkSources, setFailedDarkSources] = useState(0);

  useEffect(() => {
    setFailedLightSources(0);
    setFailedDarkSources(0);
  }, [slug, fallbackLogo]);

  const imgClassName = cn("h-8 w-8 shrink-0 rounded bg-muted object-contain", className);
  const fallbackClassName = cn(
    "h-8 w-8 shrink-0 items-center justify-center rounded bg-muted text-caption font-semibold text-muted-foreground",
    className,
  );
  const lightSrc = lightSources[failedLightSources];
  const darkSrc = darkSources[failedDarkSources];

  return (
    <>
      {lightSrc ? (
        <img
          src={lightSrc}
          alt=""
          className={cn(imgClassName, "dark:hidden")}
          onError={() => setFailedLightSources((n) => n + 1)}
        />
      ) : (
        <div className={cn(fallbackClassName, "flex dark:hidden")}>{initial}</div>
      )}
      {darkSrc ? (
        <img
          src={darkSrc}
          alt=""
          className={cn(imgClassName, "hidden dark:block")}
          onError={() => setFailedDarkSources((n) => n + 1)}
        />
      ) : (
        <div className={cn(fallbackClassName, "hidden dark:flex")}>{initial}</div>
      )}
    </>
  );
}
