/**
 * TestApiClient — lightweight API helper for E2E test data setup/teardown.
 *
 * Uses raw fetch so E2E tests have zero build-time coupling to the web app.
 */

import "./env";
import pg from "pg";

// `||` (not `??`) so an empty `NEXT_PUBLIC_API_URL=` in .env still falls
// back to localhost. dotenv sets unset-vs-empty both as "" — treating them
// the same matches user intent.
const API_BASE = process.env.NEXT_PUBLIC_API_URL || `http://localhost:${process.env.PORT || "8080"}`;
const DATABASE_URL = process.env.DATABASE_URL ?? "postgres://multica:multica@localhost:5432/multica?sslmode=disable";

interface TestWorkspace {
  id: string;
  name: string;
  slug: string;
  repos?: Array<Record<string, unknown>>;
}

interface TestWorkspaceMember {
  id: string;
  user_id: string;
  account: string;
  name: string;
  role: string;
}


interface DaemonRuntimeResponse {
  id: string;
  workspace_id: string;
  provider: string;
  name: string;
  status: string;
}

interface AgentResponse {
  id: string;
  workspace_id: string;
  runtime_id: string;
  name: string;
  model: string;
  status: string;
}

interface DaemonClaimResponse {
  task: null | {
    id: string;
    runtime_id: string;
    status: string;
  };
}


interface InternalSquadTemplateAgent {
  id: string;
  name: string;
  role_key: string;
  role: string;
}

interface InternalSquadTemplateResponse {
  squad: {
    id: string;
    name: string;
    leader_id: string;
    sop_profile: Record<string, unknown>;
    member_count: number;
  };
  agents: InternalSquadTemplateAgent[];
}

interface SquadLeaderTask {
  id: string;
  status: string;
  agent_id: string;
  runtime_id: string;
  issue_id: string;
  is_leader_task: boolean;
  error?: string | null;
}

interface TaskExecutionEvidence {
  usage: Array<Record<string, unknown>>;
  messages: Array<Record<string, unknown>>;
  trace_events: Array<Record<string, unknown>>;
}


interface IssueSystemComment {
  id: string;
  content: string;
  author_type: string;
  parent_id: string | null;
  type: string;
  created_at: string;
}

interface SquadSOPRun {
  id: string;
  profile_key: string;
  status: string;
  current_step_key: string;
  leader_task_id: string | null;
  total_duration_ms: number | null;
  profile: Record<string, unknown>;
  metrics: Record<string, unknown>;
  events: Array<{
    id: string;
    step_key: string;
    step_name: string;
    role_key: string;
    event_type: string;
    status: string;
    reason: string;
    evidence: Record<string, unknown>;
    created_by_type: string;
    task_id: string | null;
  }>;
}

interface ObservabilitySummary {
  指标: Record<string, unknown>;
  task_trace_total: number;
  model_breakdown: Array<Record<string, unknown>>;
  runtime_breakdown: Array<Record<string, unknown>>;
}

interface InternalSquadTemplateStats {
  squad_count: number;
  agent_count: number;
  member_count: number;
}

export class TestApiClient {
  private token: string | null = null;
  private workspaceSlug: string | null = null;
  private workspaceId: string | null = null;
  private account: string | null = null;
  private userId: string | null = null;
  private createdIssueIds: string[] = [];
  private createdProjectIds: string[] = [];
  private createdRuntimeIds: string[] = [];
  private createdSquadIds: string[] = [];
  private createdMemberIds: Array<{ workspaceId: string; memberId: string }> = [];

  useAuthenticatedSession(session: { token: string; account: string; userId: string | null }) {
    this.token = session.token;
    this.account = session.account;
    this.userId = session.userId;
  }

