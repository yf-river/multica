import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..");

loadEnvFile(path.join(repoRoot, ".env.worktree"));
loadEnvFile(path.join(repoRoot, ".env.local"));

const apiURL = trimEnv("ACCEPTANCE_API_URL")
  || trimEnv("NEXT_PUBLIC_API_URL")
  || `http://127.0.0.1:${trimEnv("PORT") || "8080"}`;
const workspaceSlug = trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || trimEnv("REAL_AGENT_E2E_WORKSPACE") || "goal-test-daemon";
const demoAccount = trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || trimEnv("REAL_AGENT_E2E_ACCOUNT") || "goal-test-daemon";
const demoPassword = trimEnv("ACCEPTANCE_DEMO_PASSWORD") || trimEnv("REAL_AGENT_E2E_PASSWORD") || "e2e-password";
const waitMs = Number(trimEnv("ACCEPTANCE_DEMO_WAIT_MS") || 180_000);
const forceNewDemoEvidence = trimEnv("ACCEPTANCE_DEMO_FORCE_NEW") === "1";

const token = await login();
const workspace = await ensureWorkspace(token, "Goal Test Daemon", workspaceSlug);
await markUserOnboarded(demoAccount);

const readiness = await authedJSON(token, workspace, "/api/prompt-evaluation-runtime-readiness");
const templates = [
  {
    key: "user-center",
    title: "生产验收 usercenter 小队闭环",
    description: "生产演示持久证据：usercenter 小队队长接收 issue，系统生成 SOP run，并由真实 daemon 写回 trace。",
  },
  {
    key: "multica-coding",
    title: "生产验收 Multica 编码小队闭环",
    description: "生产演示持久证据：Multica 编码小队按队长、方案设计者、开发者、验收者、规约维护者和部署运行者协作推进。",
  },
];

const prepared = [];
for (const template of templates) {
  prepared.push(await prepareSquadEvidence(token, workspace, template));
}
const optimizationCandidate = await ensureOptimizationCandidate(token, workspace.id);
const evidenceSnapshot = await ensureEvidenceSnapshot(token, workspace.id);

console.log(JSON.stringify({
  status: "已准备",
  api_url: apiURL,
  workspace,
  runtime_readiness: readiness,
  squads: prepared,
  optimization_candidate: optimizationCandidate,
  evidence_snapshot: evidenceSnapshot,
}, null, 2));

async function prepareSquadEvidence(token, workspace, templateSpec) {
  const template = await postJSON(token, workspace, "/api/squads/internal-template", {
    template_key: templateSpec.key,
  });
  const squad = template.squad;
  const leader = template.agents?.find((agent) => agent.role_key === "captain");
  if (!squad?.id || !leader?.id) {
    throw new Error(`内置小队模板返回不完整：${templateSpec.key}`);
  }

  const existing = forceNewDemoEvidence
    ? null
    : await findExistingSquadEvidence(workspace.id, templateSpec.title, squad.id, leader.id);
  if (existing) {
    return {
      template_key: templateSpec.key,
      squad_id: squad.id,
      squad_name: squad.name,
      issue_id: existing.issue_id,
      issue_title: existing.issue_title,
      sop_run_id: existing.sop_run_id,
      current_step_key: existing.current_step_key,
      leader_agent_id: leader.id,
      leader_task_id: existing.leader_task_id,
      leader_task_status: existing.leader_task_status || "未知",
      trace_event_count: Number(existing.trace_event_count || 0),
      usage_row_count: Number(existing.usage_row_count || 0),
      reused: true,
    };
  }

  const issue = await postJSON(token, workspace, "/api/issues", {
    title: `${templateSpec.title} ${new Date().toISOString()}`,
    description: templateSpec.description,
    status: "todo",
    priority: "high",
    assignee_type: "squad",
    assignee_id: squad.id,
  });

  const run = await waitForSOPRun(token, workspace, issue.id, templateSpec.key);
  await recordFirstStepEvidence(token, workspace, run, issue, leader);

  const leaderTask = await waitForLeaderTask(issue.id, leader.id, Math.min(waitMs, 30_000));
  const terminalTask = leaderTask
    ? await waitForTerminalTask(leaderTask.id, waitMs)
    : null;
  const traceCount = leaderTask ? await countTraceEvents(leaderTask.id) : 0;
  const usageCount = leaderTask ? await countTaskUsage(leaderTask.id) : 0;

  return {
    template_key: templateSpec.key,
    squad_id: squad.id,
    squad_name: squad.name,
    issue_id: issue.id,
    issue_title: issue.title,
    sop_run_id: run.id,
    current_step_key: run.current_step_key,
    leader_agent_id: leader.id,
    leader_task_id: leaderTask?.id ?? null,
    leader_task_status: terminalTask?.status ?? leaderTask?.status ?? "未入队",
    trace_event_count: traceCount,
    usage_row_count: usageCount,
    leader_task_usable: (terminalTask?.status ?? leaderTask?.status) === "completed" && traceCount > 0 && usageCount > 0,
  };
}

