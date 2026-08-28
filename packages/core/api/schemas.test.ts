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
  it("preserves the failed issue identities used for cache reconciliation", () => {
    expect(BatchDeleteIssuesResponseSchema.parse({
      deleted: 1,
      failed: [{ issue_id: "issue-2", code: "delete_failed" }],
    })).toEqual({
      deleted: 1,
      failed: [{ issue_id: "issue-2", code: "delete_failed" }],
    });
  });

  it("rejects failures without an issue identity", () => {
    expect(BatchDeleteIssuesResponseSchema.safeParse({
      deleted: 0,
      failed: [{ code: "delete_failed" }],
    }).success).toBe(false);
  });
});

describe("BatchUpdateIssuesResponseSchema", () => {
  it("preserves the result counts used by the batch toolbar", () => {
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
    expect(parsed.blocked).toHaveLength(1);
    expect(parsed.failed).toHaveLength(1);
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
    profile_description: "",
    timezone: null,
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
    expect(parsed[0]?.member_preview?.[0]).toEqual({
      member_type: "agent",
      member_id: "agent-1",
    });
  });
});


describe("dashboard + runtime usage schema drift", () => {
  it("coerces a missing numeric field to 0 instead of dropping the array", () => {
    const parsed = DashboardUsageDailyListSchema.parse([
      { date: "2026-05-19", input_tokens: 100 },
    ]);
    expect(parsed).toHaveLength(1);
    expect(parsed[0]?.output_tokens).toBe(0);
    expect(parsed[0]?.cache_read_tokens).toBe(0);
    expect(parsed[0]?.cache_write_tokens).toBe(0);
  });

  it("coerces a missing date key to \"\" so the rest of the series survives", () => {
    const parsed = DashboardUsageDailyListSchema.parse([
      { input_tokens: 5 },
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
      { provider: "anthropic", input_tokens: 7 },
    ]);
    expect(parsed[0]?.agent_id).toBe("");
  });

  it("coerces missing fields on every runtime usage schema", () => {
    expect(RuntimeUsageListSchema.parse([{ date: "2026-05-19" }])[0]?.input_tokens).toBe(0);
    expect(RuntimeUsageByAgentListSchema.parse([{}])[0]?.agent_id).toBe("");
    expect(RuntimeUsageByTaskListSchema.parse([{}])[0]?.task_id).toBe("");
  });

  it("defaults a missing provider on attribution rows", () => {
    expect(
      DashboardUsageByAgentListSchema.parse([{}])[0]?.provider,
    ).toBe("");
    expect(RuntimeUsageByAgentListSchema.parse([{}])[0]?.provider).toBe("");
    expect(RuntimeUsageByTaskListSchema.parse([{}])[0]?.provider).toBe("");
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

describe("AppConfigSchema current contract", () => {
  it("keeps cdn_signed=true from a signing-enabled server", () => {
    const parsed = AppConfigSchema.parse({
      cdn_domain: "cdn.example.com",
      cdn_signed: true,
      posthog_key: "",
      posthog_host: "",
      analytics_environment: "test",
      workspace_creation_disabled: false,
    });
    expect(parsed.cdn_signed).toBe(true);
  });
});
