"use client";

import { useEffect, useState } from "react";

const AGENT_ACTIVITY_FIRST_PAINT_DELAY_MS = 2_500;

export function useDeferredAgentActivityEnabled(forceEnabled: boolean, wsId: string | undefined): boolean {
  const [enabled, setEnabled] = useState(false);

  useEffect(() => {
    setEnabled(false);
    if (!wsId) return;
    if (forceEnabled) {
      setEnabled(true);
      return;
    }
    const timer = window.setTimeout(
      () => setEnabled(true),
      AGENT_ACTIVITY_FIRST_PAINT_DELAY_MS,
    );
    return () => window.clearTimeout(timer);
  }, [forceEnabled, wsId]);

  return enabled;
}
