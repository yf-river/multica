"use client";

/**
 * LinkHoverCard — 链接悬停时浮出的卡片。
 *
 * 展示 URL，附「复制」「打开」两个动作。用 portal 挂到 body 上并以
 * position:fixed 定位，以脱离 overflow:hidden 容器。
 * 悬停 300ms 后显示，移出 150ms 后隐藏（鼠标进了卡片则取消隐藏）。
 */

import { useState, useEffect, useCallback, useRef } from "react";
import { createPortal } from "react-dom";
import { computePosition, offset, flip, shift } from "@floating-ui/dom";
import { ExternalLink, Copy } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@multica/ui/components/ui/button";
import { copyText } from "@multica/ui/lib/clipboard";
import { useWorkspaceSlug } from "@multica/core/paths";
import { useAppOrigin } from "../navigation";
import { useT } from "../i18n";
import { openLink, isMentionHref } from "./utils/link-handler";

/** 把超长 URL 在可读处截断，保留 origin 与开头，末尾加省略号。 */
function truncateUrl(url: string, max = 48): string {
  if (url.length <= max) return url;
  try {
    const u = new URL(url);
    const origin = u.origin;
    const rest = url.slice(origin.length);
    if (rest.length <= 10) return url;
    return `${origin}${rest.slice(0, max - origin.length - 1)}…`;
  } catch {
    return `${url.slice(0, max - 1)}…`;
  }
}

// ---------------------------------------------------------------------------
// Hook——带进出延迟地管理悬停状态
// ---------------------------------------------------------------------------

const SHOW_DELAY = 300;
const HIDE_DELAY = 150;

interface HoverState {
  visible: boolean;
  href: string;
  anchorEl: HTMLAnchorElement | null;
}

/**
 * useLinkHover——监听容器内链接悬停，按延迟决定是否展示卡片。
 * @param containerRef 需要扫描链接的容器。
 * @param disabled 停用悬停卡片。
 */
function useLinkHover(containerRef: React.RefObject<HTMLElement | null>, disabled?: boolean) {
  const [state, setState] = useState<HoverState>({ visible: false, href: "", anchorEl: null });
  const showTimer = useRef(0);
  const hideTimer = useRef(0);
  const cardRef = useRef<HTMLDivElement>(null);

  const clearTimers = useCallback(() => {
    clearTimeout(showTimer.current);
    clearTimeout(hideTimer.current);
  }, []);

  // 容器鼠标事件——探测 <a> 悬停
  useEffect(() => {
    const container = containerRef.current;
    if (!container || disabled) return;

    const onMouseOver = (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      const link = target.closest("a") as HTMLAnchorElement | null;
      if (!link) return;
      const href = link.getAttribute("href");
      if (!href || isMentionHref(href)) return;
      // mention 胶囊渲染成 <a class="issue-mention"> / <a
      // class="project-mention">——它们自带丰富的自身信息，URL 悬停卡片多余，
      // 而且它想让你复制的 URL 是站内路径，并非用户期望的可分享链接。
      if (
        link.classList.contains("issue-mention") ||
        link.classList.contains("project-mention")
      ) {
        return;
      }

      clearTimeout(hideTimer.current);
      showTimer.current = window.setTimeout(() => {
        setState({ visible: true, href, anchorEl: link });
      }, SHOW_DELAY);
    };

    const onMouseOut = (e: MouseEvent) => {
      const related = e.relatedTarget as HTMLElement | null;
      // 鼠标移到了悬停卡片上就不隐藏
      if (related && cardRef.current?.contains(related)) return;
      // 鼠标移到同一链接的另一部分就不隐藏
      const link = (e.target as HTMLElement).closest("a");
      if (link && link.contains(related)) return;

      clearTimeout(showTimer.current);
      hideTimer.current = window.setTimeout(() => {
        setState((s) => ({ ...s, visible: false }));
      }, HIDE_DELAY);
    };

    container.addEventListener("mouseover", onMouseOver);
    container.addEventListener("mouseout", onMouseOut);
    return () => {
      container.removeEventListener("mouseover", onMouseOver);
      container.removeEventListener("mouseout", onMouseOut);
      clearTimers();
    };
  }, [containerRef, disabled, clearTimers]);

  // 卡片鼠标事件——悬停在卡片上时保持显示
  const onCardEnter = useCallback(() => {
    clearTimeout(hideTimer.current);
  }, []);

  const onCardLeave = useCallback(() => {
    hideTimer.current = window.setTimeout(() => {
      setState((s) => ({ ...s, visible: false }));
    }, HIDE_DELAY);
  }, []);

  return { ...state, cardRef, onCardEnter, onCardLeave };
}

