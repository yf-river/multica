import { describe, expect, it, vi } from "vitest";
import { workspaceKeys } from "@multica/core/workspace/queries";
import type { Agent, MemberWithUser } from "@multica/core/types";
import type { QueryClient } from "@tanstack/react-query";

vi.mock("@multica/core/platform", () => ({
  getCurrentWsId: () => "ws-1",
}));

const authState = { user: { id: "u1" } as { id: string } | null };
vi.mock("@multica/core/auth", () => ({
  useAuthStore: { getState: () => authState },
}));

const chatState = { selectedAgentId: "agent-1" as string | null };
vi.mock("@multica/core/chat", () => ({
  useChatStore: { getState: () => chatState },
}));

import {
  createSlashCommandSuggestion,
  createBuiltinCommandSuggestion,
} from "./slash-command-suggestion";

interface SlashCommandItem {
  id: string;
  label: string;
  description?: string;
  descriptionKey?: "note";
}

function agent(overrides: Partial<Agent>): Agent {
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
    owner_id: null,
    skills: [],
    created_at: "",
    updated_at: "",
    archived_at: null,
    ...overrides,
  };
}

function fakeQc(data: {
  members?: Array<Pick<MemberWithUser, "user_id" | "name" | "role">>;
  agents?: Agent[];
}): QueryClient {
  const map = new Map<string, unknown>();
  map.set(JSON.stringify(workspaceKeys.members("ws-1")), data.members ?? []);
  map.set(JSON.stringify(workspaceKeys.agents("ws-1")), data.agents ?? []);
  return {
    getQueryData: (key: readonly unknown[]) => map.get(JSON.stringify(key)),
  } as unknown as QueryClient;
}

function items(qc: QueryClient, query = ""): SlashCommandItem[] {
  const config = createSlashCommandSuggestion(qc);
  return config.items!({ query, editor: {} as never }) as SlashCommandItem[];
}

function builtinItems(query = ""): SlashCommandItem[] {
  const config = createBuiltinCommandSuggestion();
  return config.items!({ query, editor: {} as never }) as SlashCommandItem[];
}

function activeAgentItems(skills: Agent["skills"], query = "") {
  chatState.selectedAgentId = "agent-1";
  return items(fakeQc({
    members: [{ user_id: "u1", name: "Alice", role: "member" }],
    agents: [agent({ id: "agent-1", skills })],
  }), query);
}

describe("slash command suggestion items", () => {
  it("returns all active agent skills when query is empty", () => {
    expect(activeAgentItems([
      { id: "s1", name: "deploy", description: "Ship changes" },
      { id: "s2", name: "review", description: "Review code" },
    ]).map((i) => i.label)).toEqual(["deploy", "review"]);
  });

  it("filters skills by name case-insensitively", () => {
    expect(activeAgentItems([
      { id: "s1", name: "Deploy", description: "" },
      { id: "s2", name: "Review", description: "" },
    ], "dep").map((i) => i.id)).toEqual(["s1"]);
  });

  it("filters skills by description", () => {
    expect(activeAgentItems([
      { id: "s1", name: "deploy", description: "Ship changes" },
      { id: "s2", name: "review", description: "Read a pull request" },
    ], "pull").map((i) => i.id)).toEqual(["s2"]);
  });

  it("tolerates skills with missing descriptions from cached API data", () => {
    const skills = [
      { id: "s1", name: "deploy" } as Agent["skills"][number],
    ];
    expect(() => activeAgentItems(skills, "dep")).not.toThrow();
    expect(activeAgentItems(skills, "dep")).toEqual([
      { id: "s1", label: "deploy", description: "" },
    ]);
  });

  it("returns empty when the active agent has no skills", () => {
    expect(activeAgentItems([])).toEqual([]);
  });

  it("caps results at 20", () => {
    expect(activeAgentItems(Array.from({ length: 25 }, (_, i) => ({
      id: `s${i}`,
      name: `skill-${i}`,
      description: "",
    })))).toHaveLength(20);
  });

  it("falls back to the first available agent when selectedAgentId is stale", () => {
    chatState.selectedAgentId = "missing";
    const qc = fakeQc({
      members: [{ user_id: "u1", name: "Alice", role: "member" }],
      agents: [
        agent({
          id: "agent-1",
          skills: [{ id: "s1", name: "deploy", description: "" }],
        }),
      ],
    });

    expect(items(qc).map((i) => i.id)).toEqual(["s1"]);
  });

  it("returns empty when no agents exist", () => {
    const qc = fakeQc({
      members: [{ user_id: "u1", name: "Alice", role: "member" }],
      agents: [],
    });

    expect(items(qc)).toEqual([]);
  });

  it("excludes skills from personal agents the user cannot access", () => {
    chatState.selectedAgentId = "private-agent";
    const qc = fakeQc({
      members: [
        { user_id: "u1", name: "Alice", role: "member" },
        { user_id: "u2", name: "Bob", role: "member" },
      ],
      agents: [
        agent({
          id: "private-agent",
          scope: "personal",
          owner_id: "u2",
          skills: [{ id: "private-skill", name: "secret", description: "" }],
        }),
      ],
    });

    expect(items(qc)).toEqual([]);
  });
});

describe("buildBuiltinCommandItems", () => {
  it("returns the full built-in command set for an empty query", () => {
    expect(builtinItems()).toEqual([
      { id: "note", label: "note", descriptionKey: "note" },
    ]);
  });

  it("includes /note while the query is a prefix of the label", () => {
    expect(builtinItems("no").map((c) => c.id)).toEqual(["note"]);
    expect(builtinItems("NOTE").map((c) => c.id)).toEqual(["note"]);
  });

  it("matches the label as a prefix only — not the description", () => {
    // "agent" appears in the description but is not a label prefix.
    expect(builtinItems("agent")).toEqual([]);
    // A non-prefix substring of the label does not match either.
    expect(builtinItems("ote")).toEqual([]);
  });

  it("returns nothing for a query that matches no command", () => {
    expect(builtinItems("deploy")).toEqual([]);
  });
});
