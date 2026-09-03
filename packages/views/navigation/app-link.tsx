"use client";

import { forwardRef } from "react";
import { resolveClickIntent } from "./click-intent";
import { useNavigation } from "./context";

interface AppLinkProps extends React.AnchorHTMLAttributes<HTMLAnchorElement> {
  href: string;
}

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
      // Caller's onClick runs BEFORE any navigation, on every path, so:
      //   - synchronous side effects (close popover, clear selection, blur
      //     the trigger) land in the same tick rather than getting deferred
      //     behind the transition, and
      //   - calling preventDefault() inside it cancels the navigation
      //     entirely — the escape hatch drag guards and permission gates
      //     need, and the same one onAuxClick already offers.
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
        // Referrer is same-origin noise here and noopener hygiene applies
        // even though the destination is our own app.
        rel={target === "_blank" ? "noopener noreferrer" : undefined}
        // Spread props first so that the navigation handlers below cannot be
        // silently overridden by a caller passing
        // onClick/onAuxClick/onMouseEnter/onFocus through {...rest}. AppLink
        // owns these four events.
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
