"use client";

// Agent 在线/负载"呈现状态"的 React hooks：提供工作区级批量映射（推荐给列表/多行
// 场景）与单 Agent 详情（推荐给单 Agent 场景）。底层依赖 30s 心跳推进 unstable→
// offline 的衰变。
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

// 30s 心跳，镜像 useRuntimeHealth。呈现状态依赖墙钟时间只有一个原因：`unstable`
//（= RuntimeHealth.recently_lost）在没有新服务器数据的情况下会在第 5 分钟衰变成
// `offline`。没有心跳，这个过渡只会在下一次无关查询更新时渲染出来。
// 更早那个 2 分钟的"清除失败徽标"心跳在失败变为 sticky 后被移除；这次重新引入
// 心跳的动机不同。
const PRESENCE_TICK_MS = 30_000;

/** 每 30s +1 的计数器，作为 useMemo 的依赖以驱动重算。 */
function usePresenceTick(): number {
  const [tick, setTick] = useState(0);
  useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), PRESENCE_TICK_MS);
    return () => clearInterval(id);
  }, []);
  return tick;
}

/**
 * 以 `agent.id` 为 key 的工作区级呈现映射。**任何需要为多个 Agent 展示呈现状态
 * 的列表 / 卡片 / runtime 子视图的唯一入口。**
 *
 * 为什么存在（而不是每行调 `useAgentPresence`）：单 Agent hook 要订阅 3 个查询。
 * 30+ 行时就是一整片冗余 memo。这个批量 hook 全页只付一次代价；每行只是
 * `Map.get(id)`——O(1) 读取，没有额外订阅。
 *
 * 返回值：
 *   - `byAgent`：可直接读取的 Map。数据仍在加载时为空。
 *   - `loading`：三个输入查询都至少解析过一次之前为 true。调用方可在加载期渲染骨架。
 *
 * 单 Agent 消费方应继续用 `useAgentPresenceDetail`；本 hook 面向"已手握 Agent 列表"
 * 的界面。
 * @param wsId 工作区 id（为空时不查询）。
 * @returns 按 Agent id 索引的呈现详情映射 + 加载标记。
 */
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
    // 把出错的查询当作空数据处理，让映射照样构建——快照接口 404 不该让每一行的
    // 呈现状态都空着。
    const safeAgents = agents ?? (agentsErr ? [] : null);
    const safeRuntimes = runtimes ?? (runtimesErr ? [] : null);
    const safeSnapshot = snapshot ?? (snapshotErr ? [] : null);
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
    // 只在查询真正 pending 时算 "loading"——一旦 settle（成功或出错），就用手里
    // 有的数据渲染。这与单 Agent 版本行为一致：出错也别永远转圈。
    loading:
      (agentsPending && !agentsErr) ||
      (runtimesPending && !runtimesErr) ||
      (snapshotPending && !snapshotErr),
  };
}

/**
 * 单 Agent 呈现详情：可用性 + 最近任务状态 + 计数 +（失败时）失败原因与时间。
 * 只在底层查询还没解析时返回 "loading"——runtime 缺失是真实状态（offline），
 * 会解析成非 loading 的详情。
 *
 * 已手握 Agent 列表的界面（Agents 页、Runtime 详情）请优先用 `useWorkspacePresenceMap`，
 * 避免一整片冗余订阅。
 */
// 无法解析出真实 Agent（被删、已归档、或被过期数据引用）但还得在头像旁渲染点什么时
// 的合成兜底。给一颗灰点 + idle 最近任务——好过永远转圈的骨架。
const MISSING_AGENT_DETAIL: AgentPresenceDetail = {
  availability: "offline",
  workload: "idle",
  runningCount: 0,
  queuedCount: 0,
  capacity: 0,
};

/**
 * 订阅单个 Agent 的呈现详情。
 * @param wsId 工作区 id。
 * @param agentId 目标 Agent id（为空返回 "loading"）。
 * @param enabled 是否允许发起底层查询（默认 true）。
 * @returns 呈现详情；查询未解析时返回 "loading"；Agent 不存在时返回灰色兜底。
 */
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
  const tick = usePresenceTick();

  return useMemo<AgentPresenceDetail | "loading">(() => {
    if (!wsId || !agentId) return "loading";

    // 把查询错误当"无数据"而不是"仍在加载"。快照接口 404 / 5xx（例如后端还没
    // 上线新路由）曾让 UI 永远转圈；现在退化成空列表，圆点仍基于 runtime 健康渲染。
    const safeAgents = agents ?? (agentsErr ? [] : null);
    const safeRuntimes = runtimes ?? (runtimesErr ? [] : null);
    const safeSnapshot = snapshot ?? (snapshotErr ? [] : null);
    if (!safeAgents || !safeRuntimes || !safeSnapshot) return "loading";

    const agent = safeAgents.find((a) => a.id === agentId);
    // Agent 被引用但不在工作区活跃列表里（最常见：旧议题上的已归档负责人）。
    // 渲染灰色 offline 兜底，而不是在 "loading" 里循环。
    if (!agent) return MISSING_AGENT_DETAIL;
    // runtime 缺失是合法状态（offline）——传 null 交给 derive 处理。
    const runtime = safeRuntimes.find((r) => r.id === agent.runtime_id) ?? null;

    const tasks = safeSnapshot.filter((t) => t.agent_id === agentId);
    return deriveAgentPresenceDetail({ agent, runtime, tasks, now: Date.now() });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wsId, agentId, agents, runtimes, snapshot, agentsErr, runtimesErr, snapshotErr, tick]);
}
