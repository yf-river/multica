"use client";

import type { ComponentProps, ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@multica/ui/components/ui/empty";
import { cn } from "@multica/ui/lib/utils";
import { PageHeader } from "./page-header";

/**
 * 布局模块：集合页共用的页头、页头操作按钮和居中状态视图。
 * 为 issues、项目、成员等列表页提供一致的头部与空/错误/未找到状态。
 */

/** 集合页头部组件的属性。 */
interface CollectionPageHeaderProps {
  icon: LucideIcon;
  title: ReactNode;
  count?: number;
  description?: ReactNode;
  learnMore?: {
    href: string;
    label: ReactNode;
  };
  actions?: ReactNode;
  className?: string;
}

/**
 * 仪表盘集合页共用的页头：左侧是实体图标、标题、可选数量与说明文字，
 * 右侧是页面级操作。
 * @param icon 实体图标组件。
 * @param title 标题。
 * @param count 可选的数量徽标，大于 0 才显示。
 * @param description 可选说明文字。
 * @param learnMore 可选的「了解更多」外链。
 * @param actions 右侧操作区。
 * @param className 附加样式类名。
 */
export function CollectionPageHeader({
  icon: Icon,
  title,
  count,
  description,
  learnMore,
  actions,
  className,
}: CollectionPageHeaderProps) {
  return (
    <PageHeader className={className}>
      <div className="flex min-w-0 flex-1 items-center gap-2">
        <Icon
          aria-hidden="true"
          className="size-4 shrink-0 text-muted-foreground"
        />
        <h1 className="truncate text-body font-medium">{title}</h1>
        {typeof count === "number" && count > 0 ? (
          <span className="shrink-0 font-mono text-caption tabular-nums text-muted-foreground">
            {count}
          </span>
        ) : null}
        {description ? (
          <p className="ml-2 hidden min-w-0 truncate text-caption text-muted-foreground md:block">
            {description}
            {learnMore ? (
              <>
                {" "}
                <a
                  href={learnMore.href}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="underline decoration-muted-foreground/30 underline-offset-4 transition-colors hover:text-foreground"
                >
                  {learnMore.label}
                </a>
              </>
            ) : null}
          </p>
        ) : null}
      </div>
      {actions ? (
        <div className="flex shrink-0 items-center justify-end gap-2">
          {actions}
        </div>
      ) : null}
    </PageHeader>
  );
}

/** 集合页头操作按钮的属性。 */
interface CollectionPageHeaderActionProps
  extends Omit<ComponentProps<typeof Button>, "children"> {
  icon: LucideIcon;
  label: string;
}

/**
 * 响应式的集合页操作按钮：md 以下只显示图标，md 及以上显示图标加文字。
 * @param icon 图标组件。
 * @param label 按钮文字，也用作无障碍标签的兜底。
 */
export function CollectionPageHeaderAction({
  icon: Icon,
  label,
  className,
  type = "button",
  size = "sm",
  variant = "outline",
  ...props
}: CollectionPageHeaderActionProps) {
  const accessibleLabel = props["aria-label"] ?? label;

  return (
    <Button
      type={type}
      size={size}
      variant={variant}
      className={cn("h-8 w-8 gap-1 px-0 md:w-auto md:px-2.5", className)}
      aria-label={accessibleLabel}
      {...props}
    >
      <Icon aria-hidden="true" className="size-3.5" />
      <span className="hidden md:inline">{label}</span>
    </Button>
  );
}

/** 状态视图的色调：普通、危险、警告。 */
type PageStateTone = "muted" | "destructive" | "warning";

/** 集合页居中状态视图的属性。 */
interface CollectionPageStateProps {
  icon: LucideIcon;
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  tone?: PageStateTone;
  role?: "alert" | "status";
  className?: string;
}

const stateToneClass: Record<PageStateTone, string> = {
  muted: "text-muted-foreground",
  destructive: "text-destructive",
  warning: "text-warning",
};

/**
 * 集合页共用的居中状态视图，用于空态、错误态和未找到态。
 * @param icon 状态图标。
 * @param title 状态标题。
 * @param description 可选说明。
 * @param actions 可选操作按钮。
 * @param tone 色调，默认 "muted"。
 * @param role 无障碍角色，如 "alert" 或 "status"。
 * @param className 附加样式类名。
 */
export function CollectionPageState({
  icon: Icon,
  title,
  description,
  actions,
  tone = "muted",
  role,
  className,
}: CollectionPageStateProps) {
  return (
    <Empty
      role={role}
      className={cn("rounded-none border-0 px-6 py-16", className)}
    >
      <EmptyHeader>
        <EmptyMedia
          variant="icon"
          className={cn(
            "size-12 rounded-full [&_svg]:size-6",
            stateToneClass[tone],
          )}
        >
          <Icon aria-hidden="true" />
        </EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        {description ? (
          <EmptyDescription className="max-w-md">{description}</EmptyDescription>
        ) : null}
      </EmptyHeader>
      {actions ? (
        <EmptyContent className="mt-1 flex-row justify-center">
          {actions}
        </EmptyContent>
      ) : null}
    </Empty>
  );
}
