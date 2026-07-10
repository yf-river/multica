import { describe, expect, it } from "vitest";
import { parseWithFallback } from "./schema";
import { AppConfigSchema, EMPTY_APP_CONFIG } from "./schemas-app-config";
import {
  AgentTemplateSummaryListSchema,
  EMPTY_AGENT_TEMPLATE_SUMMARY_LIST,
} from "./schemas-agents";
import { EMPTY_USER, UserSchema } from "./schemas-auth";
import {
  EMPTY_LIST_WEBHOOK_DELIVERIES_RESPONSE,
  ListWebhookDeliveriesResponseSchema,
} from "./schemas-automation";
import {
  EMPTY_LIST_ISSUES_RESPONSE,
  ListIssuesResponseSchema,
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
  RuntimeProfileListResponseSchema,
} from "./schemas-runtimes";
import { RuntimeUsageListSchema } from "./schemas-usage";
import { EMPTY_WORKSPACE, WorkspaceSchema } from "./schemas-workspaces";
import { EMPTY_INBOX_ITEM, InboxItemSchema } from "./schemas-inbox";

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
});
