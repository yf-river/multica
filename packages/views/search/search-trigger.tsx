"use client";

/**
 * search 模块：侧边栏里的搜索入口按钮。
 * 点击打开全局搜索命令面板，右侧附带「打开搜索」快捷键提示。
 */

import { Search } from "lucide-react";
import { SidebarMenuButton } from "@multica/ui/components/ui/sidebar";
import {
  useShortcut,
} from "@multica/core/shortcuts";
import { useSearchStore } from "./search-store";
import { useT } from "../i18n";
import { ShortcutKeycaps } from "../common/shortcut-keycaps";

/** 侧边栏搜索入口：点击后打开全局搜索面板。 */
export function SearchTrigger() {
  const { t } = useT("search");
  const shortcut = useShortcut("openSearch");
  return (
    <SidebarMenuButton
      className="text-muted-foreground"
      onClick={() => useSearchStore.getState().setOpen(true)}
    >
      <Search />
      <span>{t(($) => $.trigger.label)}</span>
      {shortcut ? (
        <ShortcutKeycaps shortcut={shortcut} decorative className="pointer-events-none ml-auto" />
      ) : null}
    </SidebarMenuButton>
  );
}
