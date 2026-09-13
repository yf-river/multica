"use client";

import { forwardRef } from "react";
import { resolveClickIntent } from "./click-intent";
import { useNavigation } from "./context";

/** 应用内链接组件的属性：在原生 <a> 属性之上，href 为必填。 */
interface AppLinkProps extends React.AnchorHTMLAttributes<HTMLAnchorElement> {
  href: string;
}

/**
 * navigation 模块：应用内导航链接。
 * 普通点击走 SPA 内 push（无整页刷新）；带 cmd/ctrl 修饰键、中键或
 * target="_blank" 的点击交给浏览器原生行为。焦点/悬停时预热目标路由。
 * 调用方通过 onClick 传入的处理器先于导航执行，且可在其中 preventDefault
 * 取消导航。
 */
export const AppLink = forwardRef<HTMLAnchorElement, AppLinkProps>(
  function AppLink(
    {
      href,
      children,
      onClick,
      onAuxClick,
      onMouseEnter,
      onFocus,
      target,
      ...props
    },
    ref,
  ) {
    const { push, prefetch } = useNavigation();

    const handleClick = (e: React.MouseEvent<HTMLAnchorElement>) => {
      // 调用方的 onClick 在任何导航之前、在所有路径上都会执行，于是：
      //   - 同步副作用（关 popover、清选择、让触发器失焦）在同一 tick 落定，
      //     不会被推迟到 transition 后面，而且
      //   - 在里面调 preventDefault() 能彻底取消这次导航——这正是拖拽守卫
      //     与权限门需要的逃生门，也是 onAuxClick 早已提供的那一个。
      onClick?.(e);
      if (e.defaultPrevented) return;
      const intent = resolveClickIntent(e);
      if (intent !== "push" || e.shiftKey || target === "_blank") return;
      e.preventDefault();
      push(href);
    };

    const handleAuxClick = (e: React.MouseEvent<HTMLAnchorElement>) => {
      onAuxClick?.(e);
    };

    const handleMouseEnter = (e: React.MouseEvent<HTMLAnchorElement>) => {
      prefetch?.(href);
      onMouseEnter?.(e);
    };

    const handleFocus = (e: React.FocusEvent<HTMLAnchorElement>) => {
      prefetch?.(href);
      onFocus?.(e);
    };

    return (
      <a
        ref={ref}
        href={href}
        target={target}
        // Referrer 在这里是同源噪音；即便目的地是我们自己的应用，noopener
        // 卫生习惯仍然适用。
        rel={target === "_blank" ? "noopener noreferrer" : undefined}
        // 先展开其余 props，让下面的导航处理器不会被调用方通过 {...rest}
        // 传 onClick/onAuxClick/onMouseEnter/onFocus 悄悄覆盖。AppLink
        // 拥有这四个事件。
        {...props}
        onClick={handleClick}
        onAuxClick={handleAuxClick}
        onMouseEnter={handleMouseEnter}
        onFocus={handleFocus}
      >
        {children}
      </a>
    );
  },
);
