import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const args = new Set(process.argv.slice(2));
const runTests = args.has("--run-tests");
const includeE2E = args.has("--include-e2e") || process.env.ACCEPTANCE_INCLUDE_E2E === "1";
const includeFullE2E = args.has("--include-full-e2e") || process.env.ACCEPTANCE_INCLUDE_FULL_E2E === "1";
const timestamp = new Date().toISOString();
const safeTimestamp = timestamp.replace(/[:.]/g, "-");
const outputDir = path.join(repoRoot, "artifacts", "acceptance");

loadEnvFile(path.join(repoRoot, ".env.worktree"));
loadEnvFile(path.join(repoRoot, ".env.local"));

const frontendURL = trimEnv("ACCEPTANCE_FRONTEND_URL")
  || trimEnv("FRONTEND_URL")
  || `http://127.0.0.1:${trimEnv("FRONTEND_PORT") || "3000"}`;
const apiURL = trimEnv("ACCEPTANCE_API_URL")
  || trimEnv("NEXT_PUBLIC_API_URL")
  || `http://127.0.0.1:${trimEnv("PORT") || "8080"}`;
const browserApiURL = optionalEnv("ACCEPTANCE_BROWSER_API_URL")
  ?? optionalEnv("NEXT_PUBLIC_API_URL")
  ?? apiURL;
const browserWsURL = browserApiURL
  ? `${browserApiURL.replace(/^http/, "ws")}/ws`
  : "/ws";
const workspaceSlug = trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || trimEnv("REAL_AGENT_E2E_WORKSPACE") || "goal-test-daemon";
const demoAccount = trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || trimEnv("REAL_AGENT_E2E_ACCOUNT") || "goal-test-daemon";
const demoPassword = trimEnv("ACCEPTANCE_DEMO_PASSWORD") || trimEnv("REAL_AGENT_E2E_PASSWORD") || "e2e-password";
const realAgentProvider = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER") || "codex";
const realAgentModel = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_MODEL") || "gpt-5.3-codex-spark";
const realAgentFallbackModel = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_FALLBACK_MODEL") || "gpt-5.4-mini";

const health = await probeHTTP(`${apiURL}/health`);
const ready = await probeHTTP(`${apiURL}/readyz`);
const login = await probeHTTP(`${frontendURL}/login`);
const dashboardURL = `${frontendURL}/${encodeURIComponent(workspaceSlug)}/training?view=demo-dashboard`;

const git = {
  head: gitText(["rev-parse", "--short=12", "HEAD"]),
  branch: gitText(["branch", "--show-current"]),
  status: gitText(["status", "--short"]),
  commits: gitText(["log", "--oneline", "-n", trimEnv("ACCEPTANCE_COMMIT_LIMIT") || "12"])
    .split("\n")
    .filter(Boolean),
};

const account = await loadAccountRole();
const databaseEvidence = await loadDatabaseEvidence(account.workspace_id);
const logEvidence = loadLogEvidence();
const commands = buildCommandPlan();
const commandResults = runTests ? runCommands(commands) : commands.map((item) => ({
  ...item,
  status: "未执行",
  exitCode: null,
  durationMs: 0,
  note: "默认只生成验收包；使用 pnpm acceptance:verify 或 --run-tests 后会执行。",
}));

const risks = buildRisks({ health, ready, login, account, commandResults, git, databaseEvidence, logEvidence });
const evidence = {
  "语义版本": "multica.production_acceptance_evidence.v1",
  "生成时间": timestamp,
  "访问地址": {
    "前端": frontendURL,
    "后端": apiURL,
    "登录页": `${frontendURL}/login`,
    "领导演示入口": dashboardURL,
  },
  "演示账号": {
    "账号": demoAccount,
    "密码": demoPassword ? "已配置；仅用于内网验收，正式交付前请轮换" : "未配置",
    "工作区": workspaceSlug,
    "权限": account.role || "未从数据库确认",
    "用户ID": account.user_id || null,
    "工作区ID": account.workspace_id || null,
  },
  "健康检查": {
    health,
    readyz: ready,
    login,
  },
  "提交": git,
  "数据库抽查": databaseEvidence,
  "日志抽查": logEvidence,
  "测试命令": commandResults,
  "剩余风险": risks,
  "人工复核清单": [
    "确认生产看板能导出 multica.production_demo_evidence.v1 JSON。",
    "确认真实 Agent E2E、真实小队 E2E 至少各跑过一次，或明确记录外部模型额度不足。",
    "确认演示账号权限为 owner/admin，且演示后按团队安全要求轮换密码。",
    "确认 /health、/readyz、/login、训练与评估生产看板可从目标网络访问。",
  ],
};

