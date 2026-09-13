"use client";

import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { pluginSurfaceLaunchOptions } from "@multica/core/plugins";
import type { PluginInstallation, PluginSurface } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { buildSurfaceFrameDocument, readThemeTokens } from "./surface-document";
import { createSurfaceBridge } from "./surface-bridge";

const DEFAULT_HEIGHT = 220;

/** 插件 surface frame 组件的属性。 */
interface PluginSurfaceFrameProps {
  wsId: string;
  installation: PluginInstallation;
  surface: PluginSurface;
  issueId?: string;
  className?: string;
}

/**
 * plugins 模块：挂载单个插件 surface。
 *
 * 这个元素是「受信任的」包裹层。它嵌套的插件 iframe 才是隔离边界，永远只带
 * `sandbox="allow-scripts"`、没有 `allow-same-origin`；详见 buildSurfaceFrameDocument。
 */
export function PluginSurfaceFrame({ wsId, installation, surface, issueId, className }: PluginSurfaceFrameProps) {
  const frameRef = useRef<HTMLIFrameElement>(null);
  const anchorRef = useRef<HTMLDivElement>(null);
  const [height, setHeight] = useState(DEFAULT_HEIGHT);
  const launchInstance = useId();

  // 每个挂载的 frame 都做一次独立的 launch。产物（artifact）不可变，但桥的
  // 一次性凭据刻意既不可缓存也不可共享。
  const { data: launch, isPending, isError } = useQuery(
    pluginSurfaceLaunchOptions(wsId, installation.id, surface.key, installation.package_version_id, launchInstance, issueId),
  );

  const surfaceDocument = useMemo(() => {
    if (!launch?.url || !launch.bridge_token) return null;
    try {
      return buildSurfaceFrameDocument({ url: launch.url, bridgeToken: launch.bridge_token });
    } catch {
      return null;
    }
  }, [launch?.url, launch?.bridge_token]);

  // 终态属于「一次渲染的文档 + 一个 issue」。对比 instance 能让旧的失败/跳转
  // 状态在替换成新 launch 后不再延续——即使新 iframe 运行正常。
  const surfaceInstance = useMemo(() => ({ issueId, surfaceDocument }), [issueId, surfaceDocument]);
  const [failedSurfaceInstance, setFailedSurfaceInstance] = useState<typeof surfaceInstance | null>(null);
  const [navigatedSurfaceInstance, setNavigatedSurfaceInstance] = useState<typeof surfaceInstance | null>(null);
  const failed = failedSurfaceInstance === surfaceInstance;
  const navigated = navigatedSurfaceInstance === surfaceInstance;

  // 每份渲染出来的文档对应一个桥。srcDoc 一变 iframe 就会重载——访客的握手
  // 也随之重来——所以旧桥到此结束：close() 是终态；把一个已关闭的桥在换文档
  // 后复用来连接，正是让面板永久空白的原因。
  const bridge = useMemo(
    () => createSurfaceBridge({
      installationId: installation.id,
      bridgeToken: launch?.bridge_token ?? "",
      issueId,
      onResize: setHeight,
    }),
    [installation.id, launch?.bridge_token, issueId],
  );

  // 监听器在 srcdoc 赋值之前就已挂好。这样访客优先的「一次性 port 转移」无需
  // 重试或可复用凭据即可做到无竞态。
  useEffect(() => {
    const frame = frameRef.current;
    if (!frame || !surfaceDocument) return () => bridge.close();
    const onMessage = (event: MessageEvent) => {
      const type = (event.data as { type?: string } | null)?.type;
      if (type !== "multica:plugin-surface-error" &&
          type !== "multica:plugin-surface-navigated" &&
          type !== "multica:plugin-surface-navigation-blocked") return;
      // 与桥相同的「同一窗口」规则：没有它，页面上任何 frame 都能点亮别的
      // 面板上的失败横幅。
      if (!frame.contentWindow || event.source !== frame.contentWindow) return;
      if (type === "multica:plugin-surface-error") setFailedSurfaceInstance(surfaceInstance);
      else setNavigatedSurfaceInstance(surfaceInstance);
    };
    // 终态事件与桥都在 srcdoc 赋值前挂好，所以即使插件首行就失败或跳转，
    // 也快不过宿主。
    window.addEventListener("message", onMessage);
    bridge.connect(frame, readThemeTokens(anchorRef.current));
    frame.srcdoc = surfaceDocument;
    return () => {
      window.removeEventListener("message", onMessage);
      bridge.close();
      frame.removeAttribute("srcdoc");
    };
  }, [bridge, surfaceDocument, surfaceInstance]);

  useEffect(() => {
    if (navigated) bridge.close();
  }, [navigated, bridge]);

  if (navigated) {
    return (
      <div ref={anchorRef} className={cn("rounded-lg border border-surface-border px-4 py-3 text-caption text-muted-foreground", className)}>
        <PluginSurfaceNotice installation={installation} kind="navigated" />
      </div>
    );
  }

  if (!surfaceDocument) {
    return (
      <div ref={anchorRef} className={cn("rounded-lg border border-surface-border px-4 py-3 text-caption text-muted-foreground", className)}>
        {/* 三种状态刻意共用这一个框：仍在加载、请求失败、以及已装版本没有为
            该 surface 提供代码。三者都表示「暂时没有可渲染的东西」；只有最后
            一种会持续，它们都不该显示一个读者无从操作的空 frame。 */}
        <PluginSurfaceNotice installation={installation} kind={isPending && !isError ? "loading" : "unavailable"} />
      </div>
    );
  }

  return (
    <div ref={anchorRef} className={cn("overflow-hidden rounded-lg border border-surface-border", className)}>
      {failed ? (
        <div className="px-4 py-3 text-caption text-muted-foreground">
          <PluginSurfaceNotice installation={installation} kind="failed" />
        </div>
      ) : null}
      <iframe
        // 同时以 issue 为 key：issueId 变化时会新建桥，但文档若不变就不会
        // 重载，访客应答完也就不再发声——新桥会永远等不到握手。
        key={`${installation.id}:${surface.key}:${issueId ?? ""}`}
        ref={frameRef}
        title={`${installation.name} — ${surface.name}`}
        // 这是宿主编写的外层包裹，所以同源是有意为之；它嵌套的插件 iframe
        // 仍然不透明、不持有任何应用凭据。
        sandbox="allow-scripts allow-same-origin"
        className="w-full border-0 bg-transparent"
        style={{ height }}
      />
    </div>
  );
}

/** 按类型渲染 surface 的说明文案（加载/失败/不可用/发生了跳转）。 */
function PluginSurfaceNotice({
  installation,
  kind,
}: {
  installation: PluginInstallation;
  kind: "unavailable" | "failed" | "loading" | "navigated";
}) {
  const { t } = useT("issues");
  if (kind === "failed") return <>{t(($) => $.plugins.surface_failed, { name: installation.name })}</>;
  if (kind === "loading") return <>{t(($) => $.plugins.surface_loading, { name: installation.name })}</>;
  if (kind === "navigated") return <>{t(($) => $.plugins.surface_navigated, { name: installation.name })}</>;
  return <>{t(($) => $.plugins.surface_unavailable, { name: installation.name })}</>;
}
