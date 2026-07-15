import type { Agent } from "@multica/core/types";

export function makeAgent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "agent-1",
    runtime_id: "runtime-1",
    name: "Agent",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    custom_env_key_count: 0,
    mcp_config: null,
    mcp_config_redacted: false,
    scope: "workspace",
    max_concurrent_tasks: 1,
    model: "",
    thinking_level: "",
    owner_id: "user-1",
    skills: [],
    created_at: "2026-04-01T00:00:00Z",
    updated_at: "2026-04-01T00:00:00Z",
    archived_at: null,
    ...overrides,
  };
}
