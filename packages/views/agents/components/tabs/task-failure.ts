// 本文件提供“任务失败原因”的本地化标签工具：把后端下发的 failure_reason 字符串
// 翻译成界面文案；未知原因原样返回，让旧客户端面对新后端时如实降级而不是报错。

import type { useT } from "../../../i18n";

type AgentsT = ReturnType<typeof useT<"agents">>["t"];

// 稳定的翻译键，覆盖三类来源：服务端规范分类、provider 特有的操作层原因、
// 以及历史行上仍残留的粗粒度旧值。线上的值是开放字符串：客户端可能遇到比自己
// 版本更新的原因，`failureReasonLabel` 特意回退显示原始值。
export const FAILURE_REASON_I18N_KEYS = {
  // Platform / scheduler side.
  queued_expired: "queued_expired",
  runtime_offline: "runtime_offline",
  runtime_access_revoked: "runtime_access_revoked",
  runtime_reconnect_timeout: "runtime_reconnect_timeout",
  runtime_recovery: "runtime_recovery",
  release_pause: "release_pause",
  timeout: "timeout",
  iteration_limit: "iteration_limit",
  agent_blocked: "agent_blocked",
  api_invalid_request: "api_invalid_request",
  skill_bundle_unavailable: "skill_bundle_unavailable",
  runtime_cli_timeout: "runtime_cli_timeout",
  environment_prepare_failed: "environment_prepare_failed",
  invalid_task_identity: "invalid_task_identity",
  runtime_access_denied: "runtime_access_denied",

  // Agent process side — provider.
  "agent_error.provider_auth_or_access":
    "agent_error_provider_auth_or_access",
  "agent_error.provider_quota_limit": "agent_error_provider_quota_limit",
  "agent_error.provider_capacity_or_rate_limit":
    "agent_error_provider_capacity_or_rate_limit",
  "agent_error.provider_server_error": "agent_error_provider_server_error",
  "agent_error.provider_network": "agent_error_provider_network",

  // Agent process side — agent / runner.
  "agent_error.process_failure": "agent_error_process_failure",
  "agent_error.empty_or_unparseable_output":
    "agent_error_empty_or_unparseable_output",
  "agent_error.agent_timeout": "agent_error_agent_timeout",
  "agent_error.context_overflow": "agent_error_context_overflow",
  "agent_error.missing_config": "agent_error_missing_config",
  "agent_error.model_not_found_or_unavailable":
    "agent_error_model_not_found_or_unavailable",
  "agent_error.runtime_version_unsupported":
    "agent_error_runtime_version_unsupported",
  "agent_error.runtime_missing_executable":
    "agent_error_runtime_missing_executable",
  "agent_error.unknown": "agent_error_unknown",

  // Daemon operational reasons, outside the canonical taxonomy.
  agent_fallback_message: "agent_fallback_message",
  codex_semantic_inactivity: "codex_semantic_inactivity",
  codex_resume_oversized: "codex_resume_oversized",
  idle_watchdog: "idle_watchdog",
  local_directory_error: "local_directory_error",
  cancelled: "cancelled",

  // Pre-MUL-1949 coarse values, still present on historical rows.
  agent_error: "agent_error",
  manual: "manual",
  user_cancelled: "user_cancelled",
} as const;

type KnownFailureReason = keyof typeof FAILURE_REASON_I18N_KEYS;

/**
 * 某个 `failure_reason` 的本地化标签；无需显示时返回 null。未知值保留原始串——
 * 当新版后端加了新原因，旧版已安装客户端会如实降级显示。
 */
export function failureReasonLabel(
  reason: string | null | undefined,
  t: AgentsT,
): string | null {
  if (!reason) return null;
  if (!Object.prototype.hasOwnProperty.call(FAILURE_REASON_I18N_KEYS, reason)) {
    return reason;
  }
  const key = FAILURE_REASON_I18N_KEYS[reason as KnownFailureReason];
  return t(($) => $.task_failure.reasons[key]);
}

/**
 * 被服务端因持久化原因取消的任务的标签。用户自己发起的取消没有原因、不加标签。
 */
export function cancelReasonLabel(
  task: {
    status: string;
    error?: string | null;
    failure_reason?: string | null;
  },
  t: AgentsT,
): string | null {
  if (task.status !== "cancelled") return null;
  const reason = failureReasonLabel(task.failure_reason, t);
  if (reason) return reason;
  return task.error ? t(($) => $.task_failure.cancelled_by_system) : null;
}