async function findExistingSquadEvidence(workspaceID, titlePrefix, squadID, leaderAgentID) {
  const databaseURL = trimEnv("DATABASE_URL");
  if (!databaseURL) return null;
  const pg = await import("pg");
  const client = new pg.default.Client(databaseURL);
  await client.connect();
  try {
    const res = await client.query(
      `
        SELECT
          i.id::text AS issue_id,
          i.title AS issue_title,
          r.id::text AS sop_run_id,
          r.current_step_key,
          atq.id::text AS leader_task_id,
          atq.status AS leader_task_status,
          (
            SELECT count(*)::int
            FROM task_usage tu
            WHERE tu.task_id = atq.id
          ) AS usage_row_count,
          (
            SELECT count(*)::int
            FROM task_trace_event tte
            WHERE tte.task_id = atq.id
          ) AS trace_event_count
        FROM issue i
        JOIN squad_sop_run r ON r.issue_id = i.id AND r.squad_id = $3
        LEFT JOIN LATERAL (
          SELECT id, status
          FROM agent_task_queue
          WHERE issue_id = i.id AND agent_id = $4
            AND status = 'completed'
            AND EXISTS (SELECT 1 FROM task_usage tu WHERE tu.task_id = agent_task_queue.id)
            AND EXISTS (SELECT 1 FROM task_trace_event tte WHERE tte.task_id = agent_task_queue.id)
          ORDER BY created_at DESC
          LIMIT 1
        ) atq ON true
        WHERE i.workspace_id = $1
          AND i.title LIKE ($2 || '%')
          AND atq.id IS NOT NULL
        ORDER BY i.created_at DESC
        LIMIT 1
      `,
      [workspaceID, titlePrefix, squadID, leaderAgentID],
    );
    return res.rows[0] ?? null;
  } finally {
    await client.end();
  }
}

async function waitForSOPRun(token, workspace, issueID, templateKey) {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    const data = await authedJSON(token, workspace, `/api/issues/${issueID}/sop-runs`);
    const run = data.items?.find((item) => item.profile_key === templateKey || item.profile_key.includes(templateKey));
    if (run?.id) return run;
    await sleep(1_000);
  }
  throw new Error(`等待 SOP run 超时：${templateKey}`);
}

async function recordFirstStepEvidence(token, workspace, run, issue, leader) {
  const step = Array.isArray(run.profile?.steps) ? run.profile.steps[0] : null;
  const stepKey = step?.key || run.current_step_key || "receive";
  await postJSON(token, workspace, `/api/sop-runs/${run.id}/steps/${encodeURIComponent(stepKey)}/events`, {
    event_type: "追加证据",
    status: "进行中",
    step_name: step?.name || "接收需求",
    role_key: step?.role_key || "captain",
    evidence: {
      "来源": "acceptance:prepare-demo",
      "issue": issue.title,
      "队长": leader.name,
      "结论": "生产演示工作区已留下可抽查的小队 SOP 证据",
    },
    reason: "生产验收准备",
    duration_ms: 500,
  });
}

async function waitForLeaderTask(issueID, leaderAgentID, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const task = await findLeaderTask(issueID, leaderAgentID);
    if (task) return task;
    await sleep(1_000);
  }
  return null;
}

async function waitForTerminalTask(taskID, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let latest = null;
  while (Date.now() < deadline) {
    latest = await findTask(taskID);
    if (latest && !["queued", "dispatched", "running", "waiting_local_directory"].includes(latest.status)) {
      return latest;
    }
    await sleep(3_000);
  }
  return latest;
}

