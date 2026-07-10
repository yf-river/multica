"use client";

import { Suspense, useCallback, useEffect } from "react";
import { useRouter, usePathname, useSearchParams } from "next/navigation";
import {
  NavigationProvider,
  subscribeInternalNavigation,
  type NavigationAdapter,
} from "@multica/views/navigation";

function NavigationProviderInner({
  children,
}: {
  children: React.ReactNode;
}) {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();

  const pushWithFallback = useCallback((path: string) => {
    router.push(path);
    scheduleBrowserNavigationFallback(path, "push");
  }, [router]);
  const replaceWithFallback = useCallback((path: string) => {
    router.replace(path);
    scheduleBrowserNavigationFallback(path, "replace");
  }, [router]);

  useEffect(
    () => subscribeInternalNavigation(pushWithFallback),
    [pushWithFallback],
  );

  const adapter: NavigationAdapter = {
    push: pushWithFallback,
    replace: replaceWithFallback,
    back: () => router.back(),
    pathname,
    searchParams: new URLSearchParams(searchParams.toString()),
    getShareableUrl: (path: string) =>
      typeof window === "undefined" ? path : window.location.origin + path,
    // router.prefetch is a no-op in dev mode by Next.js design; in production
    // it warms the RSC payload + route chunk so the next push() commits with
    // no network round-trip. Safe to call repeatedly — Next dedupes internally.
    prefetch: (path: string) => {
      router.prefetch(path);
    },
  };

  return <NavigationProvider value={adapter}>{children}</NavigationProvider>;
}

function scheduleBrowserNavigationFallback(path: string, mode: "push" | "replace") {
  if (typeof window === "undefined") return;
  const before = currentPathWithSearch();
  const target = new URL(path, window.location.origin);
  const targetPath = `${target.pathname}${target.search}${target.hash}`;
  if (before === targetPath) return;
  window.setTimeout(() => {
    const current = currentPathWithSearch();
    if (current !== before || current === targetPath) return;
    if (mode === "replace") window.location.replace(path);
    else window.location.assign(path);
  }, 1_000);
}

function currentPathWithSearch() {
  return `${window.location.pathname}${window.location.search}${window.location.hash}`;
}

export function WebNavigationProvider({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <Suspense>
      <NavigationProviderInner>{children}</NavigationProviderInner>
    </Suspense>
  );
}
