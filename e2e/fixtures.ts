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
}

interface PromptLibraryItem {
  id: string;
  name: string;
  prompt_type: string;
  content?: string;
  version?: number;
}

interface PromptEvaluationAsset {
  id: string;
  prompt_id: string | null;
  name: string;
  asset_type: string;
  status: string;
  payload: Record<string, unknown>;
}

interface PromptEvaluationRun {
  id: string;
  workspace_id: string;
  asset_id: string;
  run_kind: string;
  status: string;
  agent_id: string | null;
  runtime_id: string | null;
  task_id: string | null;
  chat_session_id: string | null;
  model: string;
  runtime_provider: string;
  total_cases: number;
  passed_cases: number;
  failed_cases: number;
  pass_rate: number;
  input_tokens: number;
  output_tokens: number;
  estimated_cost: number;
  conclusion: string;
}

interface PromptEvaluationRunEvidence {
  run: PromptEvaluationRun;
  trials: Array<{
    id: string;
    case_name: string;
    status: string;
    rendered_prompt: string;
  }>;
  task_usage: unknown[];
  task_messages: unknown[];
  trace_events: unknown[];
  evidence: Record<string, unknown>;
}

interface PromptEvaluationCase {
  id: string;
  asset_id: string;
  case_name: string;
  case_index: number;
  variables: Record<string, unknown>;
  expected_contains: unknown[];
  status: string;
}

interface PromptEvaluationOptimizationCandidate {
  id: string;
  run_id: string;
  prompt_id: string;
  candidate_name: string;
  candidate_content: string;
  failed_case_count: number;
  status: string;
  published_prompt_id: string | null;
}

interface PromptEvaluationSummary {
  workspace_id: string;
  generated_at: string;
  last_run_at: string;
  指标: Record<string, number>;
  资产统计: Record<string, number>;
  运行状态: Record<string, number>;
  优化候选: Record<string, number>;
}

export class TestApiClient {
  private token: string | null = null;
  private workspaceSlug: string | null = null;
  private workspaceId: string | null = null;
  private account: string | null = null;
  private createdIssueIds: string[] = [];
  private createdRuntimeIds: string[] = [];

