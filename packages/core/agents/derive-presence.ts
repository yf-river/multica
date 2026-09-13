// 从原始服务器数据纯函数地推导 Agent 的用户可见"呈现状态"。
// 后端只存事实（有哪些任务、它们的状态、runtime 的 last_seen_at）；前端把它们翻译成
// 两个互相正交的维度：
//
//   1. AgentAvailability —— 只由 runtime 可达性推导。
//   2. Workload          —— 只由任务计数推导。
//
// 两者独立计算后组装成 AgentPresenceDetail。
// Workload 严格是"此刻手头有什么"——不含任何历史终态。过去的失败/完成放在详情页
// （Recent Work、failure_reason）和 Inbox 里。

import { deriveRuntimeHealth } from "../runtimes/derive-health";
import type { Agent, AgentRuntime, AgentTask } from "../types";
import type {
  AgentAvailability,
  AgentPresenceDetail,
  Workload,
} from "./types";

// AgentAvailability 镜像 RuntimeHealth 的可达性分桶，但把 `long_offline` 并进
// `offline`——从 Agent 可用性角度看两者都等于"不可达"；持续时长这个细节属于
// runtime 卡片，不属于 Agent 圆点。
/** 依据 runtime 推导可用性：无 runtime=offline；recently_lost=unstable；其余离线态并入 offline。 */
export function deriveAgentAvailability(
  runtime: AgentRuntime | null,
  now: number,
): AgentAvailability {
  if (!runtime) return "offline";
  const health = deriveRuntimeHealth(runtime, now);
  if (health === "online") return "online";
  if (health === "recently_lost") return "unstable";
  return "offline"; // offline | long_offline 在此折叠
}

// 原子化的负载推导：对 running/queued 计数做纯 3 分法分类。导出它，让 Runtime 级视图
// （它们已在各自索引里按 runtime 聚合计数）能复用同一套语汇，而不必从原始任务数组重推。
/** 按任务计数把负载分成 working / queued / idle 三态。 */
export function deriveWorkload(counts: {
  runningCount: number;
  queuedCount: number;
}): Workload {
  if (counts.runningCount > 0) return "working";
  if (counts.queuedCount > 0) return "queued";
  return "idle";
}

interface WorkloadDetail {
  workload: Workload;
  runningCount: number;
  queuedCount: number;
}

// 把任务列表聚合成 running/queued 计数，再交给 deriveWorkload 分类。调用方要先把
// 范围过滤好（per-agent 或 per-runtime）——这里不再过滤。
/** 聚合某范围（单个 Agent/单个 runtime）的任务，得出负载分类与细分的计数。 */
export function deriveWorkloadDetail(tasks: readonly AgentTask[]): WorkloadDetail {
  let runningCount = 0;
  let queuedCount = 0;
  for (const t of tasks) {
    if (t.status === "running") {
      runningCount += 1;
    } else if (
      t.status === "queued" ||
      t.status === "dispatched" ||
      // daemon 把这个任务停在一条繁忙的 local_directory 路径上。它仍算在 Agent 的
      // 盘子里（计入"queued"存在感），但还没进入运行阶段。
      t.status === "waiting_local_directory"
    ) {
      queuedCount += 1;
    }
    // 终态（completed / failed / cancelled）刻意忽略——负载是"此刻手头有什么"，
    // 不是历史。
  }
  return {
    workload: deriveWorkload({ runningCount, queuedCount }),
    runningCount,
    queuedCount,
  };
}

interface DerivePresenceInput {
  agent: Agent;
  runtime: AgentRuntime | null;
  // 只属于这个 Agent 的任务。调用方（buildPresenceMap、hooks）已按 agent_id
  // 过滤好——这里不重复检查。
  tasks: readonly AgentTask[];
  // 供 deriveAgentAvailability 分桶 runtime 健康用的墙钟毫秒。作为参数传入保持函数纯净。
  now: number;
}

/** 推导单个 Agent 的完整呈现详情（可用性 + 负载 + 计数 + 并发上限）。 */
export function deriveAgentPresenceDetail(input: DerivePresenceInput): AgentPresenceDetail {
  // 已归档优先于一切 runtime/任务信号——退役的 Agent 在任何地方都不能显示成活着的。
  // 在推导 runtime 健康或负载之前就短路返回，避免残留的 online runtime 行或过期快照
  // 任务把"Online / Working"泄漏给任何消费者。
  if (input.agent.archived_at) {
    return {
      availability: "archived",
      workload: "idle",
      runningCount: 0,
      queuedCount: 0,
      capacity: input.agent.max_concurrent_tasks,
    };
  }

  const availability = deriveAgentAvailability(input.runtime, input.now);
  const detail = deriveWorkloadDetail(input.tasks);

  return {
    availability,
    workload: detail.workload,
    runningCount: detail.runningCount,
    queuedCount: detail.queuedCount,
    capacity: input.agent.max_concurrent_tasks,
  };
}

// 工作区级的批量构建器。对工作区的 agents 单趟扫描，产出 Map<agentId, AgentPresenceDetail>，
// 每个列表/卡片/runtime 子页直接读，无需各自重推。
/** 一次遍历工作区全部 Agent，构建 agentId → 呈现详情的映射。 */
export function buildPresenceMap(args: {
  agents: readonly Agent[];
  runtimes: readonly AgentRuntime[];
  // 工作区 Agent 任务快照：每个活跃任务 + 每个 Agent 最近一次终态任务。
  // 直接来自 getAgentTaskSnapshot()——无需预过滤。终态行会被 deriveWorkloadDetail
  // 静默忽略（负载只看当前态）。
  snapshot: readonly AgentTask[];
  now: number;
}): Map<string, AgentPresenceDetail> {
  const out = new Map<string, AgentPresenceDetail>();
  const runtimesById = new Map<string, AgentRuntime>();
  for (const r of args.runtimes) runtimesById.set(r.id, r);

  // 按 agent_id 一次性分好组——O(N)——让逐 Agent 推导变成 O(1) 的任务扫描，
  // 而不是 O(N×M)。
  const tasksByAgent = new Map<string, AgentTask[]>();
  for (const t of args.snapshot) {
    const list = tasksByAgent.get(t.agent_id);
    if (list) list.push(t);
    else tasksByAgent.set(t.agent_id, [t]);
  }

  for (const agent of args.agents) {
    const runtime = runtimesById.get(agent.runtime_id) ?? null;
    const tasks = tasksByAgent.get(agent.id) ?? [];
    out.set(agent.id, deriveAgentPresenceDetail({ agent, runtime, tasks, now: args.now }));
  }
  return out;
}
