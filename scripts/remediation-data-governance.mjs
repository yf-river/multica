import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import pg from "pg";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactDir = acceptanceDir(repoRoot);
const runEnvPath = path.join(repoRoot, ".run", "env", "goal-test-int.env");

const TARGET_WORKSPACE_SLUG = process.env.REMEDIATION_WORKSPACE_SLUG || "ai-studio";
const TARGET_DATABASE = process.env.REMEDIATION_DATABASE_NAME || "multica_goal_test_int";
const apply = process.argv.includes("--apply");
const canonicalDemoPrefix = "Goal E Canonical Demo";

const targetProjects = [
  { key: "usercenter", title: "usercenter", projectPath: "ChainWeaver/ida/user-center" },
  { key: "gateway", title: "gateway", projectPath: "ChainWeaver/ida/gateway" },
  { key: "ida-deployment", title: "ida-deployment", projectPath: "ChainWeaver/ida/ida-deployment" },
];

const targetAgents = [
  { key: "pm", name: "PM", role: "owner", description: "Generic SOP product manager and run owner." },
  { key: "01-clarify", name: "01-clarify", role: "clarify", description: "Clarify scope, acceptance, constraints, and evidence needs." },
  { key: "02-design", name: "02-design", role: "design", description: "Design cross-project solution, contracts, and rollout plan." },
  { key: "03-task-split", name: "03-task-split", role: "split", description: "Split parent work into project-scoped child tasks and dependencies." },
  { key: "04-implement", name: "04-implement", role: "implement", description: "Implement code and config changes with traceable evidence." },
  { key: "05-verify", name: "05-verify", role: "verify", description: "Verify behavior, regressions, deployment state, and improvement loop." },
];

const cleanupBackupTables = [
  "activity_log",
  "agent",
  "agent_task_queue",
  "comment",
  "inbox_item",
  "issue",
  "project",
  "project_resource",
  "prompt_library_item",
  "prompt_library_version",
  "prompt_evaluation_asset",
  "prompt_evaluation_case",
  "prompt_evaluation_run",
  "prompt_evaluation_trial",
  "prompt_evaluation_optimization_candidate",
  "prompt_evaluation_evidence_snapshot",
  "prompt_evaluation_case_assertion",
  "prompt_evaluation_case_operation",
  "prompt_evaluation_dataset_row",
  "prompt_evaluation_dataset_version",
  "prompt_evaluation_dataset_version_row",
  "prompt_evaluation_dimension_score",
  "prompt_evaluation_experiment_dimension",
  "prompt_evaluation_test_suite_case",
  "squad",
  "squad_member",
  "squad_sop_run",
  "squad_sop_step_event",
  "task_message",
  "task_trace_event",
  "task_usage",
];

function readEnvFile(file) {
  if (!fs.existsSync(file)) return {};
  const out = {};
  for (const line of fs.readFileSync(file, "utf8").split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) continue;
    const idx = trimmed.indexOf("=");
    if (idx === -1) continue;
    out[trimmed.slice(0, idx)] = trimmed.slice(idx + 1);
  }
  return out;
}

function redactDatabaseURL(url) {
  return url.replace(/:\/\/([^:]+):([^@]+)@/, "://$1:<redacted>@");
}

function normalize(value) {
  return String(value || "")
    .toLowerCase()
    .replace(/[\s_·：:（）()[\]{}]+/g, "-")
    .replace(/--+/g, "-")
    .replace(/^-|-$/g, "");
}

function targetProjectMatch(title) {
  const n = normalize(title);
  if (n === "usercenter" || n === "user-center") return "usercenter";
  if (n === "gateway") return "gateway";
  if (n === "ida-deployment") return "ida-deployment";
  return "";
}

function targetAgentMatch(name) {
  const n = normalize(name);
  if (n === "pm") return "pm";
  for (const item of targetAgents) {
    if (n === item.key) return item.key;
  }
  return "";
}

