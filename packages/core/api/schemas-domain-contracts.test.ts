import { describe, expect, it } from "vitest";
import { parseWithFallback } from "./schema";
import { AppConfigSchema, EMPTY_APP_CONFIG } from "./schemas-app-config";
import {
  AgentEnvResponseSchema,
  AgentSchema,
  AgentTemplateSummaryListSchema,
  EMPTY_AGENT,
  EMPTY_AGENT_TEMPLATE_SUMMARY_LIST,
  EMPTY_INTERNAL_SQUAD_TEMPLATE_RESPONSE,
  InternalSquadTemplateResponseSchema,
} from "./schemas-agents";
import { EMPTY_USER, UserSchema } from "./schemas-auth";
import {
  AutopilotRunSchema,
  AutopilotTriggerSchema,
  EMPTY_GET_AUTOPILOT_RESPONSE,
  GetAutopilotResponseSchema,
  EMPTY_LIST_WEBHOOK_DELIVERIES_RESPONSE,
  ListWebhookDeliveriesResponseSchema,
} from "./schemas-automation";
import {
  EMPTY_ISSUE,
  EMPTY_LIST_ISSUES_RESPONSE,
  EMPTY_SEARCH_ISSUES_RESPONSE,
  ListIssuesResponseSchema,
  QuickCreateIssueResponseSchema,
  SearchIssuesResponseSchema,
} from "./schemas-issues";
import {
  PromptEvaluationAssetListResponseSchema,
} from "./schemas-prompt-evaluation-assets";
import {
  PromptEvaluationCaseListResponseSchema,
} from "./schemas-prompt-evaluation-cases";
import {
  EMPTY_PROMPT_EVALUATION_ASSET_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_CASE_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE_LIST_RESPONSE,
  EMPTY_PROMPT_EVALUATION_RUN_LIST_RESPONSE,
} from "./schemas-prompt-evaluation-empty";
import {
  PromptEvaluationOptimizationCandidateListResponseSchema,
} from "./schemas-prompt-evaluation-optimization";
import {
  PromptEvaluationRunListResponseSchema,
} from "./schemas-prompt-evaluation-runs";
import {
  EMPTY_PROMPT_LIBRARY_LIST_RESPONSE,
  PromptLibraryItemListResponseSchema,
} from "./schemas-prompt-library";
import {
  EMPTY_RUNTIME_PROFILE_LIST_RESPONSE,
  RuntimeLocalSkillImportRequestSchema,
  RuntimeLocalSkillListRequestSchema,
  RuntimeModelListRequestSchema,
  RuntimeProfileListResponseSchema,
} from "./schemas-runtimes";
import { RuntimeUsageListSchema } from "./schemas-usage";
import { EMPTY_WORKSPACE, WorkspaceSchema } from "./schemas-workspaces";
import { EMPTY_INBOX_ITEM, InboxItemSchema } from "./schemas-inbox";
import { EMPTY_CHAT_MESSAGES_PAGE, ChatMessagesPageSchema } from "./schemas-chat";
import {
  ExternalCredentialProfileSchema,
  TestExternalCredentialProfileResponseSchema,
} from "./schemas-external-credentials";
import { LarkInstallStatusResponseSchema } from "./schemas-lark";
import {
  GitHubConnectResponseSchema,
  GitHubPullRequestListResponseSchema,
} from "./schemas-github";
import {
  EMPTY_PROJECT_LIST_RESPONSE,
  ListProjectsResponseSchema,
} from "./schemas-projects";
import { EMPTY_SKILL, SkillSchema } from "./schemas-skills";
import { EMPTY_LABEL_LIST_RESPONSE, ListLabelsResponseSchema } from "./schemas-labels";
import { EMPTY_PINNED_ITEM_LIST, PinnedItemListSchema } from "./schemas-pins";
import {
  AgentTaskListSchema,
  EMPTY_ISSUE_EXECUTION_TREE,
  IssueExecutionTreeResponseSchema,
} from "./schemas-tasks";

