"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { runtimeListOptions } from "./queries";
import { deriveRuntimeHealth } from "./derive-health";
import type { RuntimeHealth } from "./types";
import { useRuntimeNow } from "./use-runtime-now";

/**
 * Derived runtime health (online / recently_lost / offline / about_to_gc),
 * or "loading" while the runtime list is still resolving.
 *
 * Accepts wsId explicitly so the hook also works outside a resolved route.
 */
export function useRuntimeHealth(
  wsId: string | undefined,
  runtimeId: string | undefined,
): RuntimeHealth | "loading" {
  const { data: runtimes } = useQuery({
    ...runtimeListOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const now = useRuntimeNow();

  return useMemo<RuntimeHealth | "loading">(() => {
    if (!wsId || !runtimeId) return "loading";
    if (!runtimes) return "loading";
    const runtime = runtimes.find((r) => r.id === runtimeId);
    if (!runtime) return "loading";
    return deriveRuntimeHealth(runtime, now);
  }, [wsId, runtimeId, runtimes, now]);
}
