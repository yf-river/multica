// 项目图标组件：优先渲染项目自己设置的 Emoji 图标，没有时用文件夹 Emoji 兜底。

import type { Project } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";

/** 项目图标的尺寸档位：sm / md / lg。 */
export type ProjectIconSize = "sm" | "md" | "lg";

/** 项目图标组件的入参。 */
export interface ProjectIconProps {
  /** 目标项目（只需它的 icon 字段）；缺省渲染兜底文件夹图标 */
  project?: Pick<Project, "icon"> | null;
  /** 图标尺寸 */
  size?: ProjectIconSize;
  className?: string;
}

// 每种尺寸对应的字号/盒子样式。
const SIZE_CLASS: Record<ProjectIconSize, string> = {
  sm: "size-3.5 text-caption leading-none",
  md: "size-4 text-body leading-none",
  lg: "size-6 text-display-sm leading-none",
};

/**
 * 渲染项目图标（项目自定义 Emoji，缺省为 📁）。
 *
 * @param project 目标项目；为空时显示兜底文件夹图标
 * @param size 尺寸档位
 */
export function ProjectIcon({ project, size = "sm", className }: ProjectIconProps) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        "inline-flex shrink-0 items-center justify-center",
        SIZE_CLASS[size],
        className,
      )}
    >
      {project?.icon || "📁"}
    </span>
  );
}
