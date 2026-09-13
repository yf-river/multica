// 本文件把后端的任务失败原因（`agent_task_queue.failure_reason`）归类成少数几个
// 展示用大类，供仪表盘的堆叠图、图例和明细列表使用。
//
// 后端原因分类（server/pkg/taskfailure）有 22 种，对堆叠图或可扫读的明细列表
// 来说太细。这七个大类才是运维真正会去处理的粒度：认证激增意味着「去重新授权」、
// 限流激增意味着「退避或提高上限」、运行时激增意味着「某个 daemon 挂了」。
// 原始原因仍然在接口字段里、也仍展示在明细中，信息没有丢失。
//
// 下面数组的顺序就是所有地方的渲染顺序（堆叠段、图例、明细行）：最有操作价值
// 的在前、兜底的在最后，这样无论某个工作区实际命中哪些类，图表配色都保持一致。

/** 全部失败大类（顺序即渲染顺序）：认证 / 限流 / 超时 / 上游提供方 / 运行时 / agent 自身 / 其他。 */
export const FAILURE_CLASSES = [
  "auth",
  "rate_limit",
  "timeout",
  "provider",
  "runtime",
  "agent",
  "other",
] as const;

/** 单个失败大类，是 FAILURE_CLASSES 数组元素组成的联合类型。 */
export type FailureClass = (typeof FAILURE_CLASSES)[number];

// 原始原因 → 大类 的映射表。key 是后端写入接口的原始值：22 个规范的
// `taskfailure.Reason` 字符串、失败汇总把空列的行替换成的 `"unclassified"`
// 哨兵值，以及 MUL-1949 之前仍留在历史行里的粗粒度旧值。
//
// 表里没有的原因一律落到 "other"——包括比当前客户端新的后端带来的新原因；
// 正是因为存在这种情况，这里不能做成穷举的 `Record<Reason, …>`。
const REASON_CLASS: Record<string, FailureClass> = {
  // 凭据 / 访问权限问题。
  "agent_error.provider_auth_or_access": "auth",
  "agent_error.missing_config": "auth",

  // 账号额度用尽——限流和计费配额归为一类，因为运维动作相同：等待或提高上限。
  "agent_error.provider_capacity_or_rate_limit": "rate_limit",
  "agent_error.provider_quota_limit": "rate_limit",

  // 运行超时。平台侧清扫超时和 agent 自身硬超时归在一起——从仪表盘看两者
  // 都表现为「这次运行卡住了」。
  timeout: "timeout",
  "agent_error.agent_timeout": "timeout",
  codex_semantic_inactivity: "timeout",

  // 上游模型 API 出错，或拒绝了不合理的请求。
  "agent_error.provider_server_error": "provider",
  "agent_error.provider_network": "provider",
  "agent_error.model_not_found_or_unavailable": "provider",
  api_invalid_request: "provider",

  // Multica 侧的执行底座问题：daemon 离线/重启、任务没被领取、runner 二进制
  // 缺失或过旧。
  runtime_offline: "runtime",
  runtime_recovery: "runtime",
  queued_expired: "runtime",
  "agent_error.runtime_missing_executable": "runtime",
  "agent_error.runtime_version_unsupported": "runtime",
  // daemon 从控制面拉取 agent 技能包失败，导致运行根本没开始。归到 runtime
  // 而不是 provider：运维动作是「检查 daemon 与 Multica 的连接」，和 daemon
  // 离线一样，与模型提供方无关。
  skill_bundle_unavailable: "runtime",
  // 本地运行时 CLI 在准备阶段超时。同样归 "runtime"：修复点在跑 daemon 的
  // 机器上——换更快的 CLI 或调高超时——而模型提供方从头到尾都没被请求到。
  runtime_cli_timeout: "runtime",

  // agent 进程自身产生的失败。
  "agent_error.process_failure": "agent",
  // Codex 在我们传输上限内无法交回它保存的线程。归 "agent" 而非 "runtime"：
  // daemon 健康、provider 正常——是后端自己的续跑路径无法完成。
  codex_resume_oversized: "agent",
  "agent_error.empty_or_unparseable_output": "agent",
  "agent_error.context_overflow": "agent",
  iteration_limit: "agent",
  agent_blocked: "agent",

  // 兜底 + 历史遗留的粗粒度值。
  "agent_error.unknown": "other",
  agent_error: "other",
  manual: "other",
  unclassified: "other",
};

/**
 * 把原始 `failure_reason` 归入它对应的展示大类。
 *
 * 未知原因——包括比当前客户端更新的后端引入的新原因——落到 "other" 而不是被
 * 丢弃，这样各大类总和始终能和原始失败总数对得上。
 *
 * 调用方不能传空字符串：仪表盘失败汇总里空串代表「成功」那一桶，不是失败。
 * 这里把它归到 "other"，是为了让漏传空串的调用方撑大一个可见的桶，而不是
 * 悄悄污染错误率。
 *
 * @param reason - 后端返回的原始失败原因字符串
 * @returns 对应的失败大类，未知原因返回 "other"
 */
export function failureClassOf(reason: string): FailureClass {
  return REASON_CLASS[reason] ?? "other";
}