async function login() {
  const res = await fetch(`${apiURL}/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ account: demoAccount, password: demoPassword }),
  });
  if (!res.ok) throw new Error(`登录失败：${res.status} ${await res.text()}`);
  const data = await res.json();
  if (!data.token) throw new Error("登录响应缺少 token");
  return data.token;
}

async function ensureWorkspace(token, name, slug) {
  const existing = await authedJSON(token, null, "/api/workspaces");
  const found = existing.find((item) => item.slug === slug);
  if (found) return found;

  const created = await postJSON(token, null, "/api/workspaces", { name, slug });
  return created;
}

async function markUserOnboarded(account) {
  const databaseURL = trimEnv("DATABASE_URL");
  if (!databaseURL) return;
  const pg = await import("pg");
  const client = new pg.default.Client(databaseURL);
  await client.connect();
  try {
    await client.query(
      `
        UPDATE "user"
        SET
          onboarded_at = COALESCE(onboarded_at, now()),
          onboarding_questionnaire = COALESCE(onboarding_questionnaire, '{}'::jsonb)
            || '{"source":["work_context"],"source_other":null,"source_skipped":false}'::jsonb
        WHERE account = $1
      `,
      [account],
    );
  } finally {
    await client.end();
  }
}

async function findLeaderTask(issueID, leaderAgentID) {
  const databaseURL = trimEnv("DATABASE_URL");
  if (!databaseURL) throw new Error("DATABASE_URL 未配置，无法抽查队长任务");
  const pg = await import("pg");
  const client = new pg.default.Client(databaseURL);
  await client.connect();
  try {
    const res = await client.query(
      `
        SELECT id::text, status, agent_id::text, runtime_id::text, issue_id::text
        FROM agent_task_queue
        WHERE issue_id = $1 AND agent_id = $2
        ORDER BY created_at DESC
        LIMIT 1
      `,
      [issueID, leaderAgentID],
    );
    return res.rows[0] ?? null;
  } finally {
    await client.end();
  }
}

async function findTask(taskID) {
  const databaseURL = trimEnv("DATABASE_URL");
  const pg = await import("pg");
  const client = new pg.default.Client(databaseURL);
  await client.connect();
  try {
    const res = await client.query(
      `
        SELECT id::text, status, error, completed_at::text
        FROM agent_task_queue
        WHERE id = $1
        LIMIT 1
      `,
      [taskID],
    );
    return res.rows[0] ?? null;
  } finally {
    await client.end();
  }
}

async function countTraceEvents(taskID) {
  const databaseURL = trimEnv("DATABASE_URL");
  const pg = await import("pg");
  const client = new pg.default.Client(databaseURL);
  await client.connect();
  try {
    const res = await client.query(
      `SELECT count(*)::int AS count FROM task_trace_event WHERE task_id = $1`,
      [taskID],
    );
    return Number(res.rows[0]?.count ?? 0);
  } finally {
    await client.end();
  }
}

async function countTaskUsage(taskID) {
  const databaseURL = trimEnv("DATABASE_URL");
  const pg = await import("pg");
  const client = new pg.default.Client(databaseURL);
  await client.connect();
  try {
    const res = await client.query(
      `SELECT count(*)::int AS count FROM task_usage WHERE task_id = $1`,
      [taskID],
    );
    return Number(res.rows[0]?.count ?? 0);
  } finally {
    await client.end();
  }
}

async function ensureOptimizationCandidate(token, workspaceID) {
  const existing = await findOptimizationCandidate(workspaceID);
  if (existing) {
    return { status: "已存在", candidate_id: existing.id, run_id: existing.run_id, candidate_status: existing.status };
  }
  const failedRun = await findFailedPromptEvaluationRun(workspaceID);
  if (!failedRun) {
    return { status: "跳过", reason: "未找到带 prompt_id 的失败训练评估运行" };
  }
  const candidate = await postJSON(token, { id: workspaceID }, `/api/prompt-evaluation-runs/${failedRun.id}/optimization-candidates`, {});
  return { status: "已生成", candidate_id: candidate.id, run_id: failedRun.id, candidate_status: candidate.status };
}

async function ensureEvidenceSnapshot(token, workspaceID) {
  const run = await findLatestPromptEvaluationRun(workspaceID);
  if (!run) {
    return { status: "跳过", reason: "未找到训练评估运行，无法归档运行证据" };
  }
  const existing = forceNewDemoEvidence ? null : await findEvidenceSnapshot(workspaceID, run.id);
  if (existing) {
    return { status: "已存在", snapshot_id: existing.id, run_id: existing.run_id, snapshot_type: existing.snapshot_type };
  }
  const snapshot = await postJSON(token, { id: workspaceID }, `/api/prompt-evaluation-runs/${run.id}/evidence-snapshots?snapshot_type=${encodeURIComponent("验收归档")}`, {});
  return {
    status: "已归档",
    snapshot_id: snapshot.id,
    run_id: snapshot.run_id,
    snapshot_type: snapshot.snapshot_type,
    summary: snapshot.summary,
  };
}

async function findOptimizationCandidate(workspaceID) {
  const databaseURL = trimEnv("DATABASE_URL");
  if (!databaseURL) return null;
  const pg = await import("pg");
  const client = new pg.default.Client(databaseURL);
  await client.connect();
  try {
    const res = await client.query(
      `
        SELECT id::text, run_id::text, status
        FROM prompt_evaluation_optimization_candidate
        WHERE workspace_id = $1
        ORDER BY created_at DESC
        LIMIT 1
      `,
      [workspaceID],
    );
    return res.rows[0] ?? null;
  } finally {
    await client.end();
  }
}

async function findEvidenceSnapshot(workspaceID, runID) {
  const databaseURL = trimEnv("DATABASE_URL");
  if (!databaseURL) return null;
  const pg = await import("pg");
  const client = new pg.default.Client(databaseURL);
  await client.connect();
  try {
    const res = await client.query(
      `
        SELECT id::text, run_id::text, snapshot_type
        FROM prompt_evaluation_evidence_snapshot
        WHERE workspace_id = $1
          AND run_id = $2
        ORDER BY created_at DESC
        LIMIT 1
      `,
      [workspaceID, runID],
    );
    return res.rows[0] ?? null;
  } finally {
    await client.end();
  }
}

async function findLatestPromptEvaluationRun(workspaceID) {
  const databaseURL = trimEnv("DATABASE_URL");
  if (!databaseURL) return null;
  const pg = await import("pg");
  const client = new pg.default.Client(databaseURL);
  await client.connect();
  try {
    const res = await client.query(
      `
        SELECT id::text, status, created_at::text
        FROM prompt_evaluation_run
        WHERE workspace_id = $1
        ORDER BY created_at DESC
        LIMIT 1
      `,
      [workspaceID],
    );
    return res.rows[0] ?? null;
  } finally {
    await client.end();
  }
}

async function findFailedPromptEvaluationRun(workspaceID) {
  const databaseURL = trimEnv("DATABASE_URL");
  if (!databaseURL) return null;
  const pg = await import("pg");
  const client = new pg.default.Client(databaseURL);
  await client.connect();
  try {
    const res = await client.query(
      `
        SELECT id::text
        FROM prompt_evaluation_run
        WHERE workspace_id = $1
          AND prompt_id IS NOT NULL
          AND (status <> '通过' OR failed_cases > 0)
        ORDER BY created_at DESC
        LIMIT 1
      `,
      [workspaceID],
    );
    return res.rows[0] ?? null;
  } finally {
    await client.end();
  }
}

async function authedJSON(token, workspace, urlPath) {
  const res = await authedFetch(token, workspace, urlPath);
  if (!res.ok) throw new Error(`${urlPath} 失败：${res.status} ${await res.text()}`);
  return res.json();
}

async function postJSON(token, workspace, urlPath, body) {
  const res = await authedFetch(token, workspace, urlPath, {
    method: "POST",
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`${urlPath} 失败：${res.status} ${await res.text()}`);
  return res.json();
}

async function authedFetch(token, workspace, urlPath, init = {}) {
  const headers = {
    "Content-Type": "application/json",
    ...(init.headers || {}),
    Authorization: `Bearer ${token}`,
  };
  if (workspace?.slug) headers["X-Workspace-Slug"] = workspace.slug;
  else if (workspace?.id) headers["X-Workspace-ID"] = workspace.id;
  return fetch(`${apiURL}${urlPath}`, { ...init, headers });
}

function trimEnv(name) {
  return (process.env[name] || "").trim();
}

function loadEnvFile(file) {
  if (!existsSync(file)) return;
  const content = readFileSync(file, "utf8");
  for (const rawLine of content.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith("#")) continue;
    const match = line.match(/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);
    if (!match) continue;
    const [, key, rawValue] = match;
    if (process.env[key] !== undefined) continue;
    process.env[key] = rawValue.replace(/^['"]|['"]$/g, "");
  }
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
