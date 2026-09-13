"use client";

import { useEffect, useState } from "react";

/**
 * 供官网头部「GitHub」按钮展示实时 Star 数的 hook。
 *
 * 刻意放在客户端请求：徽标所在的 LandingHeader 会在每个营销页渲染，一次客户端请求
 * 就覆盖所有页面，不必把服务端数值穿过七八处渲染位置。每个访客用自己的 IP 调 GitHub
 * API，也绕开了服务端 `github-release.ts` 取数时需要靠 PAT 解决的共享出口 IP 限流。
 *
 * 结果按「模块级」缓存（连同进行中的 Promise）：落地页之间客户端跳转时直接复用第一次
 * 的结果，不会重复请求。失败则缓存 `null` 避免进入重试循环——按钮退化为普通
 * 「GitHub」文案即可。
 */

const REPO = "multica-ai/multica";

// `undefined` = 从未请求；`number` = 已取到数量；`null` = 请求失败。
let cachedStars: number | null | undefined;
let inFlight: Promise<number | null> | null = null;

/** 读取 Star 数：命中缓存直接返回，否则发起一次请求并缓存结果。 */
async function loadStars(): Promise<number | null> {
  if (cachedStars !== undefined) return cachedStars;
  if (inFlight) return inFlight;

  inFlight = fetch(`https://api.github.com/repos/${REPO}`, {
    headers: { Accept: "application/vnd.github+json" },
  })
    .then((res) => {
      if (!res.ok) throw new Error(`GitHub API responded ${res.status}`);
      return res.json() as Promise<{ stargazers_count?: unknown }>;
    })
    .then((data) => {
      const count =
        typeof data.stargazers_count === "number" ? data.stargazers_count : null;
      cachedStars = count;
      return count;
    })
    .catch(() => {
      cachedStars = null;
      return null;
    })
    .finally(() => {
      inFlight = null;
    });

  return inFlight;
}

/**
 * 读取 GitHub Star 数（供头部徽标用）。未取到或失败时返回 null。
 *
 * @returns 当前仓库的 Star 数，或 null（加载中/失败）。
 */
export function useGithubStars(): number | null {
  const [stars, setStars] = useState<number | null>(cachedStars ?? null);

  useEffect(() => {
    let active = true;
    void loadStars().then((count) => {
      if (active && count != null) setStars(count);
    });
    return () => {
      active = false;
    };
  }, []);

  return stars;
}

/**
 * 紧凑的 Star 数格式，对齐 GitHub 仓库头部样式：千/百万只留一位小数并去掉尾部的
 * ".0"（如 "1k"、"37.6k"、"1.2m"），低于 1000 原样输出。舍入方式与 GitHub 的
 * `toFixed(1)` 一致，保证徽标和仓库页显示一致。
 *
 * @param n - 原始 Star 数。
 * @returns 紧凑展示文本。
 */
export function formatStarCount(n: number): string {
  if (n >= 1_000_000) {
    return `${(n / 1_000_000).toFixed(1).replace(/\.0$/, "")}m`;
  }
  if (n >= 1_000) {
    return `${(n / 1_000).toFixed(1).replace(/\.0$/, "")}k`;
  }
  return String(n);
}