  async login(account: string, name: string) {
    const res = await fetch(`${API_BASE}/auth/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ account, password: "e2e-password" }),
    });
    if (!res.ok) {
      throw new Error(`login failed: ${res.status}`);
    }
    const data = await res.json();

    this.token = data.token;
    this.account = account;

    if (name && data.user?.name !== name) {
      await this.authedFetch("/api/me", {
        method: "PATCH",
        body: JSON.stringify({ name }),
      });
    }

    return data;
  }

  async getWorkspaces(): Promise<TestWorkspace[]> {
    const res = await this.authedFetch("/api/workspaces");
    return res.json();
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

  async markUserOnboarded() {
    if (!this.account) {
      throw new Error("Cannot mark E2E user onboarded before login");
    }

    const client = new pg.Client(DATABASE_URL);
    await client.connect();
    try {
      const result = await client.query(
        `
          UPDATE "user"
          SET
            onboarded_at = COALESCE(onboarded_at, now()),
            onboarding_questionnaire = COALESCE(onboarding_questionnaire, '{}'::jsonb)
              || '{"source":["friends_colleagues"],"source_other":null,"source_skipped":false}'::jsonb
          WHERE account = $1
        `,
        [this.account],
      );
      if (result.rowCount !== 1) {
        throw new Error(`Failed to mark E2E user onboarded: ${this.account}`);
      }
    } finally {
      await client.end();
    }
  }

  async ensureOnlineCodeBuddyRuntime(name = `E2E CodeBuddy Runtime ${Date.now()}`) {
    if (!this.workspaceId) {
      throw new Error("Cannot seed CodeBuddy runtime before workspace is selected");
    }
    if (!this.account) {
      throw new Error("Cannot seed CodeBuddy runtime before login");
    }

    const client = new pg.Client(DATABASE_URL);
    await client.connect();
    try {
      const userRow = await client.query<{ id: string }>(
        `SELECT id FROM "user" WHERE account = $1 LIMIT 1`,
        [this.account],
      );
      if (userRow.rows.length === 0) {
        throw new Error(`E2E user missing: ${this.account}`);
      }
      const userId = userRow.rows[0]!.id;
      const result = await client.query<{ id: string }>(
        `
          INSERT INTO agent_runtime (
            workspace_id, daemon_id, name, runtime_mode, provider, status,
            device_info, metadata, owner_id, visibility, last_seen_at
          )
          VALUES (
            $1, $2, $3, 'cloud', 'codebuddy', 'online',
            'E2E CodeBuddy runtime', '{"用途":"训练与评估 E2E 真实任务入队"}'::jsonb, $4, 'public', now()
          )
          RETURNING id
        `,
        [
          this.workspaceId,
          `e2e-codebuddy-${Date.now()}-${Math.random().toString(36).slice(2)}`,
          name,
          userId,
        ],
      );
      const runtimeId = result.rows[0]!.id;
      this.createdRuntimeIds.push(runtimeId);
      return { id: runtimeId, name };
    } finally {
      await client.end();
    }
  }

  async createIssue(title: string, opts?: Record<string, unknown>) {
    const res = await this.authedFetch("/api/issues", {
      method: "POST",
      body: JSON.stringify({ title, ...opts }),
    });
    const issue = await res.json();
    this.createdIssueIds.push(issue.id);
    return issue;
  }

  async deleteIssue(id: string) {
    await this.authedFetch(`/api/issues/${id}`, { method: "DELETE" });
  }

  async listPromptLibraryItems(): Promise<PromptLibraryItem[]> {
    const res = await this.authedFetch("/api/prompt-library");
    if (!res.ok) {
      throw new Error(`list prompt library items failed: ${res.status}`);
    }
    const data = await res.json();
    return data.items ?? [];
  }

  async deletePromptLibraryItem(id: string) {
    await this.authedFetch(`/api/prompt-library/${id}`, { method: "DELETE" });
  }

  async listPromptEvaluationAssets(params?: { asset_type?: string; prompt_id?: string }): Promise<PromptEvaluationAsset[]> {
    const search = new URLSearchParams();
    if (params?.asset_type) search.set("asset_type", params.asset_type);
    if (params?.prompt_id) search.set("prompt_id", params.prompt_id);
    const res = await this.authedFetch(`/api/prompt-evaluation-assets${search.toString() ? `?${search}` : ""}`);
    if (!res.ok) {
      throw new Error(`list prompt evaluation assets failed: ${res.status}`);
    }
    const data = await res.json();
    return data.items ?? [];
  }

  async deletePromptEvaluationAsset(id: string) {
    await this.authedFetch(`/api/prompt-evaluation-assets/${id}`, { method: "DELETE" });
  }

  async createPromptEvaluationAsset(data: Record<string, unknown>): Promise<PromptEvaluationAsset> {
    const res = await this.authedFetch("/api/prompt-evaluation-assets", {
      method: "POST",
      body: JSON.stringify(data),
    });
    if (!res.ok) {
      throw new Error(`create prompt evaluation asset failed: ${res.status} ${await res.text()}`);
    }
    return res.json();
  }

  async updatePromptEvaluationAsset(id: string, data: Record<string, unknown>): Promise<PromptEvaluationAsset> {
    const res = await this.authedFetch(`/api/prompt-evaluation-assets/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    });
    if (!res.ok) {
      throw new Error(`update prompt evaluation asset failed: ${res.status}`);
    }
    return res.json();
  }

  async completePromptEvaluationAgentTask(run: PromptEvaluationRun) {
    if (!this.workspaceId) {
      throw new Error("Cannot complete E2E agent task before workspace is selected");
    }
    if (!run.task_id || !run.agent_id || !run.runtime_id || !run.chat_session_id) {
      throw new Error(`Prompt evaluation run is not linked to a complete agent task graph: ${run.id}`);
    }

    const client = new pg.Client(DATABASE_URL);
    await client.connect();
    try {
      await client.query("BEGIN");
      await client.query(
        `
          UPDATE agent_task_queue
          SET
            status = 'completed',
            started_at = COALESCE(started_at, now() - interval '2 seconds'),
            completed_at = now(),
            result = '{"status":"completed","output":"Agent 输出：完成训练评估并给出验收证据"}'::jsonb,
            error = NULL,
            session_id = COALESCE(session_id, 'e2e-prompt-eval-session'),
            work_dir = COALESCE(work_dir, '/tmp/e2e-prompt-eval')
          WHERE id = $1
        `,
        [run.task_id],
      );
      await client.query(
        `
          INSERT INTO task_usage (
            task_id, provider, model, input_tokens, output_tokens,
            cache_read_tokens, cache_write_tokens, updated_at
          )
          VALUES ($1, 'codebuddy', 'minimax-m2.7-ioa', 11, 7, 2, 3, now())
          ON CONFLICT (task_id, provider, model)
          DO UPDATE SET
            input_tokens = EXCLUDED.input_tokens,
            output_tokens = EXCLUDED.output_tokens,
            cache_read_tokens = EXCLUDED.cache_read_tokens,
            cache_write_tokens = EXCLUDED.cache_write_tokens,
            updated_at = now()
        `,
        [run.task_id],
      );
      await client.query(`DELETE FROM task_message WHERE task_id = $1`, [run.task_id]);
      await client.query(
        `
          INSERT INTO task_message (task_id, seq, type, tool, content, input, output)
          VALUES
            ($1, 1, 'text', NULL, 'Agent 输出：完成训练评估', '{}'::jsonb, NULL),
            ($1, 2, 'tool_result', '训练评估同步', '已收集 trace、token 和运行结论', '{}'::jsonb, '通过')
        `,
        [run.task_id],
      );
      await client.query(`DELETE FROM task_trace_event WHERE task_id = $1`, [run.task_id]);
      await client.query(
        `
          INSERT INTO task_trace_event (
            workspace_id, task_id, agent_id, runtime_id, source, event_type,
            event_name, status, attempt, duration_ms, run_ms, total_ms,
            provider, model, input_tokens, output_tokens, cache_read_tokens,
            cache_write_tokens, failure_reason, error_type, chat_session_id, metadata
          )
          VALUES (
            $1, $2, $3, $4, 'prompt_evaluation', 'llm.usage_reported',
            '训练评估用量已上报', 'completed', 1, 2000, 1800, 2100,
            'codebuddy', 'minimax-m2.7-ioa', 16, 7, 2,
            3, '无', '', $5, '{"阶段":"训练评估","验收":"E2E"}'::jsonb
          )
        `,
        [this.workspaceId, run.task_id, run.agent_id, run.runtime_id, run.chat_session_id],
      );
      await client.query("COMMIT");
    } catch (error) {
      await client.query("ROLLBACK");
      throw error;
    } finally {
      await client.end();
    }
  }

  async runPromptEvaluationAsset(id: string): Promise<PromptEvaluationAsset> {
    const res = await this.authedFetch(`/api/prompt-evaluation-assets/${id}/run`, { method: "POST" });
    if (!res.ok) {
      throw new Error(`run prompt evaluation asset failed: ${res.status} ${await res.text()}`);
    }
    return res.json();
  }

  async listPromptEvaluationRuns(params?: { asset_id?: string; status?: string; limit?: number }): Promise<PromptEvaluationRun[]> {
    const search = new URLSearchParams();
    if (params?.asset_id) search.set("asset_id", params.asset_id);
    if (params?.status) search.set("status", params.status);
    if (params?.limit) search.set("limit", String(params.limit));
    const res = await this.authedFetch(`/api/prompt-evaluation-runs${search.toString() ? `?${search}` : ""}`);
    if (!res.ok) {
      throw new Error(`list prompt evaluation runs failed: ${res.status}`);
    }
    const data = await res.json();
    return data.items ?? [];
  }

  async getPromptEvaluationRunEvidence(runId: string): Promise<PromptEvaluationRunEvidence> {
    const res = await this.authedFetch(`/api/prompt-evaluation-runs/${runId}/evidence`);
    if (!res.ok) {
      throw new Error(`get prompt evaluation run evidence failed: ${res.status}`);
    }
    return res.json();
  }

  async getPromptEvaluationSummary(): Promise<PromptEvaluationSummary> {
    const res = await this.authedFetch("/api/prompt-evaluation-summary");
    if (!res.ok) {
      throw new Error(`get prompt evaluation summary failed: ${res.status} ${await res.text()}`);
    }
    return res.json();
  }

  async listPromptEvaluationOptimizationCandidates(params?: { run_id?: string; prompt_id?: string; status?: string }): Promise<PromptEvaluationOptimizationCandidate[]> {
    const search = new URLSearchParams();
    if (params?.run_id) search.set("run_id", params.run_id);
    if (params?.prompt_id) search.set("prompt_id", params.prompt_id);
    if (params?.status) search.set("status", params.status);
    const res = await this.authedFetch(`/api/prompt-evaluation-optimization-candidates${search.toString() ? `?${search}` : ""}`);
    if (!res.ok) {
      throw new Error(`list prompt evaluation optimization candidates failed: ${res.status}`);
    }
    const data = await res.json();
    return data.items ?? [];
  }

  async listPromptEvaluationCases(params?: { asset_id?: string; status?: string }): Promise<PromptEvaluationCase[]> {
    const search = new URLSearchParams();
    if (params?.asset_id) search.set("asset_id", params.asset_id);
    if (params?.status) search.set("status", params.status);
    const res = await this.authedFetch(`/api/prompt-evaluation-cases${search.toString() ? `?${search}` : ""}`);
    if (!res.ok) {
      throw new Error(`list prompt evaluation cases failed: ${res.status}`);
    }
    const data = await res.json();
    return data.items ?? [];
  }

  async cleanupPromptArtifactsByPrefix(prefix: string) {
    const assets = await this.listPromptEvaluationAssets();
    for (const asset of assets) {
      if (asset.name.startsWith(prefix)) {
        try {
          await this.deletePromptEvaluationAsset(asset.id);
        } catch {
          /* ignore */
        }
      }
    }
    const prompts = await this.listPromptLibraryItems();
    for (const prompt of prompts) {
      if (prompt.name.startsWith(prefix)) {
        try {
          await this.deletePromptLibraryItem(prompt.id);
        } catch {
          /* ignore */
        }
      }
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
                 OR (workspace_id = $2 AND name = 'Multica 训练评估 Agent')
            `,
            [runtimeId, this.workspaceId],
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

  /** Clean up all issues created during this test. */
  async cleanup() {
    await this.cleanupSeededRuntimes();
    for (const id of this.createdIssueIds) {
      try {
        await this.deleteIssue(id);
      } catch {
        /* ignore — may already be deleted */
      }
    }
    this.createdIssueIds = [];
  }

  getToken() {
    return this.token;
  }

  getAccount() {
    if (!this.account) {
      throw new Error("Test API client is not logged in");
    }
    return this.account;
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
}
