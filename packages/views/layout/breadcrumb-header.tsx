"use client";

import { Fragment, type ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { PageHeader } from "./page-header";
import { AppLink } from "../navigation";

/**
 * 布局模块：带面包屑的详情页头。
 * 用「祖先 › 祖先 › …」+ 当前页 + 右侧操作的形式统一各详情页的头部。
 */

/**
 * 一个祖先层级的面包屑段。总是可点击、跳转到该段容器的链接——面包屑表达
 * 的是一条包含链，所以每一段都必须能导航到某个地方。不可导航的 chrome
 * （骨架屏、「未知」状态）不属于这里，遇到就省略该段。
 */
export interface BreadcrumbSegment {
  href: string;
  /** 纯文本，或组合节点（如图标 + 标签）。 */
  label: ReactNode;
  /**
   * 覆盖默认的 `shrink-0`。传 `flex items-center gap-1 min-w-0 max-w-72`
   * 可得到一个可截断的段（例如很长的项目标题）。
   */
  className?: string;
}

/** 面包屑详情页头的属性。 */
interface BreadcrumbHeaderProps {
  /** 祖先链接，从左到右用 chevron 分隔符渲染。 */
  segments: BreadcrumbSegment[];
  /** 当前页——不可点击的叶子节点，样式/装饰由调用方控制。 */
  leaf: ReactNode;
  /** 右侧操作，包在一个 `shrink-0` 的 flex 行里；没有则省略。 */
  actions?: ReactNode;
  /** 最左侧插槽，替换移动端侧边栏触发器。见 `PageHeader`。 */
  leading?: ReactNode;
  className?: string;
}

/**
 * 统一的详情页头：`{祖先 › 祖先 › …} 叶子  [操作]`。
 *
 * 取代了各页面手写的、已漂移成四种风格的面包屑（工作区名根、`/` 分隔符、
 * 返回箭头、裸 div）。所有地方的认知模型一致：前导面包屑是这件事真正的
 * 容器，点击其中任意一段就导航上去。
 * @param segments 祖先面包屑段列表。
 * @param leaf 当前页叶子节点。
 * @param actions 右侧操作区。
 * @param leading 最左侧插槽。
 * @param className 附加样式类名。
 */
export function BreadcrumbHeader({ segments, leaf, actions, leading, className }: BreadcrumbHeaderProps) {
  return (
    <PageHeader leading={leading} className={cn("bg-background text-body", className)}>
      <div className="flex flex-1 items-center gap-1.5 min-w-0">
        {segments.map((segment) => (
          <Fragment key={segment.href}>
            <AppLink
              href={segment.href}
              className={cn(
                "text-muted-foreground hover:text-foreground transition-colors",
                segment.className ?? "shrink-0",
              )}
            >
              {segment.label}
            </AppLink>
            <ChevronRight className="h-3 w-3 text-faint-foreground shrink-0" />
          </Fragment>
        ))}
        {leaf}
      </div>
      {actions ? <div className="flex items-center gap-1 shrink-0">{actions}</div> : null}
    </PageHeader>
  );
}
