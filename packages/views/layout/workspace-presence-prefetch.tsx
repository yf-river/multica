"use client";

import { useEffect, useState } from "react";
import { useWorkspaceId } from "@multica/core";
import { useWorkspacePresencePrefetch } from "@multica/core/agents";

const PRESENCE_PREFETCH_DELAY_MS = 6_000;

// Mount once inside any subtree that's already gated on "workspace resolved"
// (DashboardLayout on web, WorkspaceRouteLayout on desktop). useWorkspaceId
// throws when called outside a resolved workspace — the gating in those
// layouts guarantees this component never sees that state.
export function WorkspacePresencePrefetch() {
  const wsId = useWorkspaceId();
  const [enabled, setEnabled] = useState(false);

  useEffect(() => {
    setEnabled(false);
    if (typeof window === "undefined") return;

    let idleId: number | null = null;
    const delayId = globalThis.setTimeout(() => {
      if ("requestIdleCallback" in window) {
        idleId = window.requestIdleCallback(() => setEnabled(true), {
          timeout: 1500,
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
