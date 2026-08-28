import { describe, expect, it } from "vitest";
import type { ZodType } from "zod";
import { parseWithFallback } from "./schema";
import { AppConfigSchema, EMPTY_APP_CONFIG } from "./schemas-app-config";
import {
  AgentEnvResponseSchema,
  AgentSchema,
  EMPTY_AGENT,
} from "./schemas-agents";
import { EMPTY_USER, UserSchema } from "./schemas-auth";
import {
  AutopilotRunSchema,
  AutopilotTriggerSchema,
  EMPTY_WEBHOOK_DELIVERIES,
  EMPTY_GET_AUTOPILOT_RESPONSE,
  GetAutopilotResponseSchema,
  WebhookDeliveryListSchema,
} from "./schemas-automation";
import {
  EMPTY_ISSUE,
  EMPTY_LIST_ISSUES_RESPONSE,
  EMPTY_SEARCH_ISSUES,
  ListIssuesResponseSchema,
  QuickCreateIssueResponseSchema,
  SearchIssuesSchema,
} from "./schemas-issues";
import {
  RuntimeLocalSkillImportRequestSchema,
  RuntimeLocalSkillListRequestSchema,
  RuntimeModelListRequestSchema,
  RuntimeProfileListResponseSchema,
} from "./schemas-runtimes";
import { RuntimeUsageListSchema } from "./schemas-usage";
import { WorkspaceListSchema, WorkspaceSchema } from "./schemas-workspaces";
import { InboxListSchema } from "./schemas-inbox";
import { EMPTY_CHAT_MESSAGES_PAGE, ChatMessagesPageSchema } from "./schemas-chat";
import {
  ExternalCredentialProfileSchema,
  TestExternalCredentialProfileResponseSchema,
} from "./schemas-external-credentials";
import { LarkInstallationListResponseSchema, LarkInstallStatusResponseSchema } from "./schemas-lark";
import {
  GitHubConnectResponseSchema,
  GitHubPullRequestListResponseSchema,
} from "./schemas-github";
import {
  EMPTY_PROJECTS,
  ProjectListSchema,
} from "./schemas-projects";
import { EMPTY_SKILL, SkillSchema } from "./schemas-skills";
import {
  EMPTY_LABELS,
  IssueLabelListSchema,
  LabelListSchema,
} from "./schemas-labels";
import { EMPTY_PINNED_ITEM_LIST, PinnedItemListSchema } from "./schemas-pins";
import {
  AgentTaskListSchema,
  EMPTY_ISSUE_EXECUTION_TREE,
  IssueExecutionTreeResponseSchema,
} from "./schemas-tasks";

function expectFallback(
  data: unknown,
  schema: ZodType,
  fallback: unknown,
  endpoint: string,
) {
  expect(parseWithFallback(data, schema, fallback, { endpoint })).toBe(fallback);
}

