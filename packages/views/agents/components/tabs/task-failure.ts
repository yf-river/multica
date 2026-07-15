import type { TaskFailureReason } from "@multica/core/types";

// Shared user-facing copy for task failure reasons in Agent activity and Chat.
export const failureReasonLabel: Record<TaskFailureReason, string> = {
  agent_error: "Agent execution error",
  timeout: "Task timed out",
  codex_semantic_inactivity: "Codex semantic inactivity timeout",
  runtime_offline: "Daemon offline",
  runtime_recovery: "Daemon restarted",
  manual: "Cancelled by user",
};