  async login(account: string, name: string, password: string) {
    const res = await fetch(`${API_BASE}/auth/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ account, password }),
    });
    if (!res.ok) {
      throw new Error(`login failed: ${res.status} ${await res.text()}`);
    }
    const data = await res.json();

    this.token = data.token;
    this.account = account;
    this.userId = data.user?.id ?? null;

    if (name && data.user?.name !== name) {
      await this.authedFetch("/api/me", {
        method: "PATCH",
        body: JSON.stringify({ name }),
      });
    }

    return data;
  }

  getUserId(): string | null {
    return this.userId;
  }

  async getWorkspaces(): Promise<TestWorkspace[]> {
    const res = await this.authedFetch("/api/workspaces");
    return res.json();
  }

  async deleteWorkspace(id: string): Promise<void> {
    const res = await this.authedFetch(`/api/workspaces/${id}`, { method: "DELETE" });
    if (!res.ok) {
      throw new Error(`delete workspace failed: ${res.status} ${await res.text()}`);
    }
    if (this.workspaceId === id) {
      this.workspaceId = null;
      this.workspaceSlug = null;
    }
  }

  setWorkspaceId(id: string) {
    this.workspaceId = id;
  }

  setWorkspaceSlug(slug: string) {
    this.workspaceSlug = slug;
  }

  async ensureWorkspace(name = "E2E Workspace", slug = "e2e-workspace") {
    const workspaces = await this.getWorkspaces();
    const workspace = workspaces.find((item) => item.slug === slug);
    if (workspace) {
      this.workspaceId = workspace.id;
      this.workspaceSlug = workspace.slug;
      return workspace;
    }

    const res = await this.authedFetch("/api/workspaces", {
      method: "POST",
      headers: { "Idempotency-Key": crypto.randomUUID() },
      body: JSON.stringify({ name, slug }),
    });
    if (res.ok) {
      const created = (await res.json()) as TestWorkspace;
      this.workspaceId = created.id;
      this.workspaceSlug = created.slug;
      return created;
    }

    const refreshed = await this.getWorkspaces();
    const created = refreshed.find((item) => item.slug === slug);
    if (created) {
      this.workspaceId = created.id;
      this.workspaceSlug = created.slug;
      return created;
    }

    throw new Error(`Failed to ensure workspace ${slug}: ${res.status} ${res.statusText}`);
  }

  async listExternalCredentialProfiles(provider?: "gongfeng" | "tapd"): Promise<Array<{
    id: string;
    provider: string;
    name: string;
    secret_binding?: { configured?: boolean; hint?: string; mode?: string };
  }>> {
    const query = provider ? `?provider=${encodeURIComponent(provider)}` : "";
    const res = await this.authedFetch(`/api/external-credential-profiles${query}`);
    if (!res.ok) {
      throw new Error(`list external credential profiles failed: ${res.status} ${await res.text()}`);
    }
    const body = await res.json();
    return body.profiles ?? [];
  }

  async deleteExternalCredentialProfile(id: string): Promise<void> {
    const res = await this.authedFetch(`/api/external-credential-profiles/${id}`, {
      method: "DELETE",
    });
    if (!res.ok) {
      throw new Error(`delete external credential profile failed: ${res.status} ${await res.text()}`);
    }
  }



  async createAgent(data: {
    name: string;
    runtime_id: string;
    description?: string;
    instructions?: string;
    model?: string;
    scope?: "workspace" | "personal";
    max_concurrent_tasks?: number;
  }): Promise<AgentResponse> {
    const res = await this.authedFetch("/api/agents", {
      method: "POST",
      headers: { "Idempotency-Key": crypto.randomUUID() },
      body: JSON.stringify({
        name: data.name,
        description: data.description ?? "E2E 创建的执行智能体",
        instructions: data.instructions ?? "请只输出中文结论和可验收证据。",
        runtime_id: data.runtime_id,
        runtime_config: {},
        custom_env: {},
        custom_args: [],
        scope: data.scope ?? "personal",
        max_concurrent_tasks: data.max_concurrent_tasks ?? 1,
        model: data.model ?? "deepseek-v4-pro-ioa",
      }),
    });
    if (!res.ok) {
      throw new Error(`Failed to create agent: ${res.status} ${await res.text()}`);
    }
    return res.json();
  }

  async registerDaemonCodeBuddyRuntime(name = `E2E Daemon CodeBuddy Runtime ${Date.now()}`) {
    if (!this.workspaceId) {
      throw new Error("Cannot register daemon runtime before workspace is selected");
    }
    const daemonId = `e2e-daemon-${Date.now()}-${Math.random().toString(36).slice(2)}`;
    const res = await this.authedFetch("/api/daemon/register", {
      method: "POST",
      body: JSON.stringify({
        workspace_id: this.workspaceId,
        daemon_id: daemonId,
        device_name: "E2E fake daemon",
        cli_version: "e2e",
        runtimes: [{ name, type: "codebuddy", version: "e2e", status: "online" }],
      }),
    });
    if (!res.ok) {
      throw new Error(`Failed to register daemon runtime: ${res.status} ${await res.text()}`);
    }
    const data = (await res.json()) as { runtimes?: DaemonRuntimeResponse[] } | DaemonRuntimeResponse[];
    const runtimes = Array.isArray(data) ? data : (data.runtimes ?? []);
    const runtime = runtimes[0];
    if (!runtime?.id) {
      throw new Error(`Daemon register returned no runtime: ${JSON.stringify(runtimes)}`);
    }
    this.createdRuntimeIds.push(runtime.id);
    return { daemonId, runtime };
  }

  async completeNextDaemonModelList(
    runtimeId: string,
    models = [
      { id: "deepseek-v4-pro-ioa", label: "DeepSeek V4 Pro", provider: "deepseek", default: true },
      { id: "glm-5.2-ioa", label: "GLM 5.2", provider: "zhipu", default: false },
    ],
  ) {
    for (let attempt = 0; attempt < 40; attempt += 1) {
      const heartbeat = await this.authedFetch("/api/daemon/heartbeat", {
        method: "POST",
        body: JSON.stringify({ runtime_id: runtimeId }),
      });
      if (!heartbeat.ok) {
        throw new Error(`daemon heartbeat failed: ${heartbeat.status} ${await heartbeat.text()}`);
      }
      const ack = (await heartbeat.json()) as { pending_model_list?: { id: string } };
      const requestId = ack.pending_model_list?.id;
      if (requestId) {
        const result = await this.authedFetch(
          `/api/daemon/runtimes/${runtimeId}/models/${requestId}/result`,
          {
            method: "POST",
            body: JSON.stringify({ status: "completed", supported: true, models }),
          },
        );
        if (!result.ok) {
          throw new Error(`report model list failed: ${result.status} ${await result.text()}`);
        }
        return;
      }
      await new Promise((resolve) => setTimeout(resolve, 250));
    }
    throw new Error(`runtime ${runtimeId} received no model-list request`);
  }

  async claimDaemonTask(runtimeId: string) {
    const res = await this.authedFetch(`/api/daemon/runtimes/${runtimeId}/tasks/claim`, {
      method: "POST",
      body: JSON.stringify({}),
    });
    if (!res.ok) {
      throw new Error(`Failed to claim daemon task: ${res.status} ${await res.text()}`);
    }
    return (await res.json()) as DaemonClaimResponse;
  }

  async startDaemonTask(taskId: string) {
    const res = await this.authedFetch(`/api/daemon/tasks/${taskId}/start`, {
      method: "POST",
      body: JSON.stringify({}),
    });
    if (!res.ok) {
      throw new Error(`Failed to start daemon task: ${res.status} ${await res.text()}`);
    }
  }

  async reportDaemonTaskUsage(taskId: string, usage: { provider?: string; model: string; input_tokens: number; output_tokens: number; cache_read_tokens?: number; cache_write_tokens?: number }) {
    const res = await this.authedFetch(`/api/daemon/tasks/${taskId}/usage`, {
      method: "POST",
      body: JSON.stringify({
        usage: [
          {
            provider: usage.provider ?? "codebuddy",
            model: usage.model,
            input_tokens: usage.input_tokens,
            output_tokens: usage.output_tokens,
            cache_read_tokens: usage.cache_read_tokens ?? 0,
            cache_write_tokens: usage.cache_write_tokens ?? 0,
          },
        ],
      }),
    });
    if (!res.ok) {
      throw new Error(`Failed to report daemon task usage: ${res.status} ${await res.text()}`);
    }
  }

  async reportDaemonTaskMessages(taskId: string, content: string | Array<Record<string, unknown>>) {
    const messages = Array.isArray(content) ? content : [{ seq: 1, type: "text", content }];
    const res = await this.authedFetch(`/api/daemon/tasks/${taskId}/messages`, {
      method: "POST",
      body: JSON.stringify({ messages }),
    });
    if (!res.ok) {
      throw new Error(`Failed to report daemon task messages: ${res.status} ${await res.text()}`);
    }
  }

  async completeDaemonTask(taskId: string, output: string) {
    const res = await this.authedFetch(`/api/daemon/tasks/${taskId}/complete`, {
      method: "POST",
      body: JSON.stringify({
        output,
        session_id: `e2e-session-${taskId}`,
        work_dir: `/tmp/multica-e2e/${taskId}`,
      }),
    });
    if (!res.ok) {
      throw new Error(`Failed to complete daemon task: ${res.status} ${await res.text()}`);
    }
  }

  async createIssue(title: string, opts?: Record<string, unknown>) {
    const res = await this.authedFetch("/api/issues", {
      method: "POST",
      headers: { "Idempotency-Key": crypto.randomUUID() },
      body: JSON.stringify({ title, ...opts }),
    });
    if (!res.ok) {
      throw new Error(`create issue failed: ${res.status} ${await res.text()}`);
    }
    const issue = await res.json();
    this.createdIssueIds.push(issue.id);
    return issue;
  }

  rememberIssue(id: string) {
    if (id && !this.createdIssueIds.includes(id)) {
      this.createdIssueIds.push(id);
    }
  }

  async getIssue(issueId: string) {
    const res = await this.authedFetch(`/api/issues/${issueId}`);
    if (!res.ok) {
      throw new Error(`get issue failed: ${res.status} ${await res.text()}`);
    }
    return res.json();
  }


  async createProject(title: string, opts?: Record<string, unknown>) {
    const res = await this.authedFetch("/api/projects", {
      method: "POST",
      headers: { "Idempotency-Key": crypto.randomUUID() },
      body: JSON.stringify({ title, ...opts }),
    });
    if (!res.ok) {
      throw new Error(`create project failed: ${res.status} ${await res.text()}`);
    }
    const project = await res.json();
    this.createdProjectIds.push(project.id);
    return project;
  }

  async updateWorkspace(id: string, data: Record<string, unknown>): Promise<TestWorkspace> {
    const res = await this.authedFetch(`/api/workspaces/${id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
    if (!res.ok) {
      throw new Error(`update workspace failed: ${res.status} ${await res.text()}`);
    }
    return res.json();
  }

  async listWorkspaceMembers(workspaceId: string): Promise<TestWorkspaceMember[]> {
    const res = await this.authedFetch(`/api/workspaces/${workspaceId}/members`);
    if (!res.ok) {
      throw new Error(`list workspace members failed: ${res.status} ${await res.text()}`);
    }
    return res.json();
  }

  async createWorkspaceMember(
    workspaceId: string,
    data: { account: string; name?: string; password?: string; role?: string },
  ): Promise<TestWorkspaceMember> {
    const res = await this.authedFetch(`/api/workspaces/${workspaceId}/members`, {
      method: "POST",
      headers: { "Idempotency-Key": crypto.randomUUID() },
      body: JSON.stringify({
        account: data.account,
        name: data.name ?? data.account,
        password: data.password,
        role: data.role ?? "member",
      }),
    });
    if (!res.ok) {
      throw new Error(`create workspace member failed: ${res.status} ${await res.text()}`);
    }
    const member = (await res.json()) as TestWorkspaceMember;
    this.createdMemberIds.push({ workspaceId, memberId: member.id });
    return member;
  }

  async deleteWorkspaceMember(workspaceId: string, memberId: string): Promise<void> {
    const res = await this.authedFetch(`/api/workspaces/${workspaceId}/members/${memberId}`, {
      method: "DELETE",
    });
    if (!res.ok && res.status !== 404) {
      throw new Error(`delete workspace member failed: ${res.status} ${await res.text()}`);
    }
  }

  async cleanupWorkspaceMemberFixture(account: string, requestKey?: string): Promise<void> {
    const client = new pg.Client(DATABASE_URL);
    await client.connect();
    try {
      if (requestKey) {
        await client.query(
          `DELETE FROM resource_create_request WHERE resource_type='workspace_member' AND idempotency_key=$1`,
          [requestKey],
        );
      }
      await client.query(`DELETE FROM "user" WHERE account=$1`, [account]);
    } finally {
      await client.end();
    }
  }

  async getProject(projectId: string) {
    const res = await this.authedFetch(`/api/projects/${projectId}`);
    if (!res.ok) {
      throw new Error(`get project failed: ${res.status} ${await res.text()}`);
    }
    return res.json();
  }

  async listInbox(): Promise<
    Array<{
      id: string;
      title: string;
      read: boolean;
      archived: boolean;
      issue_id?: string | null;
      type?: string;
    }>
  > {
    const res = await this.authedFetch("/api/inbox");
    if (!res.ok) {
      throw new Error(`list inbox failed: ${res.status} ${await res.text()}`);
    }
    const data = await res.json();
    return Array.isArray(data) ? data : [];
  }

  async deleteProject(id: string) {
    await this.authedFetch(`/api/projects/${id}`, { method: "DELETE" });
  }

  async updateIssue(issueId: string, opts?: Record<string, unknown>) {
    const res = await this.authedFetch(`/api/issues/${issueId}`, {
      method: "PUT",
      body: JSON.stringify(opts ?? {}),
    });
    if (!res.ok) {
      throw new Error(`update issue failed: ${res.status} ${await res.text()}`);
    }
    return res.json();
  }

  async ensureInternalSquadTemplate(
    templateKey: "user-center" | "multica-coding",
    options: {
      name?: string;
      runtime_provider?: string;
      model?: string;
      scope?: "workspace" | "personal";
    } = {},
  ): Promise<InternalSquadTemplateResponse> {
    const res = await this.authedFetch("/api/squads/internal-template", {
      method: "POST",
      body: JSON.stringify({ template_key: templateKey, ...options }),
    });
    if (!res.ok) {
      throw new Error(`ensure internal squad template failed: ${res.status} ${await res.text()}`);
    }
    const data = (await res.json()) as InternalSquadTemplateResponse;
    if (data?.squad?.id) {
      this.createdSquadIds.push(data.squad.id);
    }
    return data;
  }

  rememberSquad(id: string) {
    if (id && !this.createdSquadIds.includes(id)) {
      this.createdSquadIds.push(id);
    }
  }

  async getInternalSquadTemplateStats(
    templateName: string,
    agentNamePrefix: string,
  ): Promise<InternalSquadTemplateStats> {
    if (!this.workspaceId) {
      throw new Error("Cannot inspect internal squad template before workspace is selected");
    }
    const client = new pg.Client(DATABASE_URL);
    await client.connect();
    try {
      const result = await client.query<InternalSquadTemplateStats>(
        `
          WITH target_squads AS (
            SELECT id FROM squad WHERE workspace_id = $1 AND name = $2 AND archived_at IS NULL
          ),
          target_agents AS (
            SELECT id FROM agent
            WHERE workspace_id = $1 AND name LIKE ($3 || ' · %') AND archived_at IS NULL
          )
          SELECT
            (SELECT count(*)::int FROM target_squads) AS squad_count,
            (SELECT count(*)::int FROM target_agents) AS agent_count,
            (
              SELECT count(*)::int
              FROM squad_member sm
              JOIN target_squads s ON s.id = sm.squad_id
            ) AS member_count
        `,
        [this.workspaceId, templateName, agentNamePrefix],
      );
      return result.rows[0] ?? { squad_count: 0, agent_count: 0, member_count: 0 };
    } finally {
      await client.end();
    }
  }

  async deleteIssue(id: string) {
    await this.authedFetch(`/api/issues/${id}`, { method: "DELETE" });
  }

  async listIssueSOPRuns(issueId: string): Promise<{ items: SquadSOPRun[]; total: number }> {
    const res = await this.authedFetch(`/api/issues/${issueId}/sop-runs`);
    if (!res.ok) {
      throw new Error(`list issue SOP runs failed: ${res.status} ${await res.text()}`);
    }
    return res.json();
  }

  async getWorkspaceObservabilitySummary(params?: { squad_id?: string; project_id?: string }): Promise<ObservabilitySummary> {
    if (!this.workspaceId) {
      throw new Error("Cannot get observability summary before workspace is selected");
    }
    const search = new URLSearchParams();
    if (params?.squad_id) search.set("squad_id", params.squad_id);
    if (params?.project_id) search.set("project_id", params.project_id);
    const res = await this.authedFetch(
      `/api/workspaces/${this.workspaceId}/observability/summary${search.toString() ? `?${search}` : ""}`,
    );
    if (!res.ok) {
      throw new Error(`get workspace observability summary failed: ${res.status} ${await res.text()}`);
    }
    return res.json();
  }

  async findLeaderTask(issueId: string, leaderAgentId: string): Promise<SquadLeaderTask | null> {
    const client = new pg.Client(DATABASE_URL);
    await client.connect();
    try {
      const result = await client.query<SquadLeaderTask>(
        `
          SELECT
            id::text,
            status,
            agent_id::text,
            runtime_id::text,
            issue_id::text,
            COALESCE(is_leader_task, false) AS is_leader_task,
            error
          FROM agent_task_queue
          WHERE issue_id = $1 AND agent_id = $2
          ORDER BY created_at DESC
          LIMIT 1
        `,
        [issueId, leaderAgentId],
      );
      return result.rows[0] ?? null;
    } finally {
      await client.end();
    }
  }

  async getTaskExecutionEvidence(taskId: string): Promise<TaskExecutionEvidence> {
    const client = new pg.Client(DATABASE_URL);
    await client.connect();
    try {
      const usage = await client.query<Record<string, unknown>>(
        `
          SELECT provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, created_at, updated_at
          FROM task_usage
          WHERE task_id = $1
          ORDER BY created_at ASC
        `,
        [taskId],
      );
      const messages = await client.query<Record<string, unknown>>(
        `
          SELECT seq, type, tool, content, input, output, created_at
          FROM task_message
          WHERE task_id = $1
          ORDER BY seq ASC
        `,
        [taskId],
      );
      const traceEvents = await client.query<Record<string, unknown>>(
        `
          SELECT source, event_type, event_name, status, provider, model, input_tokens, output_tokens, failure_reason, created_at
          FROM task_trace_event
          WHERE task_id = $1
          ORDER BY created_at ASC
        `,
        [taskId],
      );
      return {
        usage: usage.rows,
        messages: messages.rows,
        trace_events: traceEvents.rows,
      };
    } finally {
      await client.end();
    }
  }

  async getLatestSystemComment(issueId: string): Promise<IssueSystemComment | null> {
    const client = new pg.Client(DATABASE_URL);
    await client.connect();
    try {
      const result = await client.query<IssueSystemComment>(
        `
          SELECT
            id::text,
            content,
            author_type,
            parent_id::text,
            type,
            created_at
          FROM comment
          WHERE issue_id = $1 AND author_type = 'system'
          ORDER BY created_at DESC
          LIMIT 1
        `,
        [issueId],
      );
      return result.rows[0] ?? null;
    } finally {
      await client.end();
    }
  }

  async completeSquadLeaderTaskViaDaemon(task: SquadLeaderTask, output: string) {
    if (task.status === "queued") {
      const claimed = await this.claimDaemonTask(task.runtime_id);
      if (claimed.task?.id && claimed.task.id !== task.id) {
        throw new Error(`claimed unexpected task ${claimed.task.id}; expected ${task.id}`);
      }
    }
    await this.startDaemonTask(task.id);
    await this.reportDaemonTaskUsage(task.id, {
      provider: "codebuddy",
      model: "deepseek-v4-pro-ioa",
      input_tokens: 36,
      output_tokens: 19,
      cache_read_tokens: 5,
      cache_write_tokens: 7,
    });
    await this.reportDaemonTaskMessages(task.id, output);
    await this.completeDaemonTask(task.id, output);
  }


  async cleanupSeededRuntimes() {
    if (!this.workspaceId || this.createdRuntimeIds.length === 0) {
      this.createdRuntimeIds = [];
      return;
    }

    const client = new pg.Client(DATABASE_URL);
    await client.connect();
    try {
      for (const runtimeId of this.createdRuntimeIds) {
        await client.query("BEGIN");
        try {
          await client.query(
            `
              DELETE FROM agent
              WHERE runtime_id = $1
            `,
            [runtimeId],
          );
          await client.query(`DELETE FROM agent_runtime WHERE id = $1`, [runtimeId]);
          await client.query("COMMIT");
        } catch (error) {
          await client.query("ROLLBACK");
          throw error;
        }
      }
    } finally {
      await client.end();
      this.createdRuntimeIds = [];
    }
  }

  async cleanupSeededSquads() {
    if (!this.createdSquadIds.length) {
      return;
    }
    const client = new pg.Client(DATABASE_URL);
    await client.connect();
    try {
      for (const squadId of this.createdSquadIds) {
        await client.query(`DELETE FROM squad WHERE id = $1`, [squadId]);
      }
    } finally {
      await client.end();
      this.createdSquadIds = [];
    }
  }

  /** Clean up all issues created during this test. */
  async cleanup() {
    for (const id of this.createdIssueIds) {
      try {
        await this.deleteIssue(id);
      } catch {
        /* ignore — may already be deleted */
      }
    }
    this.createdIssueIds = [];
    for (const id of this.createdProjectIds) {
      try {
        await this.deleteProject(id);
      } catch {
        /* ignore — may already be deleted */
      }
    }
    this.createdProjectIds = [];
    for (const { workspaceId, memberId } of this.createdMemberIds) {
      try {
        await this.deleteWorkspaceMember(workspaceId, memberId);
      } catch {
        /* ignore — may already be deleted */
      }
    }
    this.createdMemberIds = [];
    await this.cleanupSeededSquads();
    await this.cleanupSeededRuntimes();
  }

  getToken() {
    return this.token;
  }

  private async authedFetch(path: string, init?: RequestInit) {
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      ...((init?.headers as Record<string, string>) ?? {}),
    };
    if (this.token) headers["Authorization"] = `Bearer ${this.token}`;
    if (this.workspaceSlug) headers["X-Workspace-Slug"] = this.workspaceSlug;
    else if (this.workspaceId) headers["X-Workspace-ID"] = this.workspaceId;
    return fetch(`${API_BASE}${path}`, { ...init, headers });
  }

  private async retryOnConnectionLoss<T>(attempt: () => Promise<T>): Promise<T> {
    try {
      return await attempt();
    } catch (error) {
      if (!(error instanceof TypeError)) throw error;
      return attempt();
    }
  }

  private e2eName(prefix: string) {
    return `${prefix} ${Date.now()} ${Math.random().toString(36).slice(2, 8)}`;
  }
}
