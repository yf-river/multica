"use client";

import { Globe, Lock } from "lucide-react";
import type { AgentVisibility } from "@multica/core/types";
import { Tooltip, TooltipTrigger, TooltipContent } from "@multica/ui/components/ui/tooltip";
import { useT } from "../../i18n";

// 本文件提供一个只读的可见性徽章，用于展示 Agent 的可见范围（个人 / 工作区）。

/**
 * 只读的可见性徽章：让用户“看到”某个 Agent 的可见性（个人 / 工作区），
 * 但无法修改。用于替换详情页上非管理员用户看到的可交互 `<VisibilityPicker>`，
 * 也是悬浮卡片和列表行上的标准徽章。
 *
 * @param value 该 Agent 的可见性取值（private / workspace）
 * @param compact 是否紧凑模式——只显示图标、省略文字标签，适合列头已经标注了
 *                该字段含义的紧凑空间（如 Agent 表格）
 * @param className 额外附加到徽章上的样式类名
 */
export function VisibilityBadge({
  value,
  compact = false,
  className = "",
}: {
  value: AgentVisibility;
  compact?: boolean;
  className?: string;
}) {
  const { t } = useT("agents");
  const Icon = value === "private" ? Lock : Globe;
  const label = t(($) => $.visibility[value].label);
  const tooltip = t(($) => $.visibility[value].tooltip);

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span
            className={`inline-flex items-center gap-1 text-caption text-muted-foreground ${className}`}
            aria-label={tooltip}
          >
            <Icon className="h-3 w-3 shrink-0" />
            {!compact && <span className="truncate">{label}</span>}
          </span>
        }
      />
      <TooltipContent>{tooltip}</TooltipContent>
    </Tooltip>
  );
}
