/**
 * Mirror of `packages/views/agents/components/tabs/task-failure.ts:REASON_LABEL`.
 *
 * Why mirror: mobile cannot import from packages/views per the apps/mobile
 * CLAUDE.md sharing rule. Only the human copy is mobile-owned.
 *
 * Keyed by the raw wire value rather than a closed enum, same as the web map:
 * `failure_reason` is an open string that grows as classifier rules land, and
 * an installed build will meet reasons it predates. Before MUL-5370 this was a
 * `Record<TaskFailureReason, string>` holding only the six pre-MUL-1949 coarse
 * values, so every refined `agent_error.*` the backend has written since
 * missed the lookup and rendered a bare "失败".
 *
 * Divergence from web, deliberate: the web helper falls back to the raw wire
 * value, which is machine-y but searchable — right for an operator reading the
 * execution log. This one backs a chat bubble read by the person who just sent
 * a message, so an unrecognised reason degrades to a plain "失败" instead of
 * leaking an enum string at them.
 */
const LABELS: Record<string, string> = {
  // Platform / scheduler side.
  queued_expired: "排队已过期",
  runtime_offline: "守护进程离线",
  runtime_recovery: "守护进程已重启",
  timeout: "任务超时",
  iteration_limit: "达到迭代上限",
  agent_blocked: "等待人工输入",
  api_invalid_request: "模型接口拒绝了请求",
  skill_bundle_unavailable: "无法下载智能体技能",
  runtime_cli_timeout: "本地运行时命令超时",

  // Agent process side — provider.
  "agent_error.provider_auth_or_access": "模型服务鉴权失败",
  "agent_error.provider_quota_limit": "模型服务额度已用尽",
  "agent_error.provider_capacity_or_rate_limit": "模型服务限流",
  "agent_error.provider_server_error": "模型服务端错误",
  "agent_error.provider_network": "连接模型服务失败",

  // Agent process side — agent / runner.
  "agent_error.process_failure": "智能体进程异常退出",
  "agent_error.empty_or_unparseable_output": "智能体没有返回可用内容",
  "agent_error.agent_timeout": "智能体超时",
  "agent_error.context_overflow": "超出上下文窗口",
  "agent_error.missing_config": "缺少 API 密钥或配置",
  "agent_error.model_not_found_or_unavailable": "模型不可用",
  "agent_error.runtime_version_unsupported": "运行命令版本不受支持",
  "agent_error.runtime_missing_executable": "未安装运行命令",
  "agent_error.unknown": "智能体执行错误",

  // Pre-MUL-1949 coarse values, still present on historical rows.
  agent_error: "智能体执行错误",
  codex_semantic_inactivity: "Codex 长时间无有效输出",
  manual: "用户已取消",
};

export function failureReasonLabel(reason: string | null | undefined): string {
  if (!reason) return "失败";
  return LABELS[reason] ?? "失败";
}
