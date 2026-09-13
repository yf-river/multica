"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { pluginInstallationsOptions } from "@multica/core/plugins";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useFeatureEnabled } from "@multica/core/config";
import { PLUGINS_V1_FLAG } from "@multica/core/feature-flags";
import { useT } from "../i18n";
import { PluginSurfaceFrame } from "./plugin-surface-frame";

/**
 * plugins 模块：在一个 issue 上渲染所有已启用的 `issue_panel` 类型 surface。
 *
 * 被禁用的安装在这里过滤、同时也会被 Action API 拒绝——客户端的过滤只是
 * 展示，服务端校验才是控制。即使这个组件从未重渲染，一个留在过期标签页里
 * 的 surface 也会随之停止工作。
 */
export function PluginPanelSection({ issueId }: { issueId: string }) {
  const { t } = useT("issues");
  const platform = "web" as const;
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const pluginsEnabled = useFeatureEnabled(PLUGINS_V1_FLAG, false);

  const { data } = useQuery({ ...pluginInstallationsOptions(wsId), enabled: pluginsEnabled && wsId.length > 0 });

  const panels = useMemo(() => {
    const installations = data?.plugins ?? [];
    return installations
      .filter((installation) => installation.enabled === true)
      .flatMap((installation) =>
        installation.surfaces
          .filter((surface) => surface.type === "issue_panel")
          // 空的 platforms 列表表示「任何平台」；声明的列表是过滤器，而不是
          // 允许集合。
          .filter((surface) => (surface.platforms ?? []).length === 0 || (surface.platforms ?? []).includes(platform))
          .map((surface) => ({ installation, surface })),
      );
  }, [data, platform]);

  if (!pluginsEnabled || panels.length === 0) return null;

  return (
    <section className="space-y-3">
      <h3 className="px-0.5 text-caption font-medium text-muted-foreground">{t(($) => $.plugins.panels)}</h3>
      {panels.map(({ installation, surface }) => (
        <PluginSurfaceFrame
          key={`${installation.id}:${surface.key}`}
          wsId={wsId}
          installation={installation}
          surface={surface}
          issueId={issueId}
        />
      ))}
    </section>
  );
}
