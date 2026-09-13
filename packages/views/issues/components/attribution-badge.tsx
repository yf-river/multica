"use client";

// 本文件：AttributionBadge —— 展示一次代理运行要对谁负责（"代表 <成员> 执行"，
// MUL-4302 §9）。解析来源放进 tooltip；只有"命名的真人未必是真负责人"时才带
// 警示色调——即回退猜测（owner_fallback）。回填（backfill）属于历史性的、
// 事后的记录（非实时、不算合规级），但那不代表显示的名字是错的，因此不给警示
// 色调；其历史来源仍显示在 tooltip 与原始 `source` 字段里（MUL-4768）。
// 有三种形态（badge 徽章 / avatar 仅头像 / inline 内嵌名），无负责人可解析时
// 一律保持静默（MUL-4765）。

import type { TaskAttribution } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { ActorAvatar } from "@multica/ui/components/common/actor-avatar";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@multica/ui/components/ui/tooltip";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

/** 名字与姓氏的首字母，用于没有头像时作为头像兜底。 */
function initialsOf(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  const first = parts[0];
  if (!first) return "?";
  const last = parts[parts.length - 1];
  if (parts.length === 1 || !last) return first.slice(0, 2).toUpperCase();
  return (first.charAt(0) + last.charAt(0)).toUpperCase();
}

/**
 * AttributionBadge 渲染一次代理运行要对谁负责（MUL-4302 §9）：
 * "代表 <成员> 执行"的溯源展示，解析来源放在 tooltip 中；只有在"被点名的真人
 * 可能并非实际负责人"时才用警示色——即回退猜测（owner_fallback）。回填
 * （backfill）是历史性的、事后的记录（非实时、不算合规级），但那并不使显示的
 * 名字变错，因此不配警示色调；它的历史出处仍显示在 tooltip 与原始 `source`
 * 字段里（MUL-4768）。
 *
 * 三种形态，无法解析出负责人时全部保持静默（MUL-4765）：
 *  - `variant="badge"`（默认）：完整的"代表 <名字>"chip。没有负责人可对应时
 *    什么都不渲染，让一条无人认领的运行读起来就是普通状态，而非一条警告。
 *  - `variant="avatar"`：只给负责人的头像，名字 + 来源放在悬停 tooltip。
 *    紧凑到能塞进密集的任务行。没有负责人可对应时不渲染——纯头像的界面
 *    对一条未归属的运行本就没什么有意义的可展示。
 *  - `variant="inline"`：裸成员名（无边框，可通过 `hideAvatar` 去掉头像），
 *    来源放 tooltip——给那种"句子式"身份行（如对话头）用，标签由调用方提供。
 *    静默规则同上。
 *
 * 任务完全没有归属（老后端）时不渲染任何东西——调用方应对
 * `task.attribution` 做可选链。
 */
