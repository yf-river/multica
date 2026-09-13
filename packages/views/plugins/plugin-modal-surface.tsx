"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Plug } from "lucide-react";
import { useFeatureEnabled } from "@multica/core/config";
import { PLUGINS_V1_FLAG } from "@multica/core/feature-flags";
import { useCurrentWorkspace } from "@multica/core/paths";
import { pluginInstallationsOptions } from "@multica/core/plugins";
import type { PluginInstallation, PluginSurface } from "@multica/core/types";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { PluginSurfaceFrame } from "./plugin-surface-frame";

/**
 * plugins 模块：`modal`（弹窗）类型 surface 的挂载点与菜单入口。
 *
 * modal 与 panel 一样是托管的、不透明的内层 iframe——CSP 由授予的 `net:`
 * 作用域推导——差别只在它出现在哪里。它绝不是插件用来打断别人的手段：
 * 它只会因为有人从 issue 菜单里挑了它而打开，永远不会由插件自己主动弹出。
 */

/** 一个可打开的 modal surface：安装实例 + 该实例里的 modal surface。 */
export interface PluginModalTarget {
  installation: PluginInstallation;
  surface: PluginSurface;
}

/** 生成 modal 目标的稳定 key。 */
export function pluginModalKey(target: PluginModalTarget): string {
  return `${target.installation.id}:${target.surface.key}`;
}

/**
 * 收集所有已启用安装里的 modal surface。
 *
 * 单独导出是为了它自己的测试：这个过滤是产品规则，应该只在一个地方维护，
 * 而不是由每个提供入口的菜单各推导一遍。
 */
export function collectModalSurfaces(installations: readonly PluginInstallation[]): PluginModalTarget[] {
  const targets: PluginModalTarget[] = [];
  for (const installation of installations) {
    // 显式 === true：后端一旦不再发送该字段，必须读作「关闭」，而不是「足够
    // 真就打开一个第三方 UI」。
    if (installation.enabled !== true) continue;
    for (const surface of installation.surfaces ?? []) {
      if (surface.type === "modal") targets.push({ installation, surface });
    }
  }
  return targets;
}

/** 返回当前工作区所有已启用的 modal surface（未启用插件时为空数组）。 */
export function usePluginModalSurfaces(): PluginModalTarget[] {
  const pluginsEnabled = useFeatureEnabled(PLUGINS_V1_FLAG, false);
  const wsId = useCurrentWorkspace()?.id ?? "";
  const { data } = useQuery({
    ...pluginInstallationsOptions(wsId),
    enabled: pluginsEnabled && wsId.length > 0,
  });

  return useMemo(
    () => (pluginsEnabled ? collectModalSurfaces(data?.plugins ?? []) : []),
    [data, pluginsEnabled],
  );
}

/** 插件 modal 弹窗组件的 props。 */
interface PluginModalSurfaceProps {
  target: PluginModalTarget | null;
  issueId?: string;
  onOpenChange: (open: boolean) => void;
}

/** 用 Dialog 包住一个插件 modal surface 并挂载其 frame。 */
export function PluginModalSurface({ target, issueId, onOpenChange }: PluginModalSurfaceProps) {
  // 与上面 usePluginModalSurfaces 同源：modal 只会从 issue 菜单打开，而菜单
  // 处于工作区路由之内。
  const workspace = useCurrentWorkspace();
  if (!target) return null;
  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Plug className="size-4 text-muted-foreground" />
            {target.surface.name}
          </DialogTitle>
          {/* 永远展示插件名。面对一弹窗第三方 UI，用户理应不靠猜样式就知道
              这是谁的。 */}
          <DialogDescription>{target.installation.name}</DialogDescription>
        </DialogHeader>
        <PluginSurfaceFrame
          // 同时以 issue 为 key：frame 会把 issue 带进它的 context 调用，而
          // 文档不变的话不会为一个不同的 issue 重载。
          key={`${pluginModalKey(target)}:${issueId ?? ""}`}
          wsId={workspace?.id ?? ""}
          installation={target.installation}
          surface={target.surface}
          issueId={issueId}
        />
      </DialogContent>
    </Dialog>
  );
}

/** 打开 modal 的菜单项，形状与 hook 菜单项一致。 */
export function PluginModalMenuItems({
  issueId,
  Item,
}: {
  issueId: string;
  Item: React.ComponentType<{ onClick?: () => void; children?: React.ReactNode }>;
}) {
  const targets = usePluginModalSurfaces();
  const [open, setOpen] = useState<PluginModalTarget | null>(null);

  if (targets.length === 0) return null;

  return (
    <>
      {targets.map((target) => (
        <Item key={pluginModalKey(target)} onClick={() => setOpen(target)}>
          <Plug className="h-3.5 w-3.5" />
          <span className="truncate">{target.surface.name}</span>
          <span className="ml-auto truncate pl-3 text-caption text-muted-foreground">
            {target.installation.name}
          </span>
        </Item>
      ))}
      <PluginModalSurface target={open} issueId={issueId} onOpenChange={(next) => !next && setOpen(null)} />
    </>
  );
}