describe("domain response schema fallbacks", () => {
  it("keeps app configuration usable when the response is not an object", () => {
    expect(parseWithFallback(null, AppConfigSchema, EMPTY_APP_CONFIG, {
      endpoint: "GET /api/config",
    })).toBe(EMPTY_APP_CONFIG);
  });

  it("rejects a malformed issue collection", () => {
    expect(parseWithFallback(
      { issues: "not-an-array", total: 1 },
      ListIssuesResponseSchema,
      EMPTY_LIST_ISSUES_RESPONSE,
      { endpoint: "GET /api/issues" },
    )).toBe(EMPTY_LIST_ISSUES_RESPONSE);
  });

  it("rejects malformed search results and empty Quick Create success", () => {
    expect(parseWithFallback(
      { issues: [{ id: 42 }], total: 1 },
      SearchIssuesResponseSchema,
      EMPTY_SEARCH_ISSUES_RESPONSE,
      { endpoint: "GET /api/issues/search" },
    )).toBe(EMPTY_SEARCH_ISSUES_RESPONSE);
    expect(QuickCreateIssueResponseSchema.safeParse({}).success).toBe(false);
    expect(QuickCreateIssueResponseSchema.safeParse({ task_id: "task-1" }).success).toBe(true);
  });

  it("rejects a malformed agent template collection", () => {
    expect(parseWithFallback(
      { templates: "not-an-array" },
      AgentTemplateSummaryListSchema,
      EMPTY_AGENT_TEMPLATE_SUMMARY_LIST,
      { endpoint: "GET /api/agent-templates" },
    )).toBe(EMPTY_AGENT_TEMPLATE_SUMMARY_LIST);
  });

  it("rejects malformed Prompt Library items", () => {
    expect(parseWithFallback(
      { items: 1, total: 1 },
      PromptLibraryItemListResponseSchema,
      EMPTY_PROMPT_LIBRARY_LIST_RESPONSE,
      { endpoint: "GET /api/prompt-library" },
    )).toBe(EMPTY_PROMPT_LIBRARY_LIST_RESPONSE);
  });

  it("rejects malformed evaluation assets", () => {
    expect(parseWithFallback(
      { items: {}, total: 1 },
      PromptEvaluationAssetListResponseSchema,
      EMPTY_PROMPT_EVALUATION_ASSET_LIST_RESPONSE,
      { endpoint: "GET /api/prompt-evaluation-assets" },
    )).toBe(EMPTY_PROMPT_EVALUATION_ASSET_LIST_RESPONSE);
  });

  it("rejects malformed evaluation runs", () => {
    expect(parseWithFallback(
      { items: "not-an-array", total: 1 },
      PromptEvaluationRunListResponseSchema,
      EMPTY_PROMPT_EVALUATION_RUN_LIST_RESPONSE,
      { endpoint: "GET /api/prompt-evaluation-runs" },
    )).toBe(EMPTY_PROMPT_EVALUATION_RUN_LIST_RESPONSE);
  });

  it("rejects malformed evaluation cases", () => {
    expect(parseWithFallback(
      { items: false, total: 1 },
      PromptEvaluationCaseListResponseSchema,
      EMPTY_PROMPT_EVALUATION_CASE_LIST_RESPONSE,
      { endpoint: "GET /api/prompt-evaluation-cases" },
    )).toBe(EMPTY_PROMPT_EVALUATION_CASE_LIST_RESPONSE);
  });

  it("rejects malformed optimization candidates", () => {
    expect(parseWithFallback(
      { items: null, total: 1 },
      PromptEvaluationOptimizationCandidateListResponseSchema,
      EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE_LIST_RESPONSE,
      { endpoint: "GET /api/prompt-evaluation-optimization-candidates" },
    )).toBe(EMPTY_PROMPT_EVALUATION_OPTIMIZATION_CANDIDATE_LIST_RESPONSE);
  });

  it("rejects a malformed usage collection", () => {
    const fallback: never[] = [];
    expect(parseWithFallback(
      { rows: [] },
      RuntimeUsageListSchema,
      fallback,
      { endpoint: "GET /api/runtimes/runtime-1/usage" },
    )).toBe(fallback);
  });

  it("rejects malformed runtime profiles", () => {
    expect(parseWithFallback(
      { runtime_profiles: [{ id: 42 }] },
      RuntimeProfileListResponseSchema,
      EMPTY_RUNTIME_PROFILE_LIST_RESPONSE,
      { endpoint: "GET /api/workspaces/:workspaceId/runtime-profiles" },
    )).toBe(EMPTY_RUNTIME_PROFILE_LIST_RESPONSE);
  });

  it("rejects malformed webhook deliveries", () => {
    expect(parseWithFallback(
      { deliveries: {}, total: 1 },
      ListWebhookDeliveriesResponseSchema,
      EMPTY_LIST_WEBHOOK_DELIVERIES_RESPONSE,
      { endpoint: "GET /api/webhook-deliveries" },
    )).toBe(EMPTY_LIST_WEBHOOK_DELIVERIES_RESPONSE);
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
      autopilot_id: "autopilot-1",
      kind: "webhook",
      enabled: true,
      cron_expression: null,
      timezone: null,
      next_run_at: null,
      webhook_token: "current-bearer-token",
      label: null,
      last_fired_at: null,
      created_at: "2026-07-11T00:00:00Z",
      updated_at: "2026-07-11T00:00:00Z",
      signing_secret: "must-not-cross-boundary",
      signing_secret_ciphertext: "ciphertext",
    });
    expect(parsed).toHaveProperty("webhook_token", "current-bearer-token");
    expect(parsed).not.toHaveProperty("signing_secret");
    expect(parsed).not.toHaveProperty("signing_secret_ciphertext");
  });

  it("rejects a malformed current user", () => {
    expect(parseWithFallback(
      { id: 42 },
      UserSchema,
      EMPTY_USER,
      { endpoint: "GET /api/me" },
    )).toBe(EMPTY_USER);
  });

  it("rejects a malformed workspace identity", () => {
    expect(parseWithFallback(
      { id: 42, name: "broken" },
      WorkspaceSchema,
      EMPTY_WORKSPACE,
      { endpoint: "GET /api/workspaces/:id" },
    )).toBe(EMPTY_WORKSPACE);
  });

  it("rejects a malformed inbox identity", () => {
    expect(parseWithFallback(
      { id: 42 },
      InboxItemSchema,
      EMPTY_INBOX_ITEM,
      { endpoint: "GET /api/inbox" },
    )).toBe(EMPTY_INBOX_ITEM);
  });

  it("rejects a malformed current chat page", () => {
    expect(parseWithFallback(
      { messages: "invalid" },
      ChatMessagesPageSchema,
      EMPTY_CHAT_MESSAGES_PAGE,
      { endpoint: "GET /api/chat/sessions/:id/messages/page" },
    )).toBe(EMPTY_CHAT_MESSAGES_PAGE);
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
      ListProjectsResponseSchema,
      EMPTY_PROJECT_LIST_RESPONSE,
      { endpoint: "GET /api/projects" },
    )).toBe(EMPTY_PROJECT_LIST_RESPONSE);
    expect(parseWithFallback(
      { id: "skill-1", workspace_id: "workspace-1", files: "invalid" },
      SkillSchema,
      EMPTY_SKILL,
      { endpoint: "GET /api/skills/:id" },
    )).toBe(EMPTY_SKILL);
  });

  it("rejects malformed Label, Pin and internal Squad identities", () => {
    expect(parseWithFallback(
      { labels: [{ id: "label-1", color: "red" }], total: 1 },
      ListLabelsResponseSchema,
      EMPTY_LABEL_LIST_RESPONSE,
      { endpoint: "GET /api/labels" },
    )).toBe(EMPTY_LABEL_LIST_RESPONSE);
    expect(parseWithFallback(
      [{ id: "pin-1", item_id: 42 }],
      PinnedItemListSchema,
      EMPTY_PINNED_ITEM_LIST,
      { endpoint: "GET /api/pins" },
    )).toBe(EMPTY_PINNED_ITEM_LIST);
    expect(parseWithFallback(
      { squad: { id: 42 }, agents: [] },
      InternalSquadTemplateResponseSchema,
      EMPTY_INTERNAL_SQUAD_TEMPLATE_RESPONSE,
      { endpoint: "POST /api/squads/internal-template" },
    )).toBe(EMPTY_INTERNAL_SQUAD_TEMPLATE_RESPONSE);
  });

  it("accepts the current task execution response and preserves additive fields", () => {
    const task = {
      id: "task-1",
      agent_id: "agent-1",
      runtime_id: "runtime-1",
      issue_id: "issue-1",
      status: "running",
      priority: 2,
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
        sop_runs: [],
        task_messages: [],
        trace_events: [],
        tool_call_chains: [],
        tool_call_summary: [],
        artifacts: [],
        wakeup_comments: [],
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
        node_count: 1,
        total_duration_ms: 0,
        wall_clock_duration_ms: null,
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
        full_analysis_deep_link: "/workspace/issues/issue-1",
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
      workspace_id: "workspace-1",
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
      last_verified_at: null,
    }).success).toBe(false);
    expect(TestExternalCredentialProfileResponseSchema.safeParse({
      provider: "gongfeng",
      status: "verified",
      last_verified_at: null,
    }).success).toBe(false);
  });

  it("requires terminal Lark install states to carry their discriminator data", () => {
    expect(LarkInstallStatusResponseSchema.safeParse({ status: "pending" }).success).toBe(true);
    expect(LarkInstallStatusResponseSchema.safeParse({ status: "success" }).success).toBe(false);
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
        workspace_id: "workspace-1",
        repo_owner: "ChainWeaver/ida",
        repo_name: "user-center",
        number: 61234,
        title: "Current Gongfeng merge request",
        state: "open",
        html_url: "https://git.code.tencent.com/ChainWeaver/ida/user-center/merge_requests/61234",
        branch: null,
        author_login: null,
        author_avatar_url: null,
        merged_at: null,
        closed_at: null,
      }],
    }).pull_requests).toHaveLength(1);

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
      skill_key: "skill-1",
      status: "completed",
    }).success).toBe(false);
    expect(RuntimeLocalSkillImportRequestSchema.safeParse({
      ...base,
      skill_key: "skill-1",
      status: "conflict",
    }).success).toBe(false);
  });

  it("normalizes the current completed runtime skill import payload", () => {
    const parsed = RuntimeLocalSkillImportRequestSchema.parse({
      id: "request-1",
      runtime_id: "runtime-1",
      skill_key: "skill-1",
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
      created_at: "2026-07-11T00:00:00Z",
      updated_at: "2026-07-11T00:00:00Z",
    });

    expect(parsed.skill?.files).toEqual([]);
  });
});
