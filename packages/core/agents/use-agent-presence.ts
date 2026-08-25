"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { agentListOptions } from "../workspace/queries";
import { runtimeListOptions } from "../runtimes/queries";
import { agentTaskSnapshotOptions } from "./queries";
import {
  buildPresenceMap,
  deriveAgentPresenceDetail,
} from "./derive-presence";
import type { AgentPresenceDetail } from "./types";

// Recompute when recently-lost runtime health can decay without new server data.
const PRESENCE_TICK_MS = 30_000;

function usePresenceTick(enabled = true): number {
  const [tick, setTick] = useState(0);
  useEffect(() => {
    if (!enabled) return;
    const id = setInterval(() => setTick((t) => t + 1), PRESENCE_TICK_MS);
    return () => clearInterval(id);
  }, [enabled]);
  return tick;
}

function settledList<T>(data: T[] | undefined, isError: boolean): T[] | null {
  return data ?? (isError ? [] : null);
}

export function useWorkspacePresenceMap(wsId: string | undefined): {
  byAgent: Map<string, AgentPresenceDetail>;
  loading: boolean;
} {
  const { data: agents, isPending: agentsPending, isError: agentsErr } = useQuery({
    ...agentListOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const { data: runtimes, isPending: runtimesPending, isError: runtimesErr } = useQuery({
    ...runtimeListOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const { data: snapshot, isPending: snapshotPending, isError: snapshotErr } = useQuery({
    ...agentTaskSnapshotOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  const tick = usePresenceTick();

  const byAgent = useMemo(() => {
    const safeAgents = settledList(agents, agentsErr);
    const safeRuntimes = settledList(runtimes, runtimesErr);
    const safeSnapshot = settledList(snapshot, snapshotErr);
    if (!safeAgents || !safeRuntimes || !safeSnapshot) {
      return new Map<string, AgentPresenceDetail>();
    }
    return buildPresenceMap({
      agents: safeAgents,
      runtimes: safeRuntimes,
      snapshot: safeSnapshot,
      now: Date.now(),
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agents, runtimes, snapshot, agentsErr, runtimesErr, snapshotErr, tick]);

  return {
    byAgent,
    loading:
      (agentsPending && !agentsErr) ||
      (runtimesPending && !runtimesErr) ||
      (snapshotPending && !snapshotErr),
  };
}

const MISSING_AGENT_DETAIL: AgentPresenceDetail = {
  availability: "offline",
  workload: "idle",
  runningCount: 0,
  queuedCount: 0,
};

export function useAgentPresenceDetail(
  wsId: string | undefined,
  agentId: string | undefined,
  enabled = true,
): AgentPresenceDetail | "loading" {
  const { data: agents, isError: agentsErr } = useQuery({
    ...agentListOptions(wsId ?? ""),
    enabled: enabled && !!wsId,
  });
  const { data: runtimes, isError: runtimesErr } = useQuery({
    ...runtimeListOptions(wsId ?? ""),
    enabled: enabled && !!wsId,
  });
  const { data: snapshot, isError: snapshotErr } = useQuery({
    ...agentTaskSnapshotOptions(wsId ?? ""),
    enabled: enabled && !!wsId,
  });
  const tick = usePresenceTick(enabled);

  return useMemo<AgentPresenceDetail | "loading">(() => {
    if (!enabled || !wsId || !agentId) return "loading";

    const safeAgents = settledList(agents, agentsErr);
    const safeRuntimes = settledList(runtimes, runtimesErr);
    const safeSnapshot = settledList(snapshot, snapshotErr);
    if (!safeAgents || !safeRuntimes || !safeSnapshot) return "loading";

    const agent = safeAgents.find((a) => a.id === agentId);
    if (!agent) return MISSING_AGENT_DETAIL;
    const runtime = safeRuntimes.find((r) => r.id === agent.runtime_id) ?? null;

    const tasks = safeSnapshot.filter((t) => t.agent_id === agentId);
    return deriveAgentPresenceDetail({ agent, runtime, tasks, now: Date.now() });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wsId, agentId, agents, runtimes, snapshot, agentsErr, runtimesErr, snapshotErr, tick]);
}