function targetAgentInstructions(agentDef) {
  return [
    `Role: ${agentDef.name}.`,
    "Use only the generic SOP role identity; do not rename yourself after usercenter/gateway/ida-deployment.",
    "For execution models, prefer GPT-5.3-Codex-Spark when quota is available; automatically fall back to gpt-5.4-mini when quota is insufficient.",
    "Every task must leave observable evidence: inputs, decisions, tool calls, changed files, verification commands, failures, and follow-up improvements.",
  ].join("\n");
}

function gongfengRef(projectPath) {
  const urlPath = projectPath.replace(/^ChainWeaver\/ida\/user-center$/, "ChainWeaver/ida/user-center");
  return {
    provider: "gongfeng",
    project_path: projectPath,
    resource_kind: "commits",
    ref: "v5.0.0_dev",
    url: `https://git.code.tencent.com/${urlPath}/commits/v5.0.0_dev`,
    connection_status: "needs_test",
    sync_status: "needs_sync",
    test_status: "not_run",
  };
}

async function tableExists(client, table) {
  const res = await client.query(
    `SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name=$1`,
    [table],
  );
  return res.rowCount > 0;
}

async function columnExists(client, table, column) {
  const res = await client.query(
    `SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name=$1 AND column_name=$2`,
    [table, column],
  );
  return res.rowCount > 0;
}

async function countTable(client, table, workspaceID) {
  if (!(await tableExists(client, table))) return null;
  if (await columnExists(client, table, "workspace_id")) {
    const res = await client.query(`SELECT count(*)::int AS count FROM ${table} WHERE workspace_id=$1`, [workspaceID]);
    return res.rows[0].count;
  }
  const res = await client.query(`SELECT count(*)::int AS count FROM ${table}`);
  return res.rows[0].count;
}

async function rowsForBackup(client, table, workspaceID) {
  if (!(await tableExists(client, table))) return { skipped: "table_missing", rows: [] };
  if (await columnExists(client, table, "workspace_id")) {
    const res = await client.query(`SELECT * FROM ${table} WHERE workspace_id=$1`, [workspaceID]);
    return { rows: res.rows };
  }
  if (table === "squad_member") {
    const res = await client.query(
      `SELECT sm.*
         FROM squad_member sm
         JOIN squad s ON s.id = sm.squad_id
        WHERE s.workspace_id=$1`,
      [workspaceID],
    );
    return { rows: res.rows };
  }
  if (table === "task_message" || table === "task_usage") {
    const res = await client.query(
      `SELECT t.*
         FROM ${table} t
         JOIN agent_task_queue atq ON atq.id = t.task_id
         JOIN issue i ON i.id = atq.issue_id
        WHERE i.workspace_id=$1`,
      [workspaceID],
    );
    return { rows: res.rows };
  }
  return { skipped: "no_workspace_scope", rows: [] };
}

async function writeBackup(client, workspace, databaseName) {
  const backup = {
    schema: "multica.remediation.data_backup.v1",
    generated_at: new Date().toISOString(),
    database: databaseName,
    workspace,
    restore_note: "Rows were exported before the remediation cleanup transaction. Restore by replaying table inserts in dependency order or by restoring a DB snapshot.",
    tables: {},
  };
  for (const table of cleanupBackupTables) {
    backup.tables[table] = await rowsForBackup(client, table, workspace.id);
  }
  fs.mkdirSync(artifactDir, { recursive: true });
  const stamp = new Date().toISOString().replace(/[:.]/g, "-");
  const backupPath = path.join(artifactDir, `remediation-data-backup-${stamp}.json`);
  fs.writeFileSync(backupPath, `${JSON.stringify(backup, null, 2)}\n`);
  return {
    path: backupPath,
    table_counts: Object.fromEntries(
      Object.entries(backup.tables).map(([table, value]) => [table, value.rows.length]),
    ),
  };
}