mkdirSync(outputDir, { recursive: true });
const jsonPath = path.join(outputDir, `production-acceptance-${safeTimestamp}.json`);
const mdPath = path.join(outputDir, `production-acceptance-${safeTimestamp}.md`);
writeFileSync(jsonPath, `${JSON.stringify(evidence, null, 2)}\n`);
writeFileSync(mdPath, renderMarkdown(evidence, jsonPath));

console.log(`验收 JSON: ${jsonPath}`);
console.log(`验收 Markdown: ${mdPath}`);
if (commandResults.some((item) => item.status === "失败")) {
  process.exitCode = 1;
}

function trimEnv(name) {
  return (process.env[name] || "").trim();
}

function optionalEnv(name) {
  if (!Object.prototype.hasOwnProperty.call(process.env, name)) return undefined;
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

async function probeHTTP(url) {
  const started = Date.now();
  try {
    const res = await fetch(url, { redirect: "manual" });
    return {
      url,
      status: res.status,
      ok: res.status >= 200 && res.status < 400,
      duration_ms: Date.now() - started,
    };
  } catch (error) {
    return {
      url,
      status: null,
      ok: false,
      duration_ms: Date.now() - started,
      error: error instanceof Error ? error.message : String(error),
    };
  }
}

function gitText(args) {
  try {
    return execFileSync("git", args, { cwd: repoRoot, encoding: "utf8" }).trim();
  } catch (error) {
    return `git ${args.join(" ")} failed: ${error instanceof Error ? error.message : String(error)}`;
  }
}

async function loadAccountRole() {
  const databaseURL = trimEnv("DATABASE_URL");
  if (!databaseURL) return { role: "", note: "DATABASE_URL 未配置，跳过账号权限抽查" };
  try {
    const pg = await import("pg");
    const client = new pg.default.Client(databaseURL);
    await client.connect();
    try {
      const res = await client.query(
        `
          SELECT
            u.id::text AS user_id,
            u.account,
            w.id::text AS workspace_id,
            w.slug AS workspace_slug,
            m.role
          FROM "user" u
          JOIN member m ON m.user_id = u.id
          JOIN workspace w ON w.id = m.workspace_id
          WHERE u.account = $1 AND w.slug = $2
          ORDER BY m.created_at DESC
          LIMIT 1
        `,
        [demoAccount, workspaceSlug],
      );
      return res.rows[0] || { role: "", note: "未找到演示账号在目标工作区的成员记录" };
    } finally {
      await client.end();
    }
  } catch (error) {
    return { role: "", note: error instanceof Error ? error.message : String(error) };
  }
}

async function loadDatabaseEvidence(workspaceID) {
  const databaseURL = trimEnv("DATABASE_URL");
  if (!databaseURL) return { status: "跳过", reason: "DATABASE_URL 未配置" };
  if (!workspaceID) return { status: "跳过", reason: "未确认演示工作区 ID" };
  try {
    const pg = await import("pg");
    const client = new pg.default.Client(databaseURL);
    await client.connect();
    try {
      const trainingSummary = await queryOne(client, `
          SELECT
            (SELECT count(*)::int FROM prompt_library_item WHERE workspace_id = $1) AS prompt_count,
            (SELECT count(*)::int FROM prompt_library_version WHERE workspace_id = $1) AS prompt_version_count,
            (SELECT count(*)::int FROM prompt_evaluation_asset WHERE workspace_id = $1) AS asset_count,
            (SELECT count(*)::int FROM prompt_evaluation_dataset_row WHERE workspace_id = $1) AS dataset_row_count,
            (SELECT count(*)::int FROM prompt_evaluation_test_suite_case WHERE workspace_id = $1) AS test_suite_case_count,
            (SELECT count(*)::int FROM prompt_evaluation_experiment_dimension WHERE workspace_id = $1) AS experiment_dimension_count,
            (SELECT count(*)::int FROM prompt_evaluation_case WHERE workspace_id = $1) AS structured_case_count,
            (SELECT count(*)::int FROM prompt_evaluation_case_assertion WHERE workspace_id = $1) AS structured_assertion_count,
            (SELECT count(*)::int FROM prompt_evaluation_run WHERE workspace_id = $1) AS run_count,
            (SELECT count(*)::int FROM prompt_evaluation_run WHERE workspace_id = $1 AND run_kind = 'Agent执行') AS agent_run_count,
            (SELECT count(*)::int FROM prompt_evaluation_trial WHERE workspace_id = $1) AS trial_count,
            (SELECT count(*)::int FROM prompt_evaluation_optimization_candidate WHERE workspace_id = $1) AS optimization_candidate_count,
            (SELECT count(*)::int FROM prompt_evaluation_evidence_snapshot WHERE workspace_id = $1) AS evidence_snapshot_count,
            COALESCE((SELECT sum(input_tokens)::bigint FROM prompt_evaluation_run WHERE workspace_id = $1), 0)::text AS input_tokens,
            COALESCE((SELECT sum(output_tokens)::bigint FROM prompt_evaluation_run WHERE workspace_id = $1), 0)::text AS output_tokens
        `, [workspaceID]);
      const assetRows = await queryRows(client, `
          SELECT asset_type AS type, count(*)::int AS count
          FROM prompt_evaluation_asset
          WHERE workspace_id = $1
          GROUP BY asset_type
          ORDER BY asset_type ASC
        `, [workspaceID]);
      const runStatusRows = await queryRows(client, `
          SELECT status, count(*)::int AS count
          FROM prompt_evaluation_run
          WHERE workspace_id = $1
          GROUP BY status
          ORDER BY status ASC
        `, [workspaceID]);
      const taskSummary = await queryOne(client, `
          SELECT
            (
              SELECT count(*)::int
              FROM agent_task_queue atq
              JOIN agent a ON a.id = atq.agent_id
              WHERE a.workspace_id = $1
            ) AS task_count,
            (
              SELECT count(*)::int
              FROM task_usage tu
              JOIN agent_task_queue atq ON atq.id = tu.task_id
              JOIN agent a ON a.id = atq.agent_id
              WHERE a.workspace_id = $1
            ) AS usage_rows,
            (
              SELECT count(*)::int
              FROM task_trace_event
              WHERE workspace_id = $1
            ) AS trace_event_rows,
            COALESCE((
              SELECT sum(tu.input_tokens)::bigint
              FROM task_usage tu
              JOIN agent_task_queue atq ON atq.id = tu.task_id
              JOIN agent a ON a.id = atq.agent_id
              WHERE a.workspace_id = $1
            ), 0)::text AS input_tokens,
            COALESCE((
              SELECT sum(tu.output_tokens)::bigint
              FROM task_usage tu
              JOIN agent_task_queue atq ON atq.id = tu.task_id
              JOIN agent a ON a.id = atq.agent_id
              WHERE a.workspace_id = $1
            ), 0)::text AS output_tokens,
            COALESCE((
              SELECT sum(tu.cache_read_tokens)::bigint
              FROM task_usage tu
              JOIN agent_task_queue atq ON atq.id = tu.task_id
              JOIN agent a ON a.id = atq.agent_id
              WHERE a.workspace_id = $1
            ), 0)::text AS cache_read_tokens,
            COALESCE((
              SELECT sum(tu.cache_write_tokens)::bigint
              FROM task_usage tu
              JOIN agent_task_queue atq ON atq.id = tu.task_id
              JOIN agent a ON a.id = atq.agent_id
              WHERE a.workspace_id = $1
            ), 0)::text AS cache_write_tokens
        `, [workspaceID]);
      const runtimeRows = await queryRows(client, `
          SELECT provider, status, count(*)::int AS count, max(last_seen_at)::text AS last_seen_at
          FROM agent_runtime
          WHERE workspace_id = $1
          GROUP BY provider, status
          ORDER BY provider ASC, status ASC
        `, [workspaceID]);
      const squadSummary = await queryOne(client, `
          SELECT
            (SELECT count(*)::int FROM squad WHERE workspace_id = $1 AND archived_at IS NULL) AS squad_count,
            (SELECT count(*)::int FROM squad_sop_run WHERE workspace_id = $1) AS sop_run_count,
            (SELECT count(*)::int FROM squad_sop_step_event WHERE workspace_id = $1) AS sop_event_count,
            (SELECT count(*)::int FROM task_trace_event WHERE workspace_id = $1 AND squad_id IS NOT NULL) AS squad_trace_event_count,
            (
              SELECT count(*)::int
              FROM agent_task_queue atq
              JOIN agent a ON a.id = atq.agent_id
              WHERE a.workspace_id = $1
                AND atq.is_leader_task = true
                AND atq.status = 'completed'
                AND EXISTS (SELECT 1 FROM task_usage tu WHERE tu.task_id = atq.id)
                AND EXISTS (SELECT 1 FROM task_trace_event tte WHERE tte.task_id = atq.id)
            ) AS completed_leader_task_count,
            (
              SELECT count(*)::int
              FROM agent_task_queue atq
              JOIN agent a ON a.id = atq.agent_id
              WHERE a.workspace_id = $1
                AND atq.is_leader_task = true
                AND atq.status = 'failed'
            ) AS failed_leader_task_count
        `, [workspaceID]);
      const latestRuns = await queryRows(client, `
          SELECT
            id::text,
            run_kind,
            status,
            model,
            runtime_provider,
            task_id::text,
            total_cases,
            passed_cases,
            failed_cases,
            input_tokens,
            output_tokens,
            created_at::text
          FROM prompt_evaluation_run
          WHERE workspace_id = $1
          ORDER BY created_at DESC
          LIMIT 5
        `, [workspaceID]);
      const latestEvidenceSnapshots = await queryRows(client, `
          SELECT
            id::text,
            run_id::text,
            snapshot_type,
            schema_version,
            summary->>'运行状态' AS run_status,
            summary->>'失败原因' AS failure_reason,
            summary->>'trace/task id' AS task_id,
            created_at::text
          FROM prompt_evaluation_evidence_snapshot
          WHERE workspace_id = $1
          ORDER BY created_at DESC
          LIMIT 5
        `, [workspaceID]);
      const latestTrace = await queryRows(client, `
          SELECT
            task_id::text,
            source,
            event_type,
            event_name,
            status,
            provider,
            model,
            input_tokens,
            output_tokens,
            failure_reason,
            created_at::text
          FROM task_trace_event
          WHERE workspace_id = $1
          ORDER BY created_at DESC
          LIMIT 8
        `, [workspaceID]);

      return {
        status: "已抽查",
        workspace_id: workspaceID,
        training: trainingSummary,
        assets_by_type: assetRows,
        runs_by_status: runStatusRows,
        tasks: taskSummary,
        runtimes: runtimeRows,
        squads: squadSummary,
        latest_runs: latestRuns,
        latest_evidence_snapshots: latestEvidenceSnapshots,
        latest_trace_events: latestTrace,
      };
    } finally {
      await client.end();
    }
  } catch (error) {
    return { status: "失败", reason: error instanceof Error ? error.message : String(error) };
  }
}

async function queryOne(client, sql, params) {
  const res = await client.query(sql, params);
  return normalizePgRow(res.rows[0] || {});
}

async function queryRows(client, sql, params) {
  const res = await client.query(sql, params);
  return res.rows.map(normalizePgRow);
}

function normalizePgRow(row) {
  return Object.fromEntries(Object.entries(row).map(([key, value]) => [key, value instanceof Date ? value.toISOString() : value]));
}

function loadLogEvidence() {
  const configuredLogPath = trimEnv("ACCEPTANCE_SERVER_LOG");
  const logPath = configuredLogPath
    || (existsSync(path.join(repoRoot, ".run", "server.log"))
      ? path.join(repoRoot, ".run", "server.log")
      : path.join(repoRoot, "artifacts", "logs", "server-goal-test.log"));
  if (!existsSync(logPath)) {
    return { status: "跳过", path: logPath, reason: "未找到服务日志文件" };
  }
  try {
    const content = readFileSync(logPath, "utf8");
    const lines = content.split(/\r?\n/).filter(Boolean);
    const tailLines = lines.slice(-120);
    const errorLines = tailLines.filter((line) => /\b(ERR|ERROR|panic|fatal|bind: address already in use)\b/i.test(line));
    const healthLines = tailLines.filter((line) => line.includes("path=/readyz") || line.includes("path=/health"));
    const daemonLines = tailLines.filter((line) => line.includes("daemon heartbeat") || line.includes("tasks/claim"));
    return {
      status: "已抽查",
      path: logPath,
      line_count: lines.length,
      tail_line_count: tailLines.length,
      error_count: errorLines.length,
      health_line_count: healthLines.length,
      daemon_line_count: daemonLines.length,
      recent_errors: errorLines.slice(-8),
      recent_health: healthLines.slice(-5),
      recent_daemon: daemonLines.slice(-5),
    };
  } catch (error) {
    return { status: "失败", path: logPath, reason: error instanceof Error ? error.message : String(error) };
  }
}

function buildCommandPlan() {
  const commands = [
    {
      name: "Web typecheck",
      command: "pnpm --filter @multica/web typecheck",
      required: true,
      timeoutMs: 180_000,
    },
    {
      name: "Web build",
      command: "pnpm --filter @multica/web build",
      required: true,
      timeoutMs: 240_000,
    },
    {
      name: "重启前端服务",
      command: [
        "set -e",
        ". ./.env.worktree",
        "pkill -f \"next start -p ${FRONTEND_PORT:-3000}\" 2>/dev/null || true",
        "sleep 1",
        "setsid pnpm --dir apps/web exec next start -p \"${FRONTEND_PORT:-3000}\" >> .run/web.log 2>&1 < /dev/null & echo $! > .run/web.pid",
        "for i in $(seq 1 45); do curl --noproxy '*' -fsS \"http://127.0.0.1:${FRONTEND_PORT:-3000}/login\" >/dev/null && break; sleep 1; done",
        "curl --noproxy '*' -fsS \"http://127.0.0.1:${FRONTEND_PORT:-3000}/login\" >/dev/null",
      ].join("; "),
      required: true,
      timeoutMs: 60_000,
    },
    {
      name: "Desktop typecheck",
      command: "pnpm --filter @multica/desktop typecheck",
      required: true,
      timeoutMs: 180_000,
    },
    {
      name: "Core reserved route tests",
      command: "pnpm --filter @multica/core test -- paths/consistency.test.ts",
      required: true,
      timeoutMs: 120_000,
    },
    {
      name: "Web proxy tests",
      command: "pnpm --filter @multica/web test -- proxy.test.ts",
      required: true,
      timeoutMs: 120_000,
    },
    {
      name: "部署浏览器验收 E2E",
      command: [
        `PLAYWRIGHT_BASE_URL=${shellQuote(frontendURL)}`,
        `FRONTEND_ORIGIN=${shellQuote(frontendURL)}`,
        `NEXT_PUBLIC_API_URL=${shellQuote(browserApiURL)}`,
        `NEXT_PUBLIC_WS_URL=${shellQuote(browserWsURL)}`,
        `ACCEPTANCE_API_URL=${shellQuote(apiURL)}`,
        `ACCEPTANCE_FRONTEND_URL=${shellQuote(frontendURL)}`,
        "pnpm exec playwright test e2e/production-acceptance.spec.ts --project=chromium",
      ].join(" "),
      required: true,
      timeoutMs: 180_000,
    },
    {
      name: "导航与命令面板 E2E",
      command: [
        `PLAYWRIGHT_BASE_URL=${shellQuote(frontendURL)}`,
        `FRONTEND_ORIGIN=${shellQuote(frontendURL)}`,
        `NEXT_PUBLIC_API_URL=${shellQuote(browserApiURL)}`,
        `NEXT_PUBLIC_WS_URL=${shellQuote(browserWsURL)}`,
        "pnpm exec playwright test e2e/navigation.spec.ts --project=chromium",
      ].join(" "),
      required: includeE2E,
      skippedByDefault: true,
      timeoutMs: 180_000,
    },
    {
      name: "训练与评估主 E2E",
      command: "pnpm exec playwright test e2e/prompt-library.spec.ts --project=chromium",
      required: includeFullE2E,
      skippedByDefault: true,
      fullE2EOnly: true,
      timeoutMs: 900_000,
    },
    {
      name: "小队 SOP E2E",
      command: "pnpm exec playwright test e2e/squad-sop.spec.ts --project=chromium",
      required: includeFullE2E,
      skippedByDefault: true,
      fullE2EOnly: true,
      timeoutMs: 900_000,
    },
    {
      name: "Codex curl 端到端 Agent/小队验收",
      command: [
        `ACCEPTANCE_API_URL=${shellQuote(apiURL)}`,
        `REAL_AGENT_E2E_WORKSPACE=${shellQuote(workspaceSlug)}`,
        `MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER=${shellQuote(realAgentProvider)}`,
        `MULTICA_PROMPT_EVALUATION_AGENT_MODEL=${shellQuote(realAgentModel)}`,
        "node scripts/codex-squad-curl-e2e.mjs",
        "||",
        `ACCEPTANCE_API_URL=${shellQuote(apiURL)}`,
        `REAL_AGENT_E2E_WORKSPACE=${shellQuote(workspaceSlug)}`,
        `MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER=${shellQuote(realAgentProvider)}`,
        `MULTICA_PROMPT_EVALUATION_AGENT_MODEL=${shellQuote(realAgentFallbackModel)}`,
        "node scripts/codex-squad-curl-e2e.mjs",
      ].join(" "),
      required: includeE2E,
      skippedByDefault: true,
      timeoutMs: 360_000,
    },
    {
      name: "训练与评估 curl 端到端验收",
      command: [
        `ACCEPTANCE_API_URL=${shellQuote(apiURL)}`,
        `REAL_AGENT_E2E_WORKSPACE=${shellQuote(workspaceSlug)}`,
        "node scripts/prompt-evaluation-curl-e2e.mjs",
      ].join(" "),
      required: includeE2E,
      skippedByDefault: true,
      timeoutMs: 120_000,
    },
    {
      name: "真实 Agent E2E",
      command: [
        "RUN_REAL_AGENT_E2E=1",
        `MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER=${shellQuote(realAgentProvider)}`,
        `MULTICA_PROMPT_EVALUATION_AGENT_MODEL=${shellQuote(realAgentModel)}`,
        "pnpm exec playwright test e2e/prompt-library-real-agent.spec.ts --project=chromium",
        "||",
        "RUN_REAL_AGENT_E2E=1",
        `MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER=${shellQuote(realAgentProvider)}`,
        `MULTICA_PROMPT_EVALUATION_AGENT_MODEL=${shellQuote(realAgentFallbackModel)}`,
        "pnpm exec playwright test e2e/prompt-library-real-agent.spec.ts --project=chromium",
      ].join(" "),
      required: includeFullE2E,
      skippedByDefault: true,
      fullE2EOnly: true,
      timeoutMs: 900_000,
    },
    {
      name: "真实小队 Agent E2E",
      command: [
        "RUN_REAL_AGENT_E2E=1",
        `MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER=${shellQuote(realAgentProvider)}`,
        `MULTICA_PROMPT_EVALUATION_AGENT_MODEL=${shellQuote(realAgentModel)}`,
        "pnpm exec playwright test e2e/squad-real-agent.spec.ts --project=chromium",
        "||",
        "RUN_REAL_AGENT_E2E=1",
        `MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER=${shellQuote(realAgentProvider)}`,
        `MULTICA_PROMPT_EVALUATION_AGENT_MODEL=${shellQuote(realAgentFallbackModel)}`,
        "pnpm exec playwright test e2e/squad-real-agent.spec.ts --project=chromium",
      ].join(" "),
      required: includeFullE2E,
      skippedByDefault: true,
      fullE2EOnly: true,
      timeoutMs: 900_000,
    },
  ];
  return commands.filter((item) => {
    if (item.fullE2EOnly) return includeFullE2E;
    return includeE2E || !item.skippedByDefault;
  });
}

function shellQuote(value) {
  return `'${String(value).replace(/'/g, "'\\''")}'`;
}

function runCommands(commands) {
  return commands.map((item) => {
    const started = Date.now();
    const res = spawnSync(item.command, {
      cwd: repoRoot,
      shell: true,
      encoding: "utf8",
      env: process.env,
      maxBuffer: 1024 * 1024 * 16,
      timeout: item.timeoutMs || 300_000,
      killSignal: "SIGTERM",
    });
    const timedOut = res.error?.code === "ETIMEDOUT" || res.signal === "SIGTERM";
    return {
      ...item,
      status: timedOut ? "超时" : res.status === 0 ? "通过" : "失败",
      exitCode: res.status,
      signal: res.signal || null,
      durationMs: Date.now() - started,
      timeoutMs: item.timeoutMs || 300_000,
      summary: summarizeCommandOutput(item.name, res.stdout),
      stdout_tail: tail(res.stdout),
      stderr_tail: tail(res.stderr),
    };
  });
}

function summarizeCommandOutput(name, stdout) {
  const parsed = parseLastJSONObject(stdout);
  if (name === "Codex curl 端到端 Agent/小队验收") {
    if (!parsed) return { status: "未解析", reason: "stdout 中未找到 JSON 对象" };
    return {
      result: parsed.result,
      task_status: parsed.task?.status || "",
      task_id: parsed.task?.id || "",
      runtime_id: parsed.runtime?.id || "",
      runtime_provider: parsed.runtime?.provider || "",
      agent_id: parsed.agent?.id || "",
      agent_model: parsed.agent?.model || "",
      issue_id: parsed.issue?.id || "",
      trace_event_count: parsed.trace_event_count ?? 0,
      message_count: parsed.message_count ?? 0,
      total_input_tokens: parsed.usage?.total_input_tokens ?? 0,
      total_output_tokens: parsed.usage?.total_output_tokens ?? 0,
      external_dependency_failure: parsed.external_dependency_failure === true,
    };
  }
  if (name === "训练与评估 curl 端到端验收") {
    if (!parsed) return { status: "未解析", reason: "stdout 中未找到 JSON 对象" };
    return {
      result: parsed.result,
      prompt_id: parsed.prompt?.id || "",
      prompt_version_count: parsed.prompt?.version_count ?? 0,
      dataset_id: parsed.dataset?.id || "",
      dataset_row_count: parsed.dataset?.dataset_row_count ?? 0,
      test_suite_id: parsed.test_suite?.id || "",
      test_suite_case_count: parsed.test_suite?.test_suite_case_count ?? 0,
      experiment_id: parsed.experiment?.id || "",
      experiment_dimension_count: parsed.experiment?.experiment_dimension_count ?? 0,
      dataset_assertion_count: parsed.dataset?.assertion_count ?? 0,
      test_suite_assertion_count: parsed.test_suite?.assertion_count ?? 0,
      run_id: parsed.run?.id || "",
      run_status: parsed.run?.status || "",
      failed_cases: parsed.run?.failed_cases ?? 0,
      optimization_candidate_id: parsed.optimization_candidate?.id || "",
      optimization_candidate_status: parsed.optimization_candidate?.status || "",
      published_prompt_id: parsed.published_prompt?.id || "",
      published_prompt_version: parsed.published_prompt?.version ?? 0,
      published_prompt_version_count: parsed.published_prompt?.version_count ?? 0,
    };
  }
  return null;
}

function parseLastJSONObject(stdout) {
  const text = String(stdout || "").trim();
  for (let index = text.lastIndexOf("{"); index >= 0; index = text.lastIndexOf("{", index - 1)) {
    try {
      return JSON.parse(text.slice(index));
    } catch {
      // Continue scanning; command wrappers may print log lines before JSON.
    }
  }
  return null;
}

function tail(value) {
  const lines = String(value || "").trim().split(/\r?\n/).filter(Boolean);
  return lines.slice(-30);
}

function buildRisks({ health, ready, login, account, commandResults, git, databaseEvidence, logEvidence }) {
  const risks = [];
  if (!health.ok) risks.push("后端 /health 未通过，不能作为可演示服务交付。");
  if (!ready.ok) risks.push("后端 /readyz 未通过，依赖或数据库连接可能未就绪。");
  if (!login.ok) risks.push("前端 /login 未通过，演示账号无法完成浏览器登录验收。");
  if (!["owner", "admin"].includes(String(account.role || ""))) {
    risks.push("演示账号权限未确认达到 owner/admin；领导演示前需要确认最高权限。");
  }
  if (commandResults.some((item) => item.status !== "通过")) {
    risks.push("仍存在未执行或失败的测试命令；正式交付前需跑 acceptance:verify 并保留报告。");
  }
  if (databaseEvidence.status !== "已抽查") {
    risks.push(`数据库结果抽查未完成：${databaseEvidence.reason || databaseEvidence.status}`);
  } else {
    const training = databaseEvidence.training || {};
    const squads = databaseEvidence.squads || {};
    const tasks = databaseEvidence.tasks || {};
    if (Number(training.run_count || 0) === 0) risks.push("数据库中未发现训练评估运行记录，生产看板会缺少运行证据。");
    if (Number(training.dataset_row_count || 0) === 0) risks.push("数据库中未发现数据集行事实表记录，数据集仍缺少可度量行级证据。");
    if (Number(training.test_suite_case_count || 0) === 0) risks.push("数据库中未发现测试套件用例事实表记录，测试套件仍缺少可度量用例证据。");
    if (Number(training.experiment_dimension_count || 0) === 0) risks.push("数据库中未发现实验维度事实表记录，实验模块仍缺少可度量对比证据。");
    if (Number(training.structured_case_count || 0) === 0) risks.push("数据库中未发现结构化评测用例，数据集/测试套件证据不足。");
    if (Number(training.structured_assertion_count || 0) === 0) risks.push("数据库中未发现结构化评测断言，训练评估可度量证据不足。");
    if (Number(training.optimization_candidate_count || 0) === 0) risks.push("数据库中未发现优化候选，失败用例到人工确认的闭环证据不足。");
    if (Number(training.evidence_snapshot_count || 0) === 0) risks.push("数据库中未发现服务端运行证据快照，领导演示缺少可复核归档。");
    if (Number(tasks.trace_event_rows || 0) === 0) risks.push("数据库中未发现任务 trace 事件，观测闭环证据不足。");
    if (Number(squads.sop_run_count || 0) === 0) risks.push("数据库中未发现小队 SOP run，小队闭环证据不足。");
    if (Number(squads.completed_leader_task_count || 0) === 0) risks.push("数据库中未发现已完成且带 usage/trace 的小队队长任务；小队闭环仍受模型额度或 runtime 成功率影响。");
  }
  if (logEvidence.status !== "已抽查") {
    risks.push(`服务日志抽查未完成：${logEvidence.reason || logEvidence.status}`);
  } else if (Number(logEvidence.error_count || 0) > 0) {
    risks.push(`服务日志最近片段存在 ${logEvidence.error_count} 条错误，需要排查后再演示。`);
  }
  if (git.status) risks.push("当前 worktree 非干净状态；提交前需要确认没有未纳入验收的改动。");
  if (risks.length === 0) {
    risks.push("无代码侧已知阻塞；外部风险仅剩模型额度、网络访问和演示账号密码轮换。");
  }
  return risks;
}

function renderMarkdown(data, jsonPath) {
  const commandRows = data["测试命令"].map((item) => `| ${item.name} | \`${item.command}\` | ${item.status} | ${item.durationMs} |`).join("\n");
  const commitRows = data["提交"].commits.map((line) => `- ${line}`).join("\n");
  const riskRows = data["剩余风险"].map((line) => `- ${line}`).join("\n");
  const db = data["数据库抽查"];
  const logs = data["日志抽查"] || {};
  const dbStatus = db.status === "已抽查" ? "已抽查" : `${db.status}：${db.reason || "无原因"}`;
  const logStatus = logs.status === "已抽查" ? "已抽查" : `${logs.status || "未记录"}：${logs.reason || "无原因"}`;
  const training = db.training || {};
  const squads = db.squads || {};
  const tasks = db.tasks || {};
  const latestRunRows = (db.latest_runs || []).map((run) => `- ${run.created_at} · ${run.run_kind} · ${run.status} · task ${run.task_id || "无"} · ${run.model || "无模型"}`).join("\n") || "- 无";
  const latestSnapshotRows = (db.latest_evidence_snapshots || []).map((snapshot) => `- ${snapshot.created_at} · ${snapshot.snapshot_type} · run ${snapshot.run_id} · ${snapshot.run_status || "未知"} · task ${snapshot.task_id || "无"}`).join("\n") || "- 无";
  const latestTraceRows = (db.latest_trace_events || []).map((event) => `- ${event.created_at} · ${event.event_name || event.event_type} · ${event.status} · task ${event.task_id || "无"}`).join("\n") || "- 无";
  return `# Multica 生产验收证据

- 生成时间：${data["生成时间"]}
- JSON 证据：\`${jsonPath}\`
- 前端：${data["访问地址"]["前端"]}
- 后端：${data["访问地址"]["后端"]}
- 领导演示入口：${data["访问地址"]["领导演示入口"]}

## 演示账号

- 账号：\`${data["演示账号"]["账号"]}\`
- 密码：${data["演示账号"]["密码"]}
- 工作区：\`${data["演示账号"]["工作区"]}\`
- 权限：${data["演示账号"]["权限"]}

## 健康检查

- /health：${data["健康检查"].health.ok ? "通过" : "失败"} (${data["健康检查"].health.status ?? "无状态"})
- /readyz：${data["健康检查"].readyz.ok ? "通过" : "失败"} (${data["健康检查"].readyz.status ?? "无状态"})
- /login：${data["健康检查"].login.ok ? "通过" : "失败"} (${data["健康检查"].login.status ?? "无状态"})

## 提交

- 分支：${data["提交"].branch}
- HEAD：${data["提交"].head}

${commitRows}

## 数据库抽查

- 状态：${dbStatus}
- 提示词：${training.prompt_count ?? "未记录"}
- 评测资产：${training.asset_count ?? "未记录"}
- 数据集行：${training.dataset_row_count ?? "未记录"}
- 测试套件用例：${training.test_suite_case_count ?? "未记录"}
- 实验维度事实：${training.experiment_dimension_count ?? "未记录"}
- 结构化用例：${training.structured_case_count ?? "未记录"}
- 结构化断言：${training.structured_assertion_count ?? "未记录"}
- 训练评估运行：${training.run_count ?? "未记录"}，其中 Agent 执行 ${training.agent_run_count ?? "未记录"}
- trial：${training.trial_count ?? "未记录"}
- 优化候选：${training.optimization_candidate_count ?? "未记录"}
- 服务端证据快照：${training.evidence_snapshot_count ?? "未记录"}
- 任务：${tasks.task_count ?? "未记录"}，usage 行 ${tasks.usage_rows ?? "未记录"}，trace 事件 ${tasks.trace_event_rows ?? "未记录"}
- 小队：${squads.squad_count ?? "未记录"}，SOP run ${squads.sop_run_count ?? "未记录"}，SOP 事件 ${squads.sop_event_count ?? "未记录"}，已完成队长任务 ${squads.completed_leader_task_count ?? "未记录"}，失败队长任务 ${squads.failed_leader_task_count ?? "未记录"}

## 日志抽查

- 状态：${logStatus}
- 文件：\`${logs.path || "未记录"}\`
- 最近错误：${logs.error_count ?? "未记录"}
- 最近健康检查日志：${logs.health_line_count ?? "未记录"}
- 最近 daemon 日志：${logs.daemon_line_count ?? "未记录"}

### 最近训练评估运行

${latestRunRows}

### 最近服务端证据快照

${latestSnapshotRows}

### 最近 trace 事件

${latestTraceRows}

## 测试命令

| 名称 | 命令 | 状态 | 耗时 ms |
| --- | --- | --- | ---: |
${commandRows}

## 剩余风险

${riskRows}
`;
}
