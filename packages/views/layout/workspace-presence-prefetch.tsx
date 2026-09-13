"use client";

import { useEffect, useState } from "react";
import { useWorkspaceId } from "@multica/core";
import { useWorkspacePresencePrefetch } from "@multica/core/agents";

const PRESENCE_PREFETCH_DELAY_MS = 6_000;

/**
 * 布局模块：工作区在线状态（presence）的预取器。
 * 在工作区解析完成后延迟、空闲时预热在线状态数据，避免打开时卡顿。
 */
// 只挂载在已经用「工作区已解析」做门控的子树里（Web 端 DashboardLayout、
// 桌面端 WorkspaceRouteLayout）。useWorkspaceId 在未解析的工作区外调用会
// 抛错——那些布局的门控保证了这个组件永远不会遇到那种状态。
export function WorkspacePresencePrefetch() {
  const wsId = useWorkspaceId();
  const [enabled, setEnabled] = useState(false);

  useEffect(() => {
    setEnabled(false);
    let idleId: number | null = null;
    const delayId = globalThis.setTimeout(() => {
      if ("requestIdleCallback" in window) {
        idleId = window.requestIdleCallback(() => setEnabled(true), {
          timeout: 1_500,
        });
        return;
      }
      setEnabled(true);
    }, PRESENCE_PREFETCH_DELAY_MS);

    return () => {
      globalThis.clearTimeout(delayId);
      if (idleId !== null) window.cancelIdleCallback(idleId);
    };
  }, [wsId]);

  useWorkspacePresencePrefetch(enabled ? wsId : undefined);
  return null;
}