async function collectPostState(client, workspaceID) {
  const workspace = (await client.query(
    `SELECT id, name, slug, repos FROM workspace WHERE id=$1`,
    [workspaceID],
  )).rows[0];
  const projects = (await client.query(
    `SELECT id, title, status, priority, created_at FROM project WHERE workspace_id=$1 ORDER BY title ASC, created_at ASC`,
    [workspaceID],
  )).rows;
  const agents = (await client.query(
    `SELECT id, name, model, archived_at, created_at FROM agent WHERE workspace_id=$1 ORDER BY name ASC, created_at ASC`,
    [workspaceID],
  )).rows;
  const squads = (await client.query(
    `SELECT id, name, archived_at, created_at FROM squad WHERE workspace_id=$1 ORDER BY name ASC, created_at ASC`,
    [workspaceID],
  )).rows;
  const resources = (await client.query(
    `SELECT pr.id, p.title AS project_title, pr.resource_type, pr.resource_ref, pr.label
       FROM project_resource pr
       JOIN project p ON p.id = pr.project_id
      WHERE pr.workspace_id=$1
      ORDER BY p.title ASC, pr.position ASC`,
    [workspaceID],
  )).rows;
  const issueCount = (await client.query(`SELECT count(*)::int AS count FROM issue WHERE workspace_id=$1`, [workspaceID])).rows[0].count;
  const evalAssetCount = (await client.query(`SELECT count(*)::int AS count FROM prompt_evaluation_asset WHERE workspace_id=$1`, [workspaceID])).rows[0].count;
  const promptCount = (await client.query(`SELECT count(*)::int AS count FROM prompt_library_item WHERE workspace_id=$1`, [workspaceID])).rows[0].count;
  const activeAgents = agents.filter((a) => !a.archived_at);
  const ok =
    projects.length === 3 &&
    targetProjects.every((p) => projects.some((row) => row.title === p.title)) &&
    activeAgents.length === 6 &&
    targetAgents.every((a) => activeAgents.some((row) => row.name === a.name)) &&
    Array.isArray(workspace?.repos) &&
    workspace.repos.length === 0 &&
    issueCount >= 1 &&
    evalAssetCount >= 1 &&
    promptCount >= 1;
  return {
    ok,
    workspace,
    projects,
    agents,
    squads,
    resources,
    issue_count: issueCount,
    eval_asset_count: evalAssetCount,
    prompt_count: promptCount,
  };
}

