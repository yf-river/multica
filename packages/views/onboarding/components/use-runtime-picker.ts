"use client";

// 引导第 3 步（step-platform-fork）的运行时数据层 Hook：负责运行时列表的轮询
// 拉取、监听新运行时注册事件触发即时刷新，并自动选中一个运行时。
import { useCallback, useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useWSEvent } from "@multica/core/realtime";
import {
  runtimeKeys,
  runtimeListOptions,
} from "@multica/core/runtimes/queries";
import type { AgentRuntime } from "@multica/core/types";

/**
 * 第 3 步「选运行时」的运行时数据逻辑：
 *
 *   - 列表为空时每 2 秒轮询一次，这样用户一装好运行时，界面立刻就能翻转为「已找到」；
 *   - 收到 `daemon:register` 的 WebSocket 事件时立即重新拉取——在线用户不用等下一次轮询；
 *   - 自动选中第一个「在线」的运行时，没有在线的就选第一个。只在用户还没手动选过时
 *     生效，所以用户手选的结果不会被后续刷新覆盖。
 *
 * @param wsId 工作区 id
 * @param wsSlug 工作区 slug（可选）
 * @returns 运行时列表、当前选中项与 id、设置选中项的 setter、是否有可用运行时
 */
export function useRuntimePicker(wsId: string, wsSlug?: string): {
  runtimes: AgentRuntime[];
  selected: AgentRuntime | null;
  selectedId: string | null;
  setSelectedId: (id: string) => void;
  hasRuntimes: boolean;
} {
  const qc = useQueryClient();

  const { data: runtimes = [] } = useQuery({
    ...runtimeListOptions(wsId, "me", wsSlug),
    refetchInterval: (q) => (q.state.data?.length ? false : 2000),
  });

  const handleDaemonEvent = useCallback(() => {
    qc.invalidateQueries({ queryKey: runtimeKeys.all(wsId) });
  }, [qc, wsId]);
  useWSEvent("daemon:register", handleDaemonEvent);

  const [selectedId, setSelectedId] = useState<string | null>(null);

  useEffect(() => {
    if (selectedId) return;
    const preferred =
      runtimes.find((r) => r.status === "online") ?? runtimes[0];
    if (preferred) setSelectedId(preferred.id);
  }, [runtimes, selectedId]);

  const selected = runtimes.find((r) => r.id === selectedId) ?? null;

  return {
    runtimes,
    selected,
    selectedId,
    setSelectedId,
    hasRuntimes: runtimes.length > 0,
  };
}
