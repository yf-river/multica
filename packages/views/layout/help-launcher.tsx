"use client";

import {
  ArrowUpRight,
  BookOpen,
  CircleHelp,
  History,
  MessageCircle,
} from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { useModalStore } from "@multica/core/modals";
import { useConfigStore } from "@multica/core/config";
import { DISCORD_URL, DiscordIcon } from "./discord";
import { useT } from "../i18n";

/**
 * 布局模块：帮助菜单入口。
 * 侧边栏里的问号按钮，点击弹出包含文档、更新日志、Discord、反馈等入口的下拉菜单。
 */

const DOCS_URL = "https://multica.ai/docs";
const CHANGELOG_URL = "https://multica.ai/changelog";
/**
 * 帮助菜单启动器组件：渲染问号按钮和它的下拉菜单。
 * 菜单里包含文档、更新日志、Discord 社区、反馈等外链或动作。
 */
export function HelpLauncher() {
  const { t } = useT("layout");
  const serverVersion = useConfigStore((state) => state.serverVersion);
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label={t(($) => $.help.trigger)}
        title={t(($) => $.help.trigger)}
        className="inline-flex size-7 items-center justify-center rounded-full text-muted-foreground transition-colors cursor-pointer hover:bg-accent hover:text-foreground data-popup-open:bg-accent data-popup-open:text-foreground"
      >
        <CircleHelp className="size-4" />
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="end"
        side="top"
        sideOffset={8}
        className="min-w-40 max-w-56"
      >
        <DropdownMenuItem
          render={
            <a href={DOCS_URL} target="_blank" rel="noopener noreferrer" />
          }
        >
          <BookOpen className="h-3.5 w-3.5" />
          {t(($) => $.help.docs)}
          <ArrowUpRight className="size-3 translate-y-px text-faint-foreground" />
        </DropdownMenuItem>
        <DropdownMenuItem
          render={
            <a
              href={CHANGELOG_URL}
              target="_blank"
              rel="noopener noreferrer"
            />
          }
        >
          <History className="h-3.5 w-3.5" />
          {t(($) => $.help.changelog)}
          <ArrowUpRight className="size-3 translate-y-px text-faint-foreground" />
        </DropdownMenuItem>
        <DropdownMenuItem
          render={
            <a href={DISCORD_URL} target="_blank" rel="noopener noreferrer" />
          }
        >
          <DiscordIcon className="h-3.5 w-3.5" />
          {t(($) => $.help.discord)}
          <ArrowUpRight className="size-3 translate-y-px text-faint-foreground" />
        </DropdownMenuItem>
        <DropdownMenuItem
          onClick={() => useModalStore.getState().open("feedback")}
        >
          <MessageCircle className="h-3.5 w-3.5" />
          {t(($) => $.help.feedback)}
        </DropdownMenuItem>
        {serverVersion && (
          <>
            <DropdownMenuSeparator />
            {/* DropdownMenuLabel 底层是 Base UI 的 Menu.GroupLabel，它会读取
                Menu.Group 上下文，没有 Group 祖先时直接抛错。所以它必须始终
                包在 DropdownMenuGroup 里——否则帮助菜单一打开就崩掉整个应用
                （侧边栏上方没有错误边界兜底）。 */}
            <DropdownMenuGroup>
              <DropdownMenuLabel className="font-normal break-words">
                {t(($) => $.help.server_version, { version: serverVersion })}
              </DropdownMenuLabel>
            </DropdownMenuGroup>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