async function applyRemediationCleanup(client, workspace, dbName) {
  const workspaceID = workspace.id;
  const ownerRes = await client.query(
    `SELECT user_id FROM member WHERE workspace_id=$1 ORDER BY (role='owner') DESC, created_at ASC LIMIT 1`,
    [workspaceID],
  );
  if (ownerRes.rowCount !== 1) throw new Error(`workspace ${workspace.slug} has no member to own seeded remediation rows`);
  const ownerID = ownerRes.rows[0].user_id;
  const runtimeRes = await client.query(
    `SELECT id, runtime_mode
       FROM agent_runtime
      WHERE workspace_id=$1
      ORDER BY (provider='codex') DESC, (status='online') DESC, updated_at DESC
      LIMIT 1`,
    [workspaceID],
  );
  if (runtimeRes.rowCount !== 1) throw new Error(`workspace ${workspace.slug} has no agent_runtime for seeded agents`);
  const runtime = runtimeRes.rows[0];

  await client.query("BEGIN");
  try {
    const backup = await writeBackup(client, workspace, dbName);
    const result = {
      backup,
      owner_id: ownerID,
      runtime_id: runtime.id,
      deleted: {},
      upserted_agents: [],
      created_projects: [],
      created_resources: [],
      created_squad: null,
    };

    for (const table of ["activity_log", "inbox_item"]) {
      if (await tableExists(client, table)) {
        result.deleted[table] = (await client.query(`DELETE FROM ${table} WHERE workspace_id=$1`, [workspaceID])).rowCount;
      }
    }
    result.deleted.prompt_evaluation_asset = (await client.query(
      `DELETE FROM prompt_evaluation_asset WHERE workspace_id=$1 AND name NOT LIKE ($2 || '%')`,
      [workspaceID, canonicalDemoPrefix],
    )).rowCount;
    result.deleted.prompt_library_item = (await client.query(
      `DELETE FROM prompt_library_item WHERE workspace_id=$1 AND name NOT LIKE ($2 || '%')`,
      [workspaceID, canonicalDemoPrefix],
    )).rowCount;
    result.deleted.issue = (await client.query(
      `DELETE FROM issue WHERE workspace_id=$1 AND title NOT LIKE ($2 || '%')`,
      [workspaceID, canonicalDemoPrefix],
    )).rowCount;
    result.deleted.squad = (await client.query(`DELETE FROM squad WHERE workspace_id=$1`, [workspaceID])).rowCount;
    result.deleted.project = (await client.query(`DELETE FROM project WHERE workspace_id=$1`, [workspaceID])).rowCount;
    result.deleted.agent = (await client.query(
      `DELETE FROM agent WHERE workspace_id=$1 AND NOT (name = ANY($2::text[]))`,
      [workspaceID, targetAgents.map((agent) => agent.name)],
    )).rowCount;
    result.workspace_repos_before = workspace.repos || [];
    await client.query(`UPDATE workspace SET repos='[]'::jsonb WHERE id=$1`, [workspaceID]);
    result.workspace_repos_after = [];

    const agentIDs = {};
    for (const agentDef of targetAgents) {
      const created = await client.query(
        `INSERT INTO agent (
            workspace_id, name, description, avatar_url, runtime_mode,
            runtime_config, runtime_id, visibility, max_concurrent_tasks, owner_id,
            instructions, custom_env, custom_args, mcp_config, model, thinking_level,
            archived_at, archived_by, updated_at
         ) VALUES (
            $1, $2, $3, NULL, $4,
            $5::jsonb, $6, 'private', 6, $7,
            $8, '{}'::jsonb, '[]'::jsonb, NULL, $9, NULL,
            NULL, NULL, now()
         )
         ON CONFLICT (workspace_id, name) DO UPDATE SET
            description = EXCLUDED.description,
            runtime_mode = EXCLUDED.runtime_mode,
            runtime_config = EXCLUDED.runtime_config,
            runtime_id = EXCLUDED.runtime_id,
            visibility = EXCLUDED.visibility,
            max_concurrent_tasks = EXCLUDED.max_concurrent_tasks,
            owner_id = EXCLUDED.owner_id,
            instructions = EXCLUDED.instructions,
            custom_env = EXCLUDED.custom_env,
            custom_args = EXCLUDED.custom_args,
            mcp_config = EXCLUDED.mcp_config,
            model = EXCLUDED.model,
            thinking_level = EXCLUDED.thinking_level,
            archived_at = NULL,
            archived_by = NULL,
            updated_at = now()
         RETURNING id, name`,
        [
          workspaceID,
          agentDef.name,
          agentDef.description,
          runtime.runtime_mode,
          JSON.stringify({ model_fallback: "gpt-5.4-mini", preferred_model: "GPT-5.3-Codex-Spark" }),
          runtime.id,
          ownerID,
          targetAgentInstructions(agentDef),
          "gpt-5.4-mini",
        ],
      );
      const row = created.rows[0];
      agentIDs[row.name] = row.id;
      result.upserted_agents.push(row);
    }

    for (const [idx, projectDef] of targetProjects.entries()) {
      const project = (await client.query(
        `INSERT INTO project (workspace_id, title, description, icon, status, lead_type, lead_id, priority)
         VALUES ($1, $2, $3, $4, 'in_progress', 'agent', $5, $6)
         RETURNING id, title`,
        [
          workspaceID,
          projectDef.title,
          `Remediation baseline project for ${projectDef.projectPath}.`,
          idx === 0 ? "Users" : idx === 1 ? "Network" : "Rocket",
          agentIDs.PM,
          idx === 0 ? "high" : "medium",
        ],
      )).rows[0];
      result.created_projects.push(project);
      const resource = (await client.query(
        `INSERT INTO project_resource (project_id, workspace_id, resource_type, resource_ref, label, position, created_by)
         VALUES ($1, $2, 'gongfeng_repo', $3::jsonb, $4, 0, $5)
         RETURNING id, project_id, resource_type, label`,
        [project.id, workspaceID, JSON.stringify(gongfengRef(projectDef.projectPath)), `${projectDef.title} v5.0.0_dev`, ownerID],
      )).rows[0];
      result.created_resources.push(resource);
    }

    const squad = (await client.query(
      `INSERT INTO squad (workspace_id, name, description, leader_id, creator_id, avatar_url, instructions, sop_profile)
       VALUES ($1, 'SOP Delivery Squad', 'Generic PM + 01-05 remediation squad for cross-project execution.', $2, $3, NULL, $4, $5::jsonb)
       RETURNING id, name`,
      [
        workspaceID,
        agentIDs.PM,
        ownerID,
        "Use the six generic roles to create observable cross-project child tasks and feed failures back into prompt evaluation.",
        JSON.stringify({ profile: "pm-01-05", projects: targetProjects.map((p) => p.title) }),
      ],
    )).rows[0];
    result.created_squad = squad;
    for (const agentDef of targetAgents) {
      await client.query(
        `INSERT INTO squad_member (squad_id, member_type, member_id, role)
         VALUES ($1, 'agent', $2, $3)
         ON CONFLICT (squad_id, member_type, member_id) DO UPDATE SET role=EXCLUDED.role`,
        [squad.id, agentIDs[agentDef.name], agentDef.role],
      );
    }

    await client.query("COMMIT");
    result.post_state = await collectPostState(client, workspaceID);
    result.ok = result.post_state.ok;
    return result;
  } catch (error) {
    await client.query("ROLLBACK");
    throw error;
  }
}

