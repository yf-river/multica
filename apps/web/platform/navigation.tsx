"use client";

/**
 * Web 端的导航适配器：把 Next.js 路由能力实现成 @multica/views/navigation 要求的
 * NavigationAdapter（含 pathname/searchParams/hash 的实时读取与站内链接事件监听）。
 */
import { Suspense, useEffect, useSyncExternalStore } from "react";
import { useRouter, usePathname, useSearchParams } from "next/navigation";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "@multica/views/navigation";
import { canGoBackInApp } from "./in-app-history";

/**
 * `multica:navigate` 桥的 Web 半边——共享内容（评论、聊天、任务描述）里的链接解析到
 * 站内目标时会触发该事件。普通点击（push）就是一次 router.push；
 * 带修饰键的点击在 Web 上一般到不了这里（真实 <a> 会交给浏览器默认行为），
 * 但编辑器必须拦截每一次点击（contenteditable 里的锚点不会原生跳转），
 * 对这些场景 `window.open` 是 Web 上最接近的做法：JS 无法打开后台标签页，
 * 所以「后台/前台标签页」两种意图最终都会落成一个前台浏览器标签页。
 */
function useInternalLinkHandler(router: ReturnType<typeof useRouter>) {
  useEffect(() => {
    const handler = (e: Event) => {
      const detail = (
        e as CustomEvent<{ path?: string; disposition?: string }>
      ).detail;
      const path = detail?.path;
      if (!path) return;
      if (
        detail?.disposition === "background-tab" ||
        detail?.disposition === "foreground-tab"
      ) {
        window.open(
          window.location.origin + path,
          "_blank",
          "noopener,noreferrer",
        );
        return;
      }
      router.push(path);
    };
    window.addEventListener("multica:navigate", handler);
    return () => window.removeEventListener("multica:navigate", handler);
  }, [router]);
}

/**
 * hash 是 Next.js 从不暴露的纯客户端状态：usePathname() 会丢弃它，
 * router.replace("/x#y") 也只是改写 window.location 而不会触发一次自己的渲染。
 * 用外部 store 读取它，每次渲染都重新读 URL，并在那些「绕过 React」的变更事件上重渲染，
 * 这样 adapter.hash 永远不会是过期的拷贝。
 */
function subscribeToHash(onStoreChange: () => void): () => void {
  window.addEventListener("hashchange", onStoreChange);
  window.addEventListener("popstate", onStoreChange);
  return () => {
    window.removeEventListener("hashchange", onStoreChange);
    window.removeEventListener("popstate", onStoreChange);
  };
}

function NavigationProviderInner({
  children,
}: {
  children: React.ReactNode;
}) {
  const router = useRouter();
  const pathname = usePathname();
  const searchParams = useSearchParams();
  const hash = useSyncExternalStore(
    subscribeToHash,
    () => window.location.hash,
    () => "",
  );
  useInternalLinkHandler(router);

  const adapter: NavigationAdapter = {
    push: router.push,
    replace: router.replace,
    back: router.back,
    forward: router.forward,
    canGoBack: canGoBackInApp,
    pathname,
    searchParams: new URLSearchParams(searchParams.toString()),
    hash,
    getShareableUrl: (path: string) =>
      typeof window === "undefined" ? path : window.location.origin + path,
    // router.prefetch 在 dev 模式下按 Next.js 设计是空操作；生产环境它会预热
    // RSC 负载与路由 chunk，让下一次 push() 免去网络往返即可提交。
    // 可安全重复调用——Next 内部会去重。
    prefetch: (path: string) => {
      router.prefetch(path);
    },
  };

  return <NavigationProvider value={adapter}>{children}</NavigationProvider>;
}

/**
 * Web 导航 Provider（导出组件）。
 *
 * 用 Suspense 包住内部实现：useSearchParams 在 app router 里需要 Suspense 边界。
 *
 * @param props.children - 子树。
 * @returns 提供导航适配器的上下文。
 */
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
