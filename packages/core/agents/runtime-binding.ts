// 判断 Agent 是否"绑定"了一个执行运行时：兼容新老服务端的信号差异。
import type { Agent } from "../types";

/**
 * 把新增的服务端信号与遗留的 runtime id 当作同一条不变式处理。
 * 新服务端暴露 `runtime_bound`；旧服务端只暴露非空 `runtime_id`。
 * 同时要求两个可用信号，也让部分响应若携带矛盾数据时"关闭即失败"。
 * @param agent Agent 的运行时绑定字段。
 * @returns 该 Agent 是否已可靠绑定到某个运行时。
 */
export function isAgentRuntimeBound(
  agent: Pick<Agent, "runtime_id" | "runtime_bound">,
): boolean {
  return (
    agent.runtime_bound !== false &&
    (agent.runtime_id ?? "").trim().length > 0
  );
}