describe("domain response schema fallbacks", () => {
  it("rejects incomplete issue-label mutation responses", () => {
    expect(IssueLabelListSchema.safeParse({}).success).toBe(false);
  });
  it("requires the current Lark installation contract", () => {
    expect(LarkInstallationListResponseSchema.safeParse({
      configured: true,
      installations: [{
        id: "installation-1",
        agent_id: "agent-1",
        app_id: "app-1",
        status: "active",
      }],
    }).success).toBe(false);

    const parsed = LarkInstallationListResponseSchema.parse({
      configured: true,
      install_supported: false,
      installations: [{
        id: "installation-1",
        agent_id: "agent-1",
        app_id: "app-1",
        status: "active",
        region: "feishu",
        installed_at: "2026-07-01T00:00:00Z",
      }],
    });
    expect(parsed.install_supported).toBe(false);
    expect(parsed.installations[0]?.region).toBe("feishu");
  });

  it("keeps app configuration usable when the response is not an object", () => {
    expect(parseWithFallback(null, AppConfigSchema, EMPTY_APP_CONFIG, {
      endpoint: "GET /api/config",
    })).toBe(EMPTY_APP_CONFIG);
  });

  it.each([
    ["issues", { issues: "not-an-array", total: 1 }, ListIssuesResponseSchema, EMPTY_LIST_ISSUES_RESPONSE, "GET /api/issues"],
    ["usage", { rows: [] }, RuntimeUsageListSchema, [], "GET /api/runtimes/runtime-1/usage"],
    ["runtime profiles", { runtime_profiles: [{ id: 42 }] }, RuntimeProfileListResponseSchema, [], "GET /api/workspaces/:workspaceId/runtime-profiles"],
    ["webhook deliveries", { deliveries: {}, total: 1 }, WebhookDeliveryListSchema, EMPTY_WEBHOOK_DELIVERIES, "GET /api/webhook-deliveries"],
    ["current user", { id: 42 }, UserSchema, EMPTY_USER, "GET /api/me"],
    ["workspaces", [{ id: 42, name: "broken" }], WorkspaceListSchema, [], "GET /api/workspaces"],
    ["inbox", [{ id: 42 }], InboxListSchema, [], "GET /api/inbox"],
    ["chat page", { messages: "invalid" }, ChatMessagesPageSchema, EMPTY_CHAT_MESSAGES_PAGE, "GET /api/chat/sessions/:id/messages/page"],
  ] as const)("rejects malformed %s responses", (_name, data, schema, fallback, endpoint) => {
    expectFallback(data, schema, fallback, endpoint);
  });

  it("rejects malformed search results and empty Quick Create success", () => {
    expect(parseWithFallback(
      { issues: [{ id: 42 }], total: 1 },
      SearchIssuesSchema,
      EMPTY_SEARCH_ISSUES,
      { endpoint: "GET /api/issues/search" },
    )).toBe(EMPTY_SEARCH_ISSUES);
    expect(QuickCreateIssueResponseSchema.safeParse({}).success).toBe(false);
    expect(QuickCreateIssueResponseSchema.safeParse({ task_id: "task-1" }).success).toBe(true);
  });




  it("rejects malformed Autopilot identity and run linkage", () => {
    expect(parseWithFallback(
      { autopilot: { id: 42 }, triggers: [] },
      GetAutopilotResponseSchema,
      EMPTY_GET_AUTOPILOT_RESPONSE,
      { endpoint: "GET /api/autopilots/:id" },
    )).toBe(EMPTY_GET_AUTOPILOT_RESPONSE);
    expect(AutopilotRunSchema.safeParse({
      id: "run-1",
      autopilot_id: "",
      source: "manual",
      status: "running",
    }).success).toBe(false);
  });

  it("strips write-only Autopilot signing secrets", () => {
    const parsed = AutopilotTriggerSchema.parse({
      id: "trigger-1",
      kind: "webhook",
      enabled: true,
      cron_expression: null,
      timezone: null,
      next_run_at: null,
      webhook_token: "current-bearer-token",
      label: null,
      signing_secret: "must-not-cross-boundary",
      signing_secret_ciphertext: "ciphertext",
    });
    expect(parsed).toHaveProperty("webhook_token", "current-bearer-token");
    expect(parsed).not.toHaveProperty("signing_secret");
    expect(parsed).not.toHaveProperty("signing_secret_ciphertext");
  });


  it("normalizes GitHub settings drift at the workspace response boundary", () => {
    const workspace = WorkspaceSchema.parse({
      id: "workspace-1",
      name: "Workspace",
      slug: "workspace",
      settings: { github_enabled: false, co_authored_by_enabled: null, custom: "kept" },
    });
    expect(workspace.settings).toEqual({
      github_enabled: false,
      github_pr_sidebar_enabled: true,
      co_authored_by_enabled: true,
      custom: "kept",
    });
  });


  it("rejects malformed task identity and execution-tree roots", () => {
    const emptyTasks: never[] = [];
    expect(parseWithFallback(
      [{ id: 42, status: "running" }],
      AgentTaskListSchema,
      emptyTasks,
      { endpoint: "GET /api/agent-task-snapshot" },
    )).toBe(emptyTasks);
    expect(parseWithFallback(
      { root: { tasks: [] }, summary: {} },
      IssueExecutionTreeResponseSchema,
      EMPTY_ISSUE_EXECUTION_TREE,
      { endpoint: "GET /api/issues/:id/execution-tree" },
    )).toBe(EMPTY_ISSUE_EXECUTION_TREE);
  });

  it("rejects malformed Project and Skill identities", () => {
    expect(parseWithFallback(
      { projects: [{ id: 42 }], total: 1 },
      ProjectListSchema,
      EMPTY_PROJECTS,
      { endpoint: "GET /api/projects" },
    )).toBe(EMPTY_PROJECTS);
    expect(parseWithFallback(
      { id: "skill-1", files: "invalid" },
      SkillSchema,
      EMPTY_SKILL,
      { endpoint: "GET /api/skills/:id" },
    )).toBe(EMPTY_SKILL);
  });

  it("rejects malformed Label and Pin identities", () => {
    expect(parseWithFallback(
      { labels: [{ id: "label-1", color: "red" }], total: 1 },
      LabelListSchema,
      EMPTY_LABELS,
      { endpoint: "GET /api/labels" },
    )).toBe(EMPTY_LABELS);
    expect(parseWithFallback(
      [{ id: "pin-1", item_id: 42 }],
      PinnedItemListSchema,
      EMPTY_PINNED_ITEM_LIST,
      { endpoint: "GET /api/pins" },
    )).toBe(EMPTY_PINNED_ITEM_LIST);
  });

  it("accepts the current task execution response and preserves additive fields", () => {
    const task = {
      id: "task-1",
      agent_id: "agent-1",
      runtime_id: "runtime-1",
      issue_id: "issue-1",
      status: "running",
      dispatched_at: null,
      started_at: null,
      completed_at: null,
      result: null,
      error: null,
      created_at: "2026-07-11T00:00:00Z",
      future_runtime_field: "preserved",
    };
    expect(AgentTaskListSchema.parse([task])[0]).toHaveProperty(
      "future_runtime_field",
      "preserved",
    );

    const parsed = IssueExecutionTreeResponseSchema.safeParse({
      root: {
        issue: {
          ...EMPTY_ISSUE,
          id: "issue-1",
          workspace_id: "workspace-1",
        },
        tasks: [task],
        task_messages: [],
        trace_events: [],
        tool_call_chains: [],
        tool_call_summary: [],
        artifacts: [],
        children: [],
      },
      summary: { task_count: 1 },
      timeline_nodes: [{
        issue_id: "issue-1",
        node_id: "status:issue-1",
        node_type: "status_change",
        status: "completed",
        artifacts: null,
      }],
      issue_summary: {
        issue_id: "issue-1",
        total_duration_ms: 0,
        agent_execution_duration_ms: 0,
        human_confirmation_duration_ms: null,
        child_issue_wait_duration_ms: null,
        total_input_tokens: 0,
        total_output_tokens: 0,
        total_cache_read_tokens: 0,
        total_cache_write_tokens: 0,
        message_count: 0,
        agent_turn_count: 0,
        trace_event_count: 0,
        usage_unavailable: false,
        acceptance_status: "running",
      },
    });
    expect(parsed.success).toBe(true);
    if (parsed.success) {
      expect(parsed.data.timeline_nodes[0]?.artifacts).toEqual([]);
    }
  });

  it("strips forbidden secret fields from generic agent responses", () => {
    const parsed = AgentSchema.parse({
      ...EMPTY_AGENT,
      id: "agent-1",
      runtime_id: "runtime-1",
      custom_env: { TOKEN: "secret" },
      custom_env_redacted: { TOKEN: "****" },
      future_capability: "kept",
    });
    expect(parsed).not.toHaveProperty("custom_env");
    expect(parsed).not.toHaveProperty("custom_env_redacted");
    expect(parsed).toHaveProperty("future_capability", "kept");
  });

  it("requires the complete dedicated agent environment payload", () => {
    expect(AgentEnvResponseSchema.safeParse({ agent_id: "agent-1" }).success).toBe(false);
    expect(AgentEnvResponseSchema.parse({
      agent_id: "agent-1",
      custom_env: {},
      ignored: "not exposed",
    })).toEqual({ agent_id: "agent-1", custom_env: {} });
  });

  it("fails closed when credential responses omit their redaction contract", () => {
    expect(ExternalCredentialProfileSchema.safeParse({
      id: "profile-1",
      user_id: "user-1",
      scope: "account",
      provider: "gongfeng",
      name: "Gongfeng",
      status: "verified",
    }).success).toBe(false);
    expect(TestExternalCredentialProfileResponseSchema.safeParse({
      provider: "gongfeng",
      status: "verified",
    }).success).toBe(false);
  });

  it("requires failed Lark install states to carry their error discriminator", () => {
    expect(LarkInstallStatusResponseSchema.safeParse({ status: "pending" }).success).toBe(true);
    expect(LarkInstallStatusResponseSchema.safeParse({ status: "success" }).success).toBe(true);
    expect(LarkInstallStatusResponseSchema.safeParse({ status: "error" }).success).toBe(false);
  });

  it("drops a GitHub URL whenever the integration is not configured", () => {
    expect(GitHubConnectResponseSchema.parse({
      configured: false,
      url: "https://github.com/apps/ignored/installations/new",
    })).toEqual({ configured: false });
  });

  it("accepts current HTTPS merge-request providers without weakening install URLs", () => {
    expect(GitHubPullRequestListResponseSchema.parse({
      pull_requests: [{
        id: "pr-61234",
        repo_owner: "ChainWeaver/ida",
        repo_name: "user-center",
        number: 61234,
        title: "Current Gongfeng merge request",
        state: "open",
        html_url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/merge_requests/61234",
        author_login: null,
        mergeable_state: null,
        checks_conclusion: null,
        checks_passed: 0,
        checks_failed: 0,
        checks_pending: 0,
        additions: 0,
        deletions: 0,
        changed_files: 0,
      }],
    })).toHaveLength(1);

    expect(GitHubConnectResponseSchema.safeParse({
      configured: true,
      url: "https://git.code.tencent.com/install",
    }).success).toBe(false);
  });

  it("rejects unknown runtime polling states and incomplete terminal payloads", () => {
    const base = {
      id: "request-1",
      runtime_id: "runtime-1",
      supported: true,
      created_at: "2026-07-11T00:00:00Z",
      updated_at: "2026-07-11T00:00:00Z",
    };

    expect(RuntimeModelListRequestSchema.safeParse({
      ...base,
      status: "unknown",
    }).success).toBe(false);
    expect(RuntimeLocalSkillListRequestSchema.safeParse({
      ...base,
      status: "completed",
      skills: "not-an-array",
    }).success).toBe(false);
    expect(RuntimeLocalSkillImportRequestSchema.safeParse({
      ...base,
      status: "completed",
    }).success).toBe(false);
    expect(RuntimeLocalSkillImportRequestSchema.safeParse({
      ...base,
      status: "conflict",
    }).success).toBe(false);
  });

  it("normalizes the current completed runtime skill import payload", () => {
    const parsed = RuntimeLocalSkillImportRequestSchema.parse({
      id: "request-1",
      runtime_id: "runtime-1",
      status: "completed",
      skill: {
        id: "skill-id",
        workspace_id: "workspace-1",
        name: "Imported skill",
        description: "",
        content: "# Skill",
        config: {},
        created_by: "user-1",
        created_at: "2026-07-11T00:00:00Z",
        updated_at: "2026-07-11T00:00:00Z",
      },
    });

    expect(parsed.skill?.files).toEqual([]);
  });
});
