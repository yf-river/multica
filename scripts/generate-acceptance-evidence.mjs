import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const args = new Set(process.argv.slice(2));
const runTests = args.has("--run-tests");
const includeE2E = args.has("--include-e2e") || process.env.ACCEPTANCE_INCLUDE_E2E === "1";
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
const workspaceSlug = trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || trimEnv("REAL_AGENT_E2E_WORKSPACE") || "goal-test-daemon";
const demoAccount = trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || trimEnv("REAL_AGENT_E2E_ACCOUNT") || "goal-test-daemon";
const demoPassword = trimEnv("ACCEPTANCE_DEMO_PASSWORD") || trimEnv("REAL_AGENT_E2E_PASSWORD") || "e2e-password";

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
const commands = buildCommandPlan();
const commandResults = runTests ? runCommands(commands) : commands.map((item) => ({
  ...item,
  status: "未执行",
  exitCode: null,
  durationMs: 0,
  note: "默认只生成验收包；使用 pnpm acceptance:verify 或 --run-tests 后会执行。",
}));

const risks = buildRisks({ health, ready, login, account, commandResults, git });
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

function buildCommandPlan() {
  const commands = [
    {
      name: "Web typecheck",
      command: "pnpm --filter @multica/web typecheck",
      required: true,
    },
    {
      name: "Web build",
      command: "pnpm --filter @multica/web build",
      required: true,
    },
    {
      name: "Desktop typecheck",
      command: "pnpm --filter @multica/desktop typecheck",
      required: true,
    },
    {
      name: "Core reserved route tests",
      command: "pnpm --filter @multica/core test -- paths/consistency.test.ts",
      required: true,
    },
    {
      name: "Web proxy tests",
      command: "pnpm --filter @multica/web test -- proxy.test.ts",
      required: true,
    },
    {
      name: "训练与评估主 E2E",
      command: "pnpm exec playwright test e2e/prompt-library.spec.ts --project=chromium",
      required: includeE2E,
      skippedByDefault: true,
    },
    {
      name: "小队 SOP E2E",
      command: "pnpm exec playwright test e2e/squad-sop.spec.ts --project=chromium",
      required: includeE2E,
      skippedByDefault: true,
    },
    {
      name: "真实 Agent E2E",
      command: "RUN_REAL_AGENT_E2E=1 pnpm exec playwright test e2e/prompt-library-real-agent.spec.ts --project=chromium",
      required: includeE2E,
      skippedByDefault: true,
    },
    {
      name: "真实小队 Agent E2E",
      command: "RUN_REAL_AGENT_E2E=1 pnpm exec playwright test e2e/squad-real-agent.spec.ts --project=chromium",
      required: includeE2E,
      skippedByDefault: true,
    },
  ];
  return commands.filter((item) => includeE2E || !item.skippedByDefault);
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
    });
    return {
      ...item,
      status: res.status === 0 ? "通过" : "失败",
      exitCode: res.status,
      durationMs: Date.now() - started,
      stdout_tail: tail(res.stdout),
      stderr_tail: tail(res.stderr),
    };
  });
}

function tail(value) {
  const lines = String(value || "").trim().split(/\r?\n/).filter(Boolean);
  return lines.slice(-30);
}

function buildRisks({ health, ready, login, account, commandResults, git }) {
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

## 测试命令

| 名称 | 命令 | 状态 | 耗时 ms |
| --- | --- | --- | ---: |
${commandRows}

## 剩余风险

${riskRows}
`;
}