export function AttributionBadge({
  attribution,
  className,
  variant = "badge",
  hideAvatar = false,
}: {
  attribution?: TaskAttribution;
  className?: string;
  variant?: "badge" | "avatar" | "inline";
  /** 仅 inline 形态：不带头像地渲染名字，避免与旁边的主头像（对话头身份行）
   *  相互争抢视觉焦点。 */
  hideAvatar?: boolean;
}) {
  const { t } = useT("issues");
  if (!attribution) return null;

  // 人可读的解析来源文案，缺省回退到原始标签——服务端新加的来源
  // 能优雅降级而不是显示空白。
  let sourceLabel: string;
  switch (attribution.source) {
    case "direct_human":
      sourceLabel = t(($) => $.execution_log.attribution.source_direct_human);
      break;
    case "delegation":
      sourceLabel = t(($) => $.execution_log.attribution.source_delegation);
      break;
    case "comment_source":
      sourceLabel = t(($) => $.execution_log.attribution.source_comment_source);
      break;
    case "trigger_owner":
      sourceLabel = t(($) => $.execution_log.attribution.source_trigger_owner);
      break;
    case "rule_owner":
      sourceLabel = t(($) => $.execution_log.attribution.source_rule_owner);
      break;
    case "owner_fallback":
      sourceLabel = t(($) => $.execution_log.attribution.source_owner_fallback);
      break;
    case "backfill":
      sourceLabel = t(($) => $.execution_log.attribution.source_backfill);
      break;
    case "unattributed":
      sourceLabel = t(($) => $.execution_log.attribution.source_unattributed);
      break;
    default:
      sourceLabel = attribution.source;
  }

  // 后端的 `precise` 位是一枚"归属覆盖率"健康指标：owner_fallback、backfill
  // 与 unattributed 都会让它为 false。但覆盖率是运维指标，不是面向读者的信号。
  // "代表 <名字>"的读者唯一关心的是：这个被点名的真人会不会其实不是负责人——
  // 只有回退猜测才成立（owner_fallback：什么都没解析出来，于是回退到代理所属者）。
  // 回填是历史性的、事后的记录（非实时、不算合规级），但那并不让显示的名字变错，
  // 因此不值得警示色调；它的历史出处仍留在 tooltip 与原始 `source` 字段里
  //（MUL-4768）。于是警示色调在"非精确来源、且非回填"时点亮；保留
  // `precise === false` 这一底数，意味着未来出现新的退化来源仍会告警
  //（fail-safe），而不是被静默读成"确信无疑"。
  const uncertain =
    attribution.precise === false && attribution.source !== "backfill";
  const initiator = attribution.initiator;

  // Inline 形态：头像 + 裸名，无边框、无"代表"包装——标签由调用方提供
  //（如"由 XX 触发"），这里只给"人"。解析来源留在 tooltip。
  if (variant === "inline") {
    const initiatorInline = attribution.initiator;
    if (!initiatorInline) return null;
    const name = initiatorInline.name || t(($) => $.execution_log.attribution.someone);
    return (
      <Tooltip>
        <TooltipTrigger
          render={
            <span
              className={cn(
                "inline-flex min-w-0 items-center gap-1.5 text-caption",
                uncertain ? "text-warning" : "text-foreground",
                className
              )}
            >
              {!hideAvatar && (
                <ActorAvatar
                  name={name}
                  initials={initialsOf(name)}
                  avatarUrl={initiatorInline.avatar_url}
                  size="xs"
                  className="shrink-0"
                />
              )}
              <span className="min-w-0 truncate">{name}</span>
            </span>
          }
        />
        <TooltipContent>{sourceLabel}</TooltipContent>
      </Tooltip>
    );
  }

  // 仅头像形态：只给负责成员的脸，名字 + 来源放进悬停 tooltip。
  // 没有负责成员就无可展示。
  if (variant === "avatar") {
    if (!initiator) return null;
    const name = initiator.name || t(($) => $.execution_log.attribution.someone);
    return (
      <Tooltip>
        <TooltipTrigger
          render={
            <span
              className={cn(
                "inline-flex shrink-0",
                // 一圈细环标记"回退猜测"，让 owner-fallback 的脸
                // 绝不会被静默读成"确信已解析出的负责人"。
                uncertain && "rounded-full ring-1 ring-warning/60",
                className
              )}
            >
              <ActorAvatar
                name={name}
                initials={initialsOf(name)}
                avatarUrl={initiator.avatar_url}
                size="xs"
              />
            </span>
          }
        />
        <TooltipContent>
          <div className="flex flex-col">
            <span>
              {t(($) => $.execution_log.attribution.on_behalf_of, { name })}
            </span>
            <span
              className={cn(
                "text-micro",
                uncertain ? "text-warning" : "text-muted-foreground"
              )}
            >
              {sourceLabel}
            </span>
          </div>
        </TooltipContent>
      </Tooltip>
    );
  }

  // 没有解析出负责成员：宁可什么都不渲染，也不要一颗警告 chip。
  // 空的负责人是正常状态（例如无人认领的任务），没什么好标记的——
  // 因此 badge 形态保持静默，与上面的 avatar 形态一致（MUL-4765）。
  if (!initiator) return null;

  const name = initiator.name || t(($) => $.execution_log.attribution.someone);
  return (
    <Badge
      variant="outline"
      className={cn(
        "max-w-40 min-w-0 gap-1 font-normal",
        uncertain ? "text-warning" : "text-muted-foreground",
        className
      )}
      title={sourceLabel}
    >
      <ActorAvatar
        name={name}
        initials={initialsOf(name)}
        avatarUrl={initiator.avatar_url}
        size="xs"
        className="shrink-0"
      />
      <span className="min-w-0 truncate">
        {t(($) => $.execution_log.attribution.on_behalf_of, { name })}
      </span>
    </Badge>
  );
}
