// 详情页标题下的描述行：默认折叠为两行，仅当确实有内容被折叠时才出现展开/收起开关。
"use client";

import { useCallback, useLayoutEffect, useRef, useState } from "react";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";

/**
 * 详情页标题下方的描述行。默认夹为两行，只在确有内容被折叠待揭示时才显示
 * 展开开关。
 *
 * 若不折叠，长描述会把元信息行和标签条往下挤，内容起点取决于别人写了多长的
 * 文字。两行是智能体和技能头部视觉上原本所在的位置；开关让折叠变得安全，
 * 因为没它的话其余文字无处可去——这里就是详情页，就地展开是唯一不会把读者
 * 打发到别处去读一段话的做法。
 *
 * 开关刻意保持低调——悬停前是弱化的，与智能体动态页的"显示更多"控件一致。
 * 品牌色保留给主要操作和实时状态；本页的"保存"和"加入智能体"按钮才该吸引
 * 眼球，而不是一个文本展开器。
 * @param children 描述文本内容。
 * @param className 附加样式。
 */
export function ExpandableDescription({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  const { t } = useT("common");
  const [expanded, setExpanded] = useState(false);
  const [clamped, setClamped] = useState(false);
  const textRef = useRef<HTMLParagraphElement | null>(null);

  // 靠测量而不是数字符数猜：两行够不够取决于容器宽度和被渲染的字体。
  const measure = useCallback(() => {
    const element = textRef.current;
    if (!element) return;
    // 在折叠状态下读取，否则已展开的元素会报告无溢出，开关会在阅读途中消失。
    if (expanded) return;
    setClamped(element.scrollHeight > element.clientHeight + 1);
  }, [expanded]);

  useLayoutEffect(() => {
    measure();
    const element = textRef.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [measure, children]);

  return (
    <>
      <p
        ref={textRef}
        className={cn(
          "mt-1 max-w-2xl text-pretty text-body leading-6 text-muted-foreground",
          !expanded && "line-clamp-2",
          className,
        )}
      >
        {children}
      </p>
      {(clamped || expanded) && (
        <button
          type="button"
          onClick={() => setExpanded((value) => !value)}
          className="mt-0.5 rounded text-caption text-muted-foreground transition-colors hover:text-foreground focus-visible:text-foreground focus-visible:outline-none"
        >
          {expanded
            ? t(($) => $.expandable_description.collapse)
            : t(($) => $.expandable_description.expand)}
        </button>
      )}
    </>
  );
}
