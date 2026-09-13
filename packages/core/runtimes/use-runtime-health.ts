"use client";

// 单台 runtime 的健康状态 hook：订阅 runtime 列表并从原始字段推导健康值。
import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { runtimeListOptions } from "./queries";
import { deriveRuntimeHealth } from "./derive-health";
import type { RuntimeHealth } from "./types";

// 每 30s 重渲染，让 recently_lost → offline（在 5 分钟标记处、没有新数据时发生）
// 之类的过渡反映到 UI 上。
const HEALTH_TICK_MS = 30_000;

/** 每 30s +1 的计数器，作为 useMemo 依赖以驱动健康重算。 */
function useHealthTick(): number {
  const [tick, setTick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), HEALTH_TICK_MS);
    return () => clearInterval(id);
  }, []);
  return tick;
}

/**
 * 派生的 runtime 健康值（online / recently_lost / offline / long_offline），
 * runtime 列表仍在解析时返回 "loading"。
 *
 * 把 wsId 作为参数接收，让 hook 能在 WorkspaceIdProvider 之外工作。
 * @param wsId 工作区 id。
 * @param runtimeId 目标 runtime id。
 * @returns 四态健康值；数据未就绪时为 "loading"。
 */
export function useRuntimeHealth(
  wsId: string | undefined,
  runtimeId: string | undefined,
): RuntimeHealth | "loading" {
  const { data: runtimes } = useQuery({
    ...runtimeListOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const tick = useHealthTick();

  return useMemo<RuntimeHealth | "loading">(() => {
    if (!wsId || !runtimeId) return "loading";
    if (!runtimes) return "loading";
    const runtime = runtimes.find((r) => r.id === runtimeId);
    if (!runtime) return "loading";
    return deriveRuntimeHealth(runtime, Date.now());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wsId, runtimeId, runtimes, tick]);
}
