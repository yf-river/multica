"use client";

/**
 * 富文本模块：近视口富块挂载所用的滚动根。
 * 通过 context 发布「富块应该相对哪个元素判断是否接近视口」。
 *
 * IntersectionObserver 会拿它的 `root` 做裁剪。不设置时用浏览器视口，
 * 那 `rootMargin` 就会扩大*视口*框——这对嵌套滚动容器里的块毫无意义。
 * Chat 的列表在自己的元素里滚动（`Virtuoso customScrollParent`），所以
 * 用默认 root 时，一条消息只有已经可见了才被算作「接近」，预加载的意义
 * 就没了。
 *
 * 在自己元素里滚动的界面把滚动根发布到这里。随页面滚动的界面（issue 描述、
 * 评论）什么都不发布、保留视口 root，这对它们才是正确的。
 */

import { createContext, useContext, type ReactNode } from "react";

const RichContentScrollRootContext = createContext<HTMLElement | null>(null);

/**
 * 提供富块滚动根的 Provider。
 * @param scrollRoot 滚动根元素，null 表示用浏览器视口。
 * @param children 子内容。
 */
export function RichContentScrollRootProvider({
  scrollRoot,
  children,
}: {
  scrollRoot: HTMLElement | null;
  children: ReactNode;
}) {
  return (
    <RichContentScrollRootContext.Provider value={scrollRoot}>
      {children}
    </RichContentScrollRootContext.Provider>
  );
}

/**
 * 读取富块应相对哪个元素判断「接近视口」，返回 null 表示用浏览器视口。
 */
export function useRichContentScrollRoot(): HTMLElement | null {
  return useContext(RichContentScrollRootContext);
}
