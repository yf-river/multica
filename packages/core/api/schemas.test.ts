import { describe, expect, it } from "vitest";
import {
  AppConfigSchema,
  BatchDeleteIssuesResponseSchema,
  BatchUpdateIssuesResponseSchema,
  DashboardAgentRunTimeListSchema,
  DashboardUsageByAgentListSchema,
  DashboardUsageDailyListSchema,
  EMPTY_USER,
  ListIssuesResponseSchema,
  RuntimeUsageByAgentListSchema,
  RuntimeUsageByTaskListSchema,
  RuntimeUsageListSchema,
  ObservabilitySummarySchema,
  PromptEvaluationAssetListResponseSchema,
  PromptEvaluationAssetSchema,
  PromptLibraryItemListResponseSchema,
  PromptLibraryItemSchema,
  SquadListSchema,
  TimelineEntriesSchema,
  UserSchema,
} from "./schemas";
import { parseWithFallback } from "./schema";

const baseIssue = {
  id: "11111111-1111-1111-1111-111111111111",
  workspace_id: "ws-1",
  number: 1,
  identifier: "MUL-1",
  title: "Test",
  description: null,
  status: "todo",
  priority: "medium",
  assignee_type: null,
  assignee_id: null,
  creator_type: "member",
  creator_id: "user-1",
  parent_issue_id: null,
  project_id: null,
  position: 0,
  start_date: null,
  due_date: null,
  metadata: {},
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

describe("BatchDeleteIssuesResponseSchema", () => {
  it("preserves explicit per-item failures", () => {
    expect(BatchDeleteIssuesResponseSchema.parse({
      deleted: 1,
      failed: [{ issue_id: "issue-2", code: "delete_failed" }],
    })).toEqual({
      deleted: 1,
      failed: [{ issue_id: "issue-2", code: "delete_failed" }],
    });
  });

  it("rejects unknown failure codes", () => {
    expect(BatchDeleteIssuesResponseSchema.safeParse({
      deleted: 0,
      failed: [{ issue_id: "issue-1", code: "mystery" }],
    }).success).toBe(false);
  });
});

describe("BatchUpdateIssuesResponseSchema", () => {
  it("preserves blocked and failed item results", () => {
    const parsed = BatchUpdateIssuesResponseSchema.parse({
      updated: 1,
      blocked: [{
        issue_id: "issue-2",
        identifier: "MUL-2",
        title: "Parent",
        incomplete_children: [],
      }],
      blocked_reason: "child_issues_not_done",
      failed: [{ issue_id: "issue-3", code: "event_failed" }],
    });
    expect(parsed.updated).toBe(1);
    expect(parsed.blocked?.[0]?.issue_id).toBe("issue-2");
    expect(parsed.failed).toEqual([{ issue_id: "issue-3", code: "event_failed" }]);
  });
});

describe("IssueSchema (via ListIssuesResponseSchema)", () => {
  it("accepts a primitive metadata KV map", () => {
    const payload = {
      issues: [
        {
          ...baseIssue,
          metadata: { pipeline_status: "waiting", pr_number: 3, is_blocked: true },
        },
      ],
      total: 1,
    };
    const parsed = ListIssuesResponseSchema.parse(payload);
    expect(parsed.issues[0]?.metadata).toEqual({
      pipeline_status: "waiting",
      pr_number: 3,
      is_blocked: true,
    });
  });

  it("defaults metadata to {} when the server omits it (older backend)", () => {
    const { metadata: _omit, ...issueWithoutMetadata } = baseIssue;
    const payload = { issues: [issueWithoutMetadata], total: 1 };
    const parsed = ListIssuesResponseSchema.parse(payload);
    expect(parsed.issues[0]?.metadata).toEqual({});
  });

  it("rejects metadata with non-primitive values (nested object)", () => {
    const payload = {
      issues: [{ ...baseIssue, metadata: { nested: { x: 1 } } }],
      total: 1,
    };
    expect(ListIssuesResponseSchema.safeParse(payload).success).toBe(false);
  });
});

describe("TimelineEntriesSchema", () => {
  it("preserves source_task_id for agent failure comments", () => {
    const parsed = TimelineEntriesSchema.parse([
      {
        type: "comment",
        id: "comment-1",
        actor_type: "agent",
        actor_id: "agent-1",
        created_at: "2026-01-01T00:00:00Z",
        content: "API Error: 500 Internal server error",
        comment_type: "system",
        source_task_id: "task-1",
      },
    ]);

    expect(parsed[0]?.source_task_id).toBe("task-1");
  });
});

describe("UserSchema timezone contract", () => {
  const base = {
    id: "11111111-1111-1111-1111-111111111111",
    name: "Ada",
    account: "ada",
    avatar_url: null,
    onboarded_at: null,
    onboarding_questionnaire: {},
    profile_description: "",
    timezone: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };

  it("preserves an explicit IANA timezone", () => {
    const parsed = UserSchema.parse({ ...base, timezone: "Asia/Tokyo" });
    expect(parsed.timezone).toBe("Asia/Tokyo");
  });

  it("accepts an explicit null timezone", () => {
    const parsed = UserSchema.parse({ ...base, timezone: null });
    expect(parsed.timezone).toBe(null);
  });

  // Wrong-type drift: a future server bug sending `timezone` as a number
  // must not throw into the UI. parseWithFallback degrades the whole user
  // object to the explicit fallback (EMPTY_USER) so /api/me callers keep a
  // valid shape instead of white-screening.
  it("falls back to EMPTY_USER when timezone is the wrong type", () => {
    const parsed = parseWithFallback(
      { ...base, timezone: 42 },
      UserSchema,
      EMPTY_USER,
      { endpoint: "GET /api/me" },
    );
    expect(parsed).toBe(EMPTY_USER);
  });
});

describe("SquadListSchema member preview drift", () => {
  const baseSquad = {
    id: "squad-1",
    workspace_id: "ws-1",
    name: "Frontend Squad",
    description: "",
    instructions: "",
    avatar_url: null,
    leader_id: "agent-1",
    creator_id: "user-1",
    created_at: "2026-05-01T00:00:00Z",
    updated_at: "2026-05-01T00:00:00Z",
    archived_at: null,
    archived_by: null,
    sop_profile: {},
    scope: "workspace" as const,
    member_count: 0,
    member_preview: [],
  };

  it("preserves lightweight member preview rows", () => {
    const parsed = SquadListSchema.parse([
      {
        ...baseSquad,
        member_count: 2,
        member_preview: [
          { member_type: "agent", member_id: "agent-1", role: "leader" },
          { member_type: "member", member_id: "user-2", role: "member" },
        ],
      },
    ]);
    expect(parsed[0]?.member_count).toBe(2);
    expect(parsed[0]?.member_preview).toHaveLength(2);
    expect(parsed[0]?.member_preview?.[0]?.role).toBe("leader");
  });
});

describe("PromptLibraryItemSchema", () => {
  const basePrompt = {
    id: "prompt-1",
    workspace_id: "ws-1",
    project_id: null,
    name: "user-center 需求澄清提示词",
    description: "小队队长使用",
    prompt_type: "需求澄清",
    content: "请澄清目标、边界、验收条件和风险。",
    variables: [{ name: "issue_title", label: "任务标题", required: true }],
    tags: ["user-center", "小队"],
    status: "启用",
    version: 1,
    created_by: "user-1",
    created_at: "2026-06-21T00:00:00Z",
    updated_at: "2026-06-21T00:00:00Z",
  };

  it("preserves Chinese prompt library fields", () => {
    const parsed = PromptLibraryItemSchema.parse(basePrompt);
    expect(parsed.name).toBe("user-center 需求澄清提示词");
    expect(parsed.prompt_type).toBe("需求澄清");
    expect(parsed.status).toBe("启用");
    expect(parsed.variables[0]?.label).toBe("任务标题");
    expect(parsed.tags).toEqual(["user-center", "小队"]);
  });

  it("defaults list response shape", () => {
    const parsed = PromptLibraryItemListResponseSchema.parse({});
    expect(parsed.items).toEqual([]);
    expect(parsed.total).toBe(0);
  });
});

describe("PromptEvaluationAssetSchema", () => {
  it("preserves Chinese evaluation asset semantics", () => {
    const parsed = PromptEvaluationAssetSchema.parse({
      id: "asset-1",
      workspace_id: "ws-1",
      prompt_id: "prompt-1",
      name: "user-center 澄清数据集",
      description: "用于验证澄清提示词",
      asset_type: "数据集",
      payload: {
        schema_version: 1,
        schema: "multica.training_evaluation.payload.v1",
        语义版本: "multica.training_evaluation.v1",
        cases: [{ case_name: "登录失败澄清", variables: { issue_title: "登录失败" }, expected_contains: ["验收条件"] }],
        metric_contract: ["pass_rate"],
        metric_notes: ["仅统计当前快照"],
        experiment_dimensions: [{ name: "中文一致性", weight: 2 }],
        experiment_target: "current prompt",
        baseline_output: "current output",
        agent_id: "agent-1",
      },
      status: "启用",
      created_by: "user-1",
      created_at: "2026-06-21T00:00:00Z",
      updated_at: "2026-06-21T00:00:00Z",
    });

    expect(parsed.asset_type).toBe("数据集");
    expect(parsed.payload).toMatchObject({ cases: [{ case_name: "登录失败澄清" }] });
    expect(parsed.payload).toMatchObject({
      metric_contract: ["pass_rate"],
      metric_notes: ["仅统计当前快照"],
      experiment_dimensions: [{ name: "中文一致性", weight: 2 }],
      experiment_target: "current prompt",
      baseline_output: "current output",
      agent_id: "agent-1",
    });
  });

  it("rejects invalid strict training evaluation payloads", () => {
    expect(
      PromptEvaluationAssetSchema.safeParse({
        id: "asset-invalid",
        workspace_id: "ws-1",
        name: "坏数据集",
        asset_type: "数据集",
        payload: {
          schema_version: 1,
          schema: "multica.training_evaluation.payload.v1",
          cases: [{ variables: { issue_title: "登录失败" }, expected_contains: ["验收条件"] }],
        },
        created_at: "2026-06-21T00:00:00Z",
        updated_at: "2026-06-21T00:00:00Z",
      }).success,
    ).toBe(false);
  });

  it("rejects malformed current metric and experiment contracts", () => {
    const base = {
      id: "asset-invalid-contract",
      workspace_id: "ws-1",
      name: "坏维度契约",
      asset_type: "测试套件",
      payload: {
        schema_version: 1,
        schema: "multica.training_evaluation.payload.v1",
        cases: [],
      },
      created_at: "2026-06-21T00:00:00Z",
      updated_at: "2026-06-21T00:00:00Z",
    };
    expect(PromptEvaluationAssetSchema.safeParse({
      ...base,
      payload: { ...base.payload, metric_notes: "not-an-array" },
    }).success).toBe(false);
    expect(PromptEvaluationAssetSchema.safeParse({
      ...base,
      payload: { ...base.payload, experiment_dimensions: [{ weight: 2 }] },
    }).success).toBe(false);
    expect(PromptEvaluationAssetSchema.safeParse({
      ...base,
      payload: { ...base.payload, baseline_output: { text: "not-a-string" } },
    }).success).toBe(false);
    expect(PromptEvaluationAssetSchema.safeParse({
      ...base,
      payload: { ...base.payload, agent_id: "" },
    }).success).toBe(false);
  });

  it("defaults evaluation asset list response shape", () => {
    const parsed = PromptEvaluationAssetListResponseSchema.parse({});
    expect(parsed.items).toEqual([]);
    expect(parsed.total).toBe(0);
  });

  it("rejects removed optimization run assets", () => {
    expect(() => PromptEvaluationAssetSchema.parse({
      id: "asset-2",
      workspace_id: "ws-1",
      prompt_id: "prompt-1",
      name: "澄清提示词优化运行",
      asset_type: "优化运行",
      payload: { 候选版本数: 3, 指标: ["完整性", "可执行性"] },
      status: "启用",
      created_at: "2026-06-21T00:00:00Z",
      updated_at: "2026-06-21T00:00:00Z",
    })).toThrow();
  });
});

// The workspace dashboard and runtime-detail pages were re-pointed at the
// unified `task_usage_hourly` rollup. Every numeric field drives chart /
// KPI math, and string keys (date / agent_id / model) bucket the series.
// The contract these schemas must hold: a row missing a field degrades
// that field to a sane default rather than dropping the WHOLE array to
// the `[]` fallback — one drifted row must not blank the entire chart.
describe("dashboard + runtime usage schema drift", () => {
  it("coerces a missing numeric field to 0 instead of dropping the array", () => {
    const parsed = DashboardUsageDailyListSchema.parse([
      { date: "2026-05-19", model: "claude-opus-4-7", input_tokens: 100 },
    ]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.output_tokens).toBe(0);
    expect(parsed[0]?.cache_read_tokens).toBe(0);
    expect(parsed[0]?.cache_write_tokens).toBe(0);
  });

  it("coerces a missing date key to \"\" so the rest of the series survives", () => {
    const parsed = DashboardUsageDailyListSchema.parse([
      { model: "claude-opus-4-7", input_tokens: 5 },
    ]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.date).toBe("");
  });

  it("coerces a missing agent_id key to \"\" for the agent-runtime panel", () => {
    const parsed = DashboardAgentRunTimeListSchema.parse([
      { total_seconds: 42, task_count: 3, failed_count: 0 },
    ]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.agent_id).toBe("");
  });

  it("coerces a missing agent_id key to \"\" for the usage-by-agent panel", () => {
    const parsed = DashboardUsageByAgentListSchema.parse([
      { model: "claude-opus-4-7", input_tokens: 7 },
    ]);
    expect(parsed[0]?.agent_id).toBe("");
  });

  it("coerces missing fields on every runtime usage schema", () => {
    expect(RuntimeUsageListSchema.parse([{ date: "2026-05-19" }])[0]?.input_tokens).toBe(0);
    expect(RuntimeUsageByAgentListSchema.parse([{ model: "x" }])[0]?.agent_id).toBe("");
    expect(RuntimeUsageByTaskListSchema.parse([{ model: "x" }])[0]?.task_id).toBe("");
  });

  it("defaults a missing provider to \"\" so an older server's rows still price by bare model", () => {
    // provider was added for cross-provider model disambiguation; a server
    // predating it omits the field. The schema must fill "" (→ bare-model
    // pricing lookup) rather than drop the row.
    expect(
      DashboardUsageDailyListSchema.parse([{ date: "2026-05-19", model: "claude-opus-4-7" }])[0]
        ?.provider,
    ).toBe("");
    expect(
      DashboardUsageByAgentListSchema.parse([{ model: "claude-opus-4-7" }])[0]?.provider,
    ).toBe("");
    expect(RuntimeUsageByAgentListSchema.parse([{ model: "x" }])[0]?.provider).toBe("");
    expect(RuntimeUsageByTaskListSchema.parse([{ model: "x" }])[0]?.provider).toBe("");
  });

  it("rejects a non-array body so parseWithFallback can return its fallback", () => {
    expect(DashboardUsageDailyListSchema.safeParse(null).success).toBe(false);
    expect(RuntimeUsageListSchema.safeParse({ rows: [] }).success).toBe(false);
  });

  it("keeps unknown server-side fields via .loose()", () => {
    const parsed = RuntimeUsageListSchema.parse([
      { date: "2026-05-19", region: "us-east" },
    ]);
    expect((parsed[0] as Record<string, unknown>).region).toBe("us-east");
  });
});

describe("ObservabilitySummarySchema full-summary defaults", () => {
  it("defaults missing completeness fields to full-summary semantics", () => {
    const parsed = ObservabilitySummarySchema.parse({});
    expect(parsed.summary_completeness["采样上限"]).toBe(0);
    expect(parsed.summary_completeness["说明"]).toContain("全量汇总");
    expect(parsed.sop_run_maybe_truncated).toBe(false);
    expect(parsed.task_trace_maybe_truncated).toBe(false);
  });
});

describe("AppConfigSchema current contract", () => {
  it("keeps cdn_signed=true from a signing-enabled server", () => {
    const parsed = AppConfigSchema.parse({
      cdn_domain: "cdn.example.com",
      cdn_signed: true,
      allow_signup: true,
      posthog_key: "",
      posthog_host: "",
      analytics_environment: "test",
      workspace_creation_disabled: false,
    });
    expect(parsed.cdn_signed).toBe(true);
  });
});