// ---------------------------------------------------------------------------
// 组件
// ---------------------------------------------------------------------------

/**
 * LinkHoverCard——链接悬停浮层的内容：截断 URL + 复制/打开按钮。
 */
function LinkHoverCard({
  visible,
  href,
  anchorEl,
  cardRef,
  onCardEnter,
  onCardLeave,
}: {
  visible: boolean;
  href: string;
  anchorEl: HTMLAnchorElement | null;
  cardRef: React.RefObject<HTMLDivElement | null>;
  onCardEnter: () => void;
  onCardLeave: () => void;
}) {
  const [pos, setPos] = useState({ top: 0, left: 0 });
  const [positioned, setPositioned] = useState(false);
  const slug = useWorkspaceSlug();
  const appOrigin = useAppOrigin();
  const { t } = useT("editor");

  // portal div 挂载（ref 回调）时就定位卡片。用 useEffect 会与 portal 渲染
  // 竞态——effect 跑时 div 可能还没进 DOM。
  const setCardRef = useCallback(
    (node: HTMLDivElement | null) => {
      (cardRef as React.MutableRefObject<HTMLDivElement | null>).current = node;
      if (!node || !anchorEl) {
        setPositioned(false);
        return;
      }
      computePosition(anchorEl, node, {
        placement: "bottom-start",
        strategy: "fixed",
        middleware: [offset(4), flip(), shift({ padding: 8 })],
      }).then(({ x, y }) => {
        setPos({ top: y, left: x });
        setPositioned(true);
      });
    },
    [anchorEl, cardRef],
  );

  // 隐藏时重置定位标记
  useEffect(() => {
    if (!visible) setPositioned(false);
  }, [visible]);

  if (!visible || !anchorEl) return null;

  const handleCopy = async (e: React.MouseEvent) => {
    e.stopPropagation();
    e.preventDefault();
    if (await copyText(href)) {
      toast.success(t(($) => $.link_hover.link_copied));
    } else {
      toast.error(t(($) => $.link_hover.copy_failed));
    }
  };

  const handleOpen = (e: React.MouseEvent) => {
    e.stopPropagation();
    e.preventDefault();
    openLink(href, slug, appOrigin);
  };

  return createPortal(
    <div
      ref={setCardRef}
      className="link-hover-card"
      style={{
        position: "fixed",
        top: pos.top,
        left: pos.left,
        zIndex: 50,
        display: positioned ? undefined : "none",
      }}
      onMouseEnter={onCardEnter}
      onMouseLeave={onCardLeave}
    >
      <span
        className="min-w-0 flex-1 truncate text-caption text-muted-foreground px-1"
        title={href}
      >
        {truncateUrl(href)}
      </span>
      <Button
        size="icon-xs"
        variant="ghost"
        className="text-muted-foreground"
        onClick={handleCopy}
        title={t(($) => $.link_hover.copy_link)}
      >
        <Copy className="size-3.5" />
      </Button>
      <Button
        size="icon-xs"
        variant="ghost"
        className="text-muted-foreground"
        onClick={handleOpen}
        title={t(($) => $.link_hover.open_link)}
      >
        <ExternalLink className="size-3.5" />
      </Button>
    </div>,
    document.body,
  );
}

/** LinkHoverCard 组件与 useLinkHover hook 一并导出。 */
export { useLinkHover, LinkHoverCard };
