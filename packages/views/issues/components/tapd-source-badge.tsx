"use client";

// 本文件：TAPDSourceBadge —— 问题"来源系统"徽标。
// 当 issue 是从 TAPD 或工蜂（Gongfeng）导入/关联的，就按其 metadata 里的
// source_provider 渲染对应来源的小图标（TAPD 用其 logo，工蜂用名称里的"蜂"字），
// 帮助用户一眼识别问题来自哪个外部系统。非这两个来源时返回 null 不渲染。

import type { Issue } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";

const TAPD_LOGO_URL = "https://static-fe.tapd.cn/img/TAPD_Logo2_mini.f5e386a7.svg";

/** 从 issue 的 metadata 里读出来源提供方：仅 "tapd"/"gongfeng" 两种；其他一律 null。 */
function sourceProvider(issue: Pick<Issue, "metadata">): "tapd" | "gongfeng" | null {
  const provider = String(issue.metadata?.source_provider ?? "").toLowerCase();
  return provider === "tapd" || provider === "gongfeng" ? provider : null;
}

/**
 * 来源徽标：`variant="card"` 用在卡片/详情（带底色圆角块）；
 * `variant="inline"` 用在列表行内（紧凑小圆点，工蜂场景显示一个"蜂"字）。
 * 无来源或来源不是 TAPD/工蜂时渲染 null。
 */
export function TAPDSourceBadge({
  issue,
  variant = "card",
  className,
}: {
  issue: Pick<Issue, "metadata">;
  variant?: "inline" | "card";
  className?: string;
}) {
  const provider = sourceProvider(issue);
  if (!provider) return null;
  const label = provider === "tapd" ? "TAPD" : "工蜂";
  const mark = provider === "gongfeng" ? label.slice(1) : "";

  if (variant === "inline") {
    return (
      <span
        aria-label={label}
        title={label}
        data-testid="tapd-source-badge"
        className={cn(
          "inline-flex h-3.5 w-3.5 shrink-0 items-center justify-center overflow-hidden rounded-full border-0 bg-transparent shadow-none",
          className,
        )}
      >
        {provider === "tapd" ? (
          <img src={TAPD_LOGO_URL} alt="" className="block h-full w-full object-cover" />
        ) : (
          <span className="text-micro font-bold text-primary">{mark}</span>
        )}
      </span>
    );
  }

  return (
    <span
      aria-label={label}
      title={label}
      data-testid="tapd-source-badge"
      className={cn(
        "inline-flex h-7 w-7 shrink-0 items-center justify-center overflow-hidden rounded-md bg-background p-0.5",
        className,
      )}
    >
      {provider === "tapd" ? (
        <img src={TAPD_LOGO_URL} alt="" className="h-full w-full object-contain" />
      ) : (
        <span className="text-caption font-bold text-primary">{mark}</span>
      )}
    </span>
  );
}
