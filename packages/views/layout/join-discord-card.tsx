"use client";

import { X } from "lucide-react";
import { useAuthStore } from "@multica/core/auth";
import { DISCORD_URL, DiscordIcon } from "./discord";
import { useDiscordCardDismissed } from "./use-discord-card-dismissed";
import { useT } from "../i18n";

/**
 * 布局模块：可关闭的「加入 Discord」入口。
 * 与帮助菜单共享侧边栏底部区域，关闭后对当前用户/当前浏览器保持隐藏。
 *
 * 有意做成侧边栏行而不是带边框的卡片：它对齐 SidebarMenuButton 的尺寸
 * （h-8 / rounded-md / gap-2 / text-body），不带常驻边框或底色。可关闭的
 * 推广是侧边栏里优先级最低的项，所以不能画得比它上面的导航更重。
 *
 * 与帮助菜单里的外链不同，这里不放外链箭头：与帮助触发器共享 224px 的
 * 条带后留给标签的只有 128px，箭头加关闭按钮在最宽的语言（英文，109px）
 * 下会一起溢出。关闭能力是面向用户的功能，箭头只是提示——Discord 标志
 * 已经表明了去向，所以关闭按钮优先。`flex-1 min-w-0` 让标签截断而不是
 * 把触发器挤出去。
 *
 * 这 128px 是默认 256px 侧边栏下的预算，而侧边栏可被用户缩到 200px——
 * 每拖 1px 标签就少 1px。所以只在 256px 下刚好放得下的标题，对任何把
 * 侧边栏收窄的人仍会截断；每种语言的标题都要舒适地低于预算，而不只是
 * 勉强低于它（MUL-5704）。
 */
export function JoinDiscordCard() {
  const { t } = useT("layout");
  const userId = useAuthStore((s) => s.user?.id);
  const [dismissed, dismiss] = useDiscordCardDismissed(userId);

  if (dismissed) return null;

  return (
    <div className="group/discord relative min-w-0 flex-1">
      <a
        href={DISCORD_URL}
        target="_blank"
        rel="noopener noreferrer"
        className="flex h-8 items-center gap-2 rounded-md px-2 pr-8 text-body text-muted-foreground ring-sidebar-ring outline-hidden transition-colors hover:bg-sidebar-accent/70 hover:text-sidebar-accent-foreground focus-visible:ring-2"
      >
        <DiscordIcon className="size-4 shrink-0" />
        <span className="truncate">
          {t(($) => $.sidebar.discord_card.title)}
        </span>
      </a>
      {/* 悬停/聚焦时才显示，让静止的行保持安静。粗指针没有 hover，
          所以那里让它常显。 */}
      <button
        type="button"
        aria-label={t(($) => $.sidebar.discord_card.dismiss)}
        onClick={dismiss}
        className="absolute inset-y-0 right-1 my-auto flex size-6 cursor-pointer items-center justify-center rounded-sm text-muted-foreground opacity-0 ring-sidebar-ring transition-opacity hover:bg-sidebar-accent hover:text-foreground focus-visible:opacity-100 focus-visible:ring-2 group-hover/discord:opacity-100 [@media(hover:none)]:opacity-100"
      >
        <X className="size-3.5" />
      </button>
    </div>
  );
}