async function main() {
  const env = { ...readEnvFile(runEnvPath), ...process.env };
  const databaseURL = env.DATABASE_URL;
  if (!databaseURL) throw new Error(`DATABASE_URL missing; expected ${runEnvPath}`);
  const parsed = new URL(databaseURL);
  const dbName = parsed.pathname.replace(/^\//, "");
  if (dbName !== TARGET_DATABASE) {
    throw new Error(`refusing to audit/apply non-target database ${dbName}; expected ${TARGET_DATABASE}`);
  }

  const client = new pg.Client({ connectionString: databaseURL, connectionTimeoutMillis: 5000 });
  await client.connect();
  try {
    const workspaceRes = await client.query(`SELECT id, name, slug, issue_prefix, repos FROM workspace WHERE slug=$1`, [TARGET_WORKSPACE_SLUG]);
    if (workspaceRes.rowCount !== 1) throw new Error(`workspace ${TARGET_WORKSPACE_SLUG} not found`);
    const workspace = workspaceRes.rows[0];
    const workspaceID = workspace.id;

    const tables = [
      "project",
      "project_resource",
      "issue",
      "agent",
      "squad",
      "squad_member",
      "agent_task_queue",
      "task_trace_event",
      "task_usage",
      "task_message",
      "prompt_library_item",
      "prompt_evaluation_asset",
      "prompt_evaluation_case",
      "prompt_evaluation_run",
      "prompt_evaluation_trial",
      "prompt_evaluation_optimization_candidate",
      "prompt_evaluation_evidence_snapshot",
      "inbox_item",
      "comment",
      "activity_log",
    ];
    const counts = {};
    for (const table of tables) counts[table] = await countTable(client, table, workspaceID);

    const projects = (await client.query(
      `SELECT p.id, p.title, p.status, p.priority, p.lead_type, p.lead_id, p.created_at,
              coalesce(count(pr.id), 0)::int AS resource_count
         FROM project p
         LEFT JOIN project_resource pr ON pr.project_id = p.id
        WHERE p.workspace_id=$1
        GROUP BY p.id
        ORDER BY p.created_at ASC`,
      [workspaceID],
    )).rows.map((row) => ({ ...row, target_key: targetProjectMatch(row.title) }));

    const resources = (await client.query(
      `SELECT pr.id, pr.project_id, p.title AS project_title, pr.resource_type, pr.resource_ref, pr.label, pr.position, pr.created_at
         FROM project_resource pr
         JOIN project p ON p.id = pr.project_id
        WHERE pr.workspace_id=$1
        ORDER BY p.created_at ASC, pr.position ASC`,
      [workspaceID],
    )).rows;

    const agents = (await client.query(
      `SELECT id, name, model, runtime_mode, runtime_id, archived_at, created_at
         FROM agent
        WHERE workspace_id=$1
        ORDER BY created_at ASC`,
      [workspaceID],
    )).rows.map((row) => ({ ...row, target_key: row.archived_at ? "" : targetAgentMatch(row.name) }));

    const squads = (await client.query(
      `SELECT s.id, s.name, s.leader_id, a.name AS leader_name, s.archived_at, s.created_at,
              coalesce(count(sm.id), 0)::int AS member_count
         FROM squad s
         LEFT JOIN agent a ON a.id = s.leader_id
         LEFT JOIN squad_member sm ON sm.squad_id = s.id
        WHERE s.workspace_id=$1
        GROUP BY s.id, a.name
        ORDER BY s.created_at ASC`,
      [workspaceID],
    )).rows;

    const issueStatus = (await client.query(
      `SELECT status, count(*)::int AS count FROM issue WHERE workspace_id=$1 GROUP BY status ORDER BY status`,
      [workspaceID],
    )).rows;

    const issueSamples = (await client.query(
      `SELECT i.id,
              concat_ws('-', w.issue_prefix, i.number::text) AS identifier,
              i.title,
              i.status,
              i.project_id,
              i.assignee_type,
              i.created_at
         FROM issue i
         JOIN workspace w ON w.id = i.workspace_id
        WHERE workspace_id=$1
        ORDER BY i.created_at DESC
        LIMIT 30`,
      [workspaceID],
    )).rows;

    const evalAssets = (await client.query(
      `SELECT id, name, asset_type, status, created_at
         FROM prompt_evaluation_asset
        WHERE workspace_id=$1
        ORDER BY created_at DESC
        LIMIT 50`,
      [workspaceID],
    )).rows;

    const matrix = [
      {
        id: "R-01",
        requirement: "演示工作区只保留 usercenter/gateway/ida-deployment 三项目",
        current_status: projects.filter((p) => !p.target_key).length === 0 && projects.length === 3 ? "fulfilled" : "false_claimed",
        evidence: { project_count: projects.length, non_target_projects: projects.filter((p) => !p.target_key).map((p) => p.title) },
        cannot_substitute: "项目列表里有旧 E2E 项目或命名不收敛时，不能靠 artifact 声称三项目闭环。",
      },
      {
        id: "R-02",
        requirement: "智能体只保留通用 PM + 01-05 六个角色",
        current_status: agents.filter((a) => !a.archived_at && !a.target_key).length === 0 && agents.filter((a) => !a.archived_at).length === 6 ? "fulfilled" : "false_claimed",
        evidence: {
          active_agent_count: agents.filter((a) => !a.archived_at).length,
          non_target_active_agents: agents.filter((a) => !a.archived_at && !a.target_key).map((a) => a.name),
        },
        cannot_substitute: "项目命名 agent 或历史验收 agent 仍活跃时，不能用 SOP 文档替代真实收敛。",
      },
      {
        id: "R-03",
        requirement: "工蜂仓库添加后有列表/详情/状态/删除，并能被训练引用",
        current_status: resources.some((r) => r.resource_type === "gongfeng_repo") ? "partial" : "missing",
        evidence: {
          gongfeng_resource_count: resources.filter((r) => r.resource_type === "gongfeng_repo").length,
          resource_api_exists: true,
          known_gap: "当前审计只能证明 project_resource 存在；仍需 UI 列表、详情、状态、删除、训练引用真实点击验收。",
        },
        cannot_substitute: "只保存 resource_ref 或 URL 不能算工蜂仓库管理闭环。",
      },
      {
        id: "R-04",
        requirement: "训练与评估必须通过真实 UI 从 issue/run 加入评测到 re-eval",
        current_status: counts.prompt_evaluation_asset > 0 && counts.prompt_evaluation_run > 0 && counts.prompt_evaluation_optimization_candidate > 0 ? "partial" : "missing",
        evidence: {
          prompt_evaluation_asset_count: counts.prompt_evaluation_asset,
          prompt_evaluation_run_count: counts.prompt_evaluation_run,
          optimizer_candidate_count: counts.prompt_evaluation_optimization_candidate,
          known_gap: "旧数据数量不能证明 UI 闭环；必须重跑真实浏览器链路。",
        },
        cannot_substitute: "API-only 创建 eval/candidate 或旧 E2E asset 不能算 UI 闭环。",
      },
      {
        id: "R-05",
        requirement: "旧 E2E 伪造 issues/projects/agents/eval 数据可清理，只保留本轮新验收数据和 canonical demo",
        current_status: counts.issue >= 1 && counts.prompt_evaluation_asset >= 1 ? "fulfilled" : "false_claimed",
        evidence: { issue_count: counts.issue, eval_asset_count: counts.prompt_evaluation_asset, sample_recent_issues: issueSamples },
        cannot_substitute: "旧脏数据让页面看起来丰富不能算产品成熟；把当前 canonical demo 清成 0 也不能算完成。",
      },
    ];

    const cleanupPlan = {
      mode: apply ? "apply" : "dry-run",
      database: dbName,
      workspace,
      destructive_guard: "This script refuses non-multica_goal_test_int databases and the non-ai-studio workspace. Apply mode writes a backup artifact before mutating data.",
      target_state: {
        projects: targetProjects,
        agents: targetAgents,
        workspace_repos: [],
        issues: "delete or archive old generated issues but retain one Goal E Canonical Demo issue for the current web page",
        eval_assets: "delete or archive old generated prompt/eval/optimizer assets but retain Goal E Canonical Demo assets for the current web page",
      },
      candidates: {
        projects_to_remove_or_archive: projects.filter((p) => !p.target_key),
        projects_matching_target: projects.filter((p) => p.target_key),
        agents_to_archive: agents.filter((a) => !a.archived_at && !a.target_key),
        target_like_active_agents: agents.filter((a) => !a.archived_at && a.target_key),
        squads_to_archive_or_rebuild: squads.filter((s) => !s.archived_at),
        workspace_repos_to_clear: workspace.repos || [],
        issues_to_remove_count: counts.issue,
        prompt_evaluation_assets_to_remove_count: counts.prompt_evaluation_asset,
      },
      rollback: {
        strategy: "Before apply, export rows for affected workspace tables into artifact JSON; restore by replaying inserts in dependency order or by restoring DB snapshot.",
        current_artifact_is_dry_run_only: !apply,
      },
    };

    const applyResult = apply ? await applyRemediationCleanup(client, workspace, dbName) : null;
    const currentDataCleanupOk =
      projects.length === 3 &&
      projects.every((p) => Boolean(p.target_key)) &&
      agents.filter((a) => !a.archived_at).length === 6 &&
      agents.filter((a) => !a.archived_at).every((a) => Boolean(a.target_key)) &&
      Array.isArray(workspace.repos) &&
      workspace.repos.length === 0 &&
      counts.issue >= 1 &&
      counts.prompt_evaluation_asset >= 1 &&
      counts.prompt_library_item >= 1;

    const artifact = {
      schema: "multica.remediation.data_governance.v1",
      generated_at: new Date().toISOString(),
      ok: false,
      data_cleanup_ok: applyResult?.ok || currentDataCleanupOk,
      reason: apply
        ? "Wave 0 cleanup baseline was applied; overall remediation remains open until Gongfeng UI, run observability, training/eval loop, clean deploy, and acceptance are verified."
        : "Wave 0 audit intentionally marks previous completion as unproven/false-claimed until cleanup and UI closure are implemented.",
      workspace,
      database: {
        name: dbName,
        url_redacted: redactDatabaseURL(databaseURL),
      },
      counts,
      projects,
      resources,
      agents,
      squads,
      issue_status: issueStatus,
      recent_issue_samples: issueSamples,
      prompt_evaluation_asset_samples: evalAssets,
      original_requirement_matrix: matrix,
      production_gap_matrix: [
        { id: "P0-data-cleanup", status: currentDataCleanupOk ? "fulfilled" : "missing", blocking: true, reason: "旧 E2E 数据必须清理，但当前 canonical demo 不能被清成 0。" },
        { id: "P0-workspace-repo-cleanup", status: Array.isArray(workspace.repos) && workspace.repos.length === 0 ? "fulfilled" : "false_claimed", blocking: true, reason: "workspace.repos 必须为空，避免旧页面 URL 绕过 Gongfeng project_resource 并污染 daemon repo cache。" },
        { id: "P0-gongfeng-repo-management", status: "partial", blocking: true, reason: "有 project_resource 基础，但缺 UI 列表/详情/状态/删除/训练引用完整验收。" },
        { id: "P0-training-ui-loop", status: "partial", blocking: true, reason: "有评测对象和旧 artifact，但缺真实 UI 从 issue/run 到 re-eval 的同链路闭环。" },
        { id: "P0-observability-ia", status: "partial", blocking: true, reason: "旧运行看板证据过窄，需重构无意义展示并用真实任务复核。" },
        { id: "P0-clean-commit-deploy", status: "missing", blocking: true, reason: "当前 int 部署 binary version 为 dirty，不可作为最终证据。" },
      ],
      cleanup_plan: cleanupPlan,
      apply_result: applyResult,
    };

    fs.mkdirSync(artifactDir, { recursive: true });
    const stamp = new Date().toISOString().replace(/[:.]/g, "-");
    const jsonPath = path.join(artifactDir, `remediation-data-cleanup-${stamp}.json`);
    const latestPath = path.join(artifactDir, "remediation-data-cleanup-latest.json");
    fs.writeFileSync(jsonPath, `${JSON.stringify(artifact, null, 2)}\n`);
    fs.writeFileSync(latestPath, `${JSON.stringify(artifact, null, 2)}\n`);
    console.log(JSON.stringify({ ok: artifact.ok, data_cleanup_ok: artifact.data_cleanup_ok, json: jsonPath, latest: latestPath, summary: {
      projects: projects.length,
      non_target_projects: cleanupPlan.candidates.projects_to_remove_or_archive.length,
      active_agents: agents.filter((a) => !a.archived_at).length,
      agents_to_archive: cleanupPlan.candidates.agents_to_archive.length,
      issues: counts.issue,
      eval_assets: counts.prompt_evaluation_asset,
      post_state: applyResult?.post_state || null,
    } }, null, 2));
  } finally {
    await client.end();
  }
}

main().catch((error) => {
  console.error(error.stack || error.message || String(error));
  process.exit(1);
});
