"use client";

import { useEffect, useState } from "react";
import { api } from "@multica/core/api";
import { createLogger } from "@multica/core/logger";
import type { Agent, AgentRuntime } from "@multica/core/types/agent";

const logger = createLogger("task-transcript.metadata");

interface IdentifiedValue<T> {
  id: string;
  value: T;
}

export function useTranscriptMetadata(
  open: boolean,
  agentId?: string | null,
  runtimeId?: string | null,
): { agentInfo: Agent | null; runtimeInfo: AgentRuntime | null } {
  const [agentState, setAgentState] = useState<IdentifiedValue<Agent> | null>(null);
  const [runtimeState, setRuntimeState] = useState<IdentifiedValue<AgentRuntime> | null>(null);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;

    if (agentId) {
      void api
        .getAgent(agentId)
        .then((agent) => {
          if (!cancelled) setAgentState({ id: agentId, value: agent });
        })
        .catch((error: unknown) => {
          if (!cancelled) {
            logger.warn("failed to load transcript agent metadata", { agentId, error });
          }
        });
    }

    if (runtimeId) {
      void api
        .listRuntimes()
        .then((runtimes) => {
          if (cancelled) return;
          const runtime = runtimes.find((item) => item.id === runtimeId);
          if (runtime) setRuntimeState({ id: runtimeId, value: runtime });
        })
        .catch((error: unknown) => {
          if (!cancelled) {
            logger.warn("failed to load transcript runtime metadata", { runtimeId, error });
          }
        });
    }

    return () => {
      cancelled = true;
    };
  }, [open, agentId, runtimeId]);

  return {
    agentInfo: agentState && agentState.id === agentId ? agentState.value : null,
    runtimeInfo: runtimeState && runtimeState.id === runtimeId ? runtimeState.value : null,
  };
}
