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

interface SquadLeaderTask {
  id: string;
  status: string;
  agent_id: string;
  runtime_id: string;
  issue_id: string;
  is_leader_task: boolean;
  error?: string | null;
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
