import { describe, expect, it } from "vitest";
import type { Agent, Skill } from "../types";
import {
  canAssignAgentToIssue,
  canDeleteSkill,
  canEditAgent,
  canEditSkill,
} from "./rules";

const ALICE = "user-alice";
const BOB = "user-bob";

function makeAgent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "agt_1",
    workspace_id: "ws_1",
    runtime_id: "rt_1",
    name: "agent",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    has_custom_env: false,
    custom_env_key_count: 0,
    mcp_config: null,
    mcp_config_redacted: false,
    scope: "workspace",
    status: "idle",
    max_concurrent_tasks: 1,
    model: "default",
    thinking_level: "",
    owner_id: ALICE,
    skills: [],
    created_at: "2026-04-01T00:00:00Z",
    updated_at: "2026-04-01T00:00:00Z",
    archived_at: null,
    archived_by: null,
    ...overrides,
  };
}

function makeSkill(createdBy: string | null): Skill {
  return {
    id: "skl_1",
    workspace_id: "ws_1",
    name: "skill",
    description: "",
    content: "",
    config: {},
    files: [],
    created_by: createdBy,
    created_at: "2026-04-01T00:00:00Z",
    updated_at: "2026-04-01T00:00:00Z",
  };
}

describe("canEditAgent", () => {
  const agent = makeAgent({ owner_id: ALICE });

  it("allows the owner", () => {
    expect(canEditAgent(agent, { userId: ALICE, role: "member" }).allowed).toBe(
      true,
    );
  });
  it("allows workspace owner", () => {
    expect(canEditAgent(agent, { userId: BOB, role: "owner" }).allowed).toBe(
      true,
    );
  });
  it("allows workspace admin", () => {
    expect(canEditAgent(agent, { userId: BOB, role: "admin" }).allowed).toBe(
      true,
    );
  });
  it("denies non-owner member", () => {
    const d = canEditAgent(agent, { userId: BOB, role: "member" });
    expect(d.allowed).toBe(false);
    expect(d.reason).toBe("not_resource_owner");
  });
  it("denies when userId is null", () => {
    const d = canEditAgent(agent, { userId: null, role: null });
    expect(d.allowed).toBe(false);
    expect(d.reason).toBe("not_authenticated");
  });
  it("denies when agent owner_id is null and user is plain member", () => {
    const orphan = makeAgent({ owner_id: null });
    expect(
      canEditAgent(orphan, { userId: ALICE, role: "member" }).allowed,
    ).toBe(false);
  });
  it("admin can still edit an orphan (owner_id null) agent", () => {
    const orphan = makeAgent({ owner_id: null });
    expect(canEditAgent(orphan, { userId: BOB, role: "admin" }).allowed).toBe(
      true,
    );
  });
});

describe("canAssignAgentToIssue", () => {
  it("allows any member to assign workspace-visibility agents", () => {
    const a = makeAgent({ scope: "workspace", owner_id: ALICE });
    expect(
      canAssignAgentToIssue(a, { userId: BOB, role: "member" }).allowed,
    ).toBe(true);
  });
  it("denies non-members from assigning workspace agents", () => {
    const a = makeAgent({ scope: "workspace", owner_id: ALICE });
    const d = canAssignAgentToIssue(a, { userId: BOB, role: null });
    expect(d.allowed).toBe(false);
    expect(d.reason).toBe("not_member");
  });
  it("allows the owner to assign their personal agent", () => {
    const a = makeAgent({ scope: "personal", owner_id: ALICE });
    expect(
      canAssignAgentToIssue(a, { userId: ALICE, role: "member" }).allowed,
    ).toBe(true);
  });
  it("allows workspace admin to assign someone else's personal agent", () => {
    const a = makeAgent({ scope: "personal", owner_id: ALICE });
    expect(
      canAssignAgentToIssue(a, { userId: BOB, role: "admin" }).allowed,
    ).toBe(true);
  });
  it("denies a plain member from assigning someone else's personal agent", () => {
    const a = makeAgent({ scope: "personal", owner_id: ALICE });
    const d = canAssignAgentToIssue(a, { userId: BOB, role: "member" });
    expect(d.allowed).toBe(false);
    expect(d.reason).toBe("private_visibility");
  });
  it("denies logged-out users", () => {
    const a = makeAgent({ scope: "workspace" });
    const d = canAssignAgentToIssue(a, { userId: null, role: null });
    expect(d.allowed).toBe(false);
    expect(d.reason).toBe("not_authenticated");
  });
});

describe("canEditSkill / canDeleteSkill", () => {
  const skill = makeSkill(ALICE);
  it("allows admins", () => {
    expect(canEditSkill(skill, { userId: BOB, role: "admin" }).allowed).toBe(
      true,
    );
  });
  it("allows the creator", () => {
    expect(canEditSkill(skill, { userId: ALICE, role: "member" }).allowed)
      .toBe(true);
  });
  it("denies non-creator member", () => {
    expect(canEditSkill(skill, { userId: BOB, role: "member" }).allowed)
      .toBe(false);
  });
  it("denies when created_by is null and user is plain member", () => {
    expect(
      canEditSkill(makeSkill(null), { userId: ALICE, role: "member" }).allowed,
    ).toBe(false);
  });
  it("canDeleteSkill mirrors canEditSkill", () => {
    expect(canDeleteSkill(skill, { userId: ALICE, role: "member" }).allowed)
      .toBe(true);
    expect(canDeleteSkill(skill, { userId: BOB, role: "member" }).allowed)
      .toBe(false);
  });
});
