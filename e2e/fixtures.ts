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

interface PromptEvaluationAgentRunResponse {
  asset: PromptEvaluationAsset;
  run: PromptEvaluationRun;
  task_id: string;
  chat_session_id: string;
  agent_id: string;
  runtime_id: string;
  model: string;
  status: string;
  message: string;
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
  source: string;
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

interface CodingSquadFixture {
  runtimeId: string;
  squadId: string;
  squadName: string;
  leaderAgentId: string;
  agents: Array<{ id: string; name: string; role: string; roleKey: string }>;
}

interface SquadLeaderTask {
  id: string;
  status: string;
  agent_id: string;
  runtime_id: string;
  issue_id: string;
  is_leader_task: boolean;
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

export class TestApiClient {
  private token: string | null = null;
  private workspaceSlug: string | null = null;
  private workspaceId: string | null = null;
  private account: string | null = null;
  private createdIssueIds: string[] = [];
  private createdRuntimeIds: string[] = [];
  private createdSquadIds: string[] = [];

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

  async createCodingSquadFixture(name = `E2E Multica 编码小队 ${Date.now()}`): Promise<CodingSquadFixture> {
    if (!this.workspaceId) {
      throw new Error("Cannot seed coding squad before workspace is selected");
    }
    if (!this.account) {
      throw new Error("Cannot seed coding squad before login");
    }

    const runtime = await this.ensureOnlineCodeBuddyRuntime(`${name} Runtime`);
    const profile = {
      profile_key: "multica-coding-squad-v1",
      project: "Multica",
      repo: "/data/ida/goal-test",
      mode: "coding_squad",
      operation_skills: ["需求拆解", "代码实现", "独立验收", "规约同步", "部署验证"],
      acceptance: ["方案可审阅", "开发范围清晰", "验收者独立检查", "日志和测试证据齐全"],
      forbidden_actions: ["开发者自证通过", "越权修改非负责范围", "泄露密钥"],
      model_policy: {
        "大量文本任务": "minimax-m2.7-ioa",
        "代码测试任务": "gpt/code",
      },
      roles: [
        {
          key: "captain",
          name: "队长",
          responsibility: "接需求、判断流程、拆任务、分派给不同 AI、跟踪进度。",
          output: "小队执行计划和任务分派记录",
        },
        {
          key: "designer",
          name: "方案设计者",
          responsibility: "输出技术方案、影响面和测试方案，开发前给人确认。",
          output: "技术方案和测试计划",
        },
        {
          key: "developer",
          name: "开发者",
          responsibility: "只处理被分配的代码范围。",
          boundary: "不得随手修改别人负责的范围。",
          output: "代码变更和自测记录",
        },
        {
          key: "acceptor",
          name: "验收者",
          responsibility: "独立检查开发者代码、测试结果和漏改风险。",
          forbidden: "开发者不能自己说通过。",
          output: "独立验收结论",
        },
        {
          key: "spec-maintainer",
          name: "规约维护者",
          responsibility: "同步流程文档、测试数据说明、接口索引和技能说明。",
          output: "规约同步清单",
        },
        {
          key: "operator",
          name: "部署运行者",
          responsibility: "负责端口、环境变量、数据库、启动服务、健康检查和部署验证。",
          forbidden: "不得泄露密钥，不随意改业务代码。",
          output: "部署与健康检查证据",
        },
      ],
      steps: [
        { key: "receive", name: "需求接收与流程判断", role_key: "captain" },
        { key: "design", name: "技术方案与测试方案", role_key: "designer" },
        { key: "develop", name: "范围内实现", role_key: "developer" },
        { key: "acceptance", name: "独立验收", role_key: "acceptor" },
        { key: "spec", name: "规约同步", role_key: "spec-maintainer" },
        { key: "deploy", name: "部署与健康检查", role_key: "operator" },
      ],
    };
    const roleSeeds = [
      ["captain", "队长", "负责接需求、拆任务、分派和跟踪进度。"],
      ["designer", "方案设计者", "负责技术方案、影响面和测试方案。"],
      ["developer", "开发者", "负责限定范围内的代码实现。"],
      ["acceptor", "验收者", "负责独立验收和漏改检查。"],
      ["spec-maintainer", "规约维护者", "负责同步文档、接口索引和技能说明。"],
      ["operator", "部署运行者", "负责环境、启动、日志和健康检查。"],
    ] as const;

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
      await client.query("BEGIN");
      const agents: CodingSquadFixture["agents"] = [];
      for (const [roleKey, role, instruction] of roleSeeds) {
        const agent = await client.query<{ id: string }>(
          `
            INSERT INTO agent (
              workspace_id, name, runtime_mode, runtime_config, runtime_id,
              visibility, status, max_concurrent_tasks, owner_id,
              instructions, custom_env, custom_args, model
            )
            VALUES (
              $1, $2, 'cloud', '{"provider":"codebuddy","用途":"Multica 编码小队 E2E"}'::jsonb, $3,
              'workspace', 'idle', 2, $4,
              $5, '{}'::jsonb, '[]'::jsonb, 'minimax-m2.7-ioa'
            )
            RETURNING id
          `,
          [
            this.workspaceId,
            `${name} · ${role}`,
            runtime.id,
            userId,
            `你是 Multica 编码小队的${role}。${instruction}所有输出使用中文，并保留可验收证据。`,
          ],
        );
        agents.push({ id: agent.rows[0]!.id, name: `${name} · ${role}`, role, roleKey });
      }

      const squad = await client.query<{ id: string }>(
        `
          INSERT INTO squad (
            workspace_id, name, description, leader_id, creator_id, instructions, sop_profile
          )
          VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
          RETURNING id
        `,
        [
          this.workspaceId,
          name,
          "用于开发 Multica 自身的生产级编码小队，包含队长、方案设计者、开发者、验收者、规约维护者和部署运行者。",
          agents[0]!.id,
          userId,
          "队长先澄清需求和验收口径，再按角色分派；开发者不得越界；验收者必须独立给出证据；所有指标和输出使用中文。",
          JSON.stringify(profile),
        ],
      );
      const squadId = squad.rows[0]!.id;
      for (const agent of agents) {
        await client.query(
          `
            INSERT INTO squad_member (squad_id, member_type, member_id, role)
            VALUES ($1, 'agent', $2, $3)
            ON CONFLICT (squad_id, member_type, member_id)
            DO UPDATE SET role = EXCLUDED.role
          `,
          [squadId, agent.id, agent.role],
        );
      }
      await client.query("COMMIT");
      this.createdSquadIds.push(squadId);
      return {
        runtimeId: runtime.id,
        squadId,
        squadName: name,
        leaderAgentId: agents[0]!.id,
        agents,
      };
    } catch (error) {
      await client.query("ROLLBACK");
      throw error;
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

  async listIssueSOPRuns(issueId: string): Promise<{ items: SquadSOPRun[]; total: number }> {
    const res = await this.authedFetch(`/api/issues/${issueId}/sop-runs`);
    if (!res.ok) {
      throw new Error(`list issue SOP runs failed: ${res.status} ${await res.text()}`);
    }
    return res.json();
  }

  async recordSOPStepEvent(
    runId: string,
    stepId: string,
    data: Record<string, unknown>,
  ): Promise<SquadSOPRun["events"][number]> {
    const res = await this.authedFetch(`/api/sop-runs/${runId}/steps/${encodeURIComponent(stepId)}/events`, {
      method: "POST",
      body: JSON.stringify(data),
    });
    if (!res.ok) {
      throw new Error(`record SOP step event failed: ${res.status} ${await res.text()}`);
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
            COALESCE(is_leader_task, false) AS is_leader_task
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

  async completeSquadLeaderTaskWithEvidence(task: SquadLeaderTask, opts?: { squadId?: string }) {
    if (!this.workspaceId) {
      throw new Error("Cannot complete squad leader task before workspace is selected");
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
            started_at = COALESCE(started_at, now() - interval '5 seconds'),
            completed_at = now(),
            result = '{"状态":"完成","结论":"队长已完成分派并收集编码小队验收证据"}'::jsonb,
            error = NULL,
            session_id = COALESCE(session_id, 'e2e-coding-squad-session'),
            work_dir = COALESCE(work_dir, '/data/ida/goal-test')
          WHERE id = $1
        `,
        [task.id],
      );
      await client.query(
        `
          INSERT INTO task_usage (
            task_id, provider, model, input_tokens, output_tokens,
            cache_read_tokens, cache_write_tokens, updated_at
          )
          VALUES ($1, 'codebuddy', 'minimax-m2.7-ioa', 31, 19, 5, 7, now())
          ON CONFLICT (task_id, provider, model)
          DO UPDATE SET
            input_tokens = EXCLUDED.input_tokens,
            output_tokens = EXCLUDED.output_tokens,
            cache_read_tokens = EXCLUDED.cache_read_tokens,
            cache_write_tokens = EXCLUDED.cache_write_tokens,
            updated_at = now()
        `,
        [task.id],
      );
      await client.query(`DELETE FROM task_message WHERE task_id = $1`, [task.id]);
      await client.query(
        `
          INSERT INTO task_message (task_id, seq, type, tool, content, input, output)
          VALUES
            ($1, 1, 'text', NULL, '队长输出：已完成需求接收、方案分派和验收证据登记', '{}'::jsonb, NULL),
            ($1, 2, 'tool_result', 'multica squad activity', '已记录编码小队 SOP 事件', '{}'::jsonb, '通过')
        `,
        [task.id],
      );
      await client.query(`DELETE FROM task_trace_event WHERE task_id = $1`, [task.id]);
      await client.query(
        `
          INSERT INTO task_trace_event (
            workspace_id, task_id, issue_id, squad_id, agent_id, runtime_id,
            source, event_type, event_name, status, attempt,
            queue_wait_ms, duration_ms, run_ms, total_ms,
            provider, model, input_tokens, output_tokens, cache_read_tokens,
            cache_write_tokens, failure_reason, error_type, chat_session_id, metadata
          )
          VALUES (
            $1, $2, $3, $4, $5, $6,
            'squad_sop', 'squad.leader.completed', '编码小队队长任务完成', 'completed', 1,
            400, 2800, 2300, 3200,
            'codebuddy', 'minimax-m2.7-ioa', 36, 19, 5,
            7, '无', '', NULL,
            '{"阶段":"编码小队","证据":"E2E","模型策略":"minimax"}'::jsonb
          )
        `,
        [this.workspaceId, task.id, task.issue_id, opts?.squadId ?? null, task.agent_id, task.runtime_id],
      );
      await client.query("COMMIT");
    } catch (error) {
      await client.query("ROLLBACK");
      throw error;
    } finally {
      await client.end();
    }
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
      const structuredOutput = [
        "Agent 输出：完成训练评估并给出验收证据。",
        "```json",
        JSON.stringify({
          用例结果: [
            {
              case_index: 0,
              status: "通过",
              output: "输出需求澄清结论、风险、测试证据和下一步建议。",
              failure_reason: "无",
              evidence: {
                命中: ["需求澄清结论", "风险", "测试证据", "下一步建议"],
                缺失: [],
                trace_task_id: run.task_id,
              },
            },
          ],
          评估结论: "Agent 已返回结构化逐用例评估，全部用例通过",
          总用例数: 1,
          通过数: 1,
          失败数: 0,
          失败原因: "无",
          改进建议: ["保持中文输出和 trace/task id 证据"],
          可复盘证据: [run.task_id],
        }),
        "```",
      ].join("\n");
      await client.query("BEGIN");
      await client.query(
        `
          UPDATE agent_task_queue
          SET
            status = 'completed',
            started_at = COALESCE(started_at, now() - interval '2 seconds'),
            completed_at = now(),
            result = $2::jsonb,
            error = NULL,
            session_id = COALESCE(session_id, 'e2e-prompt-eval-session'),
            work_dir = COALESCE(work_dir, '/tmp/e2e-prompt-eval')
          WHERE id = $1
        `,
        [run.task_id, JSON.stringify({ status: "completed", output: structuredOutput })],
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
            ($1, 1, 'text', NULL, $2, '{}'::jsonb, NULL),
            ($1, 2, 'tool_result', '训练评估同步', '已收集 trace、token 和结构化逐用例结论', '{}'::jsonb, '通过')
        `,
        [run.task_id, structuredOutput],
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

  async completePromptEvaluationOptimizationAgentTask(run: PromptEvaluationRun) {
    if (!this.workspaceId) {
      throw new Error("Cannot complete E2E optimization agent task before workspace is selected");
    }
    if (!run.task_id || !run.agent_id || !run.runtime_id || !run.chat_session_id) {
      throw new Error(`Prompt evaluation optimization run is not linked to a complete agent task graph: ${run.id}`);
    }

    const client = new pg.Client(DATABASE_URL);
    await client.connect();
    try {
      const structuredOutput = [
        "Agent 优化输出：已基于失败用例生成待人工确认的优化候选。",
        "```json",
        JSON.stringify({
          用例结果: [
            {
              case_index: 0,
              status: "通过",
              output: "已生成优化候选提示词正文。",
              failure_reason: "无",
              evidence: {
                命中: ["优化候选", "验收条件", "trace/task id"],
                trace_task_id: run.task_id,
              },
            },
          ],
          评估结论: "Agent 已生成可人工确认的优化候选",
          优化候选名称: "Agent 自动优化候选",
          候选提示词正文:
            "请澄清 {{issue_title}}，输出必须使用中文；必须给出优化候选、验收条件、trace/task id、失败原因和下一步人工确认点。",
          逐条修改依据: "补充验收条件、trace/task id、失败原因和人工确认点，保证领导演示可以复盘。",
          可能影响的通过用例: "需要回归原有中文澄清用例，确认没有降低通过质量。",
          人工验收清单: ["确认中文输出", "确认包含 trace/task id", "确认原提示词未被自动替换"],
        }),
        "```",
      ].join("\n");
      await client.query("BEGIN");
      await client.query(
        `
          UPDATE agent_task_queue
          SET
            status = 'completed',
            started_at = COALESCE(started_at, now() - interval '2 seconds'),
            completed_at = now(),
            result = $2::jsonb,
            error = NULL,
            session_id = COALESCE(session_id, 'e2e-prompt-eval-optimization-session'),
            work_dir = COALESCE(work_dir, '/tmp/e2e-prompt-eval')
          WHERE id = $1
        `,
        [run.task_id, JSON.stringify({ status: "completed", output: structuredOutput })],
      );
      await client.query(
        `
          INSERT INTO task_usage (
            task_id, provider, model, input_tokens, output_tokens,
            cache_read_tokens, cache_write_tokens, updated_at
          )
          VALUES ($1, 'codebuddy', 'minimax-m2.7-ioa', 21, 13, 1, 2, now())
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
            ($1, 1, 'text', NULL, $2, '{}'::jsonb, NULL),
            ($1, 2, 'tool_result', 'Agent 优化同步', '已生成候选正文、依据和人工验收清单', '{}'::jsonb, '通过')
        `,
        [run.task_id, structuredOutput],
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
            'Agent 优化候选已生成', 'completed', 1, 2400, 2100, 2500,
            'codebuddy', 'minimax-m2.7-ioa', 21, 13, 1,
            2, '无', '', $5, '{"阶段":"Agent 优化运行","验收":"E2E"}'::jsonb
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

  async runPromptEvaluationOptimizationAgent(runId: string): Promise<PromptEvaluationAgentRunResponse> {
    const res = await this.authedFetch(`/api/prompt-evaluation-runs/${runId}/optimization-agent-run`, { method: "POST" });
    if (!res.ok) {
      throw new Error(`run prompt evaluation optimization agent failed: ${res.status} ${await res.text()}`);
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
    await this.cleanupSeededSquads();
    await this.cleanupSeededRuntimes();
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
