"use client";

import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button } from "@multica/ui/components/ui/button";
import {
  resolvePostAuthDestination,
  useHasOnboarded,
} from "@multica/core/paths";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import { useNavigation } from "../navigation";
import { useLogout } from "../auth";
import { DragStrip } from "../platform";
import { useT } from "../i18n";

/**
 * workspace 模块：无访问权限页。
 * 当 URL 里的工作区 slug 解析不到当前用户能访问的工作区时渲染。刻意不区分
 * 「工作区不存在」与「存在但我不是成员」——无论提示哪种，都会让攻击者能够
 * 枚举工作区 slug。
 */
export function NoAccessPage() {
  const { t } = useT("workspace");
  const nav = useNavigation();
  const logout = useLogout();
  const hasOnboarded = useHasOnboarded();
  const { data: workspaces = [] } = useQuery(workspaceListOptions());

  // 清掉过期的 `last_workspace_slug` cookie。web 代理只看这个 cookie 就把 `/`
  // 重定向到 `/<lastSlug>/issues`（不做访问检查）。当 cookie 指向一个用户刚
  // 失去访问权的工作区时，任何对 `/` 的访问——手动导航、浏览器 Back 回 `/`、
  // 或重新加载页面——都会把用户弹回坏 slug，重新困在 NoAccessPage。恢复按钮
  // 不再经由 `/`（recover() 直接解析出具体目的地），但在这里清掉 cookie 能让
  // 其它那些 `/` 入口不再触发这个循环。
  // 在浏览器外是空操作（桌面端渲染器也有 document，无害）。
  useEffect(() => {
    if (typeof document === "undefined") return;
    document.cookie = "last_workspace_slug=; path=/; max-age=0; SameSite=Lax";
  }, []);

  // 用 replace 而非 push：失败的 `/<bad-slug>` URL 不能留在历史里，否则一次
  // 浏览器 Back 会把用户送回这个 NoAccessPage。
  const recover = () => {
    nav.replace(resolvePostAuthDestination(workspaces, hasOnboarded));
  };

  return (
    <div className="flex min-h-svh flex-col">
      <DragStrip />
      <div className="flex flex-1 flex-col items-center justify-center gap-6 px-6 pb-12 text-center">
        <div className="space-y-2">
          <h1 className="text-display-sm font-semibold tracking-tight">
            {t(($) => $.no_access.title)}
          </h1>
          <p className="max-w-md text-muted-foreground">
            {t(($) => $.no_access.description)}
          </p>
        </div>
        <div className="flex flex-col gap-2 sm:flex-row">
          <Button onClick={recover}>
            {t(($) => $.no_access.go_to_workspaces)}
          </Button>
          <Button variant="outline" onClick={logout}>
            {t(($) => $.no_access.sign_in_different)}
          </Button>
        </div>
      </div>
    </div>
  );
}
