import { chromium } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const env = {
  ...process.env,
  ...readEnvFile(path.join(repoRoot, ".run/env/goal-test-int.env")),
};

const browserURL = trimSlash(process.env.GOAL_TEST_BROWSER_URL || `http://127.0.0.1:${env.FRONTEND_PORT || "13682"}`);
const frontendURL = trimSlash(process.env.GOAL_TEST_FRONTEND_URL || env.FRONTEND_ORIGIN || "http://9.134.129.162:13682");
const backendURL = trimSlash(process.env.GOAL_TEST_BACKEND_URL || env.REMOTE_API_URL || "http://127.0.0.1:18762");
const workspaceSlug = process.env.GOAL_TEST_WORKSPACE_SLUG || "goal-test-daemon";
const account = process.env.GOAL_TEST_ACCOUNT || "goal-test-daemon";
const password = process.env.GOAL_TEST_PASSWORD || "e2e-password";
const artifactRoot = path.resolve(process.env.GOAL_TEST_PLAYGROUND_AUDIT_DIR || path.join(repoRoot, "artifacts/acceptance"));
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");
const screenshotDir = path.join(artifactRoot, `playground-difference-audit-${stamp}`);

process.env.NO_PROXY = mergeNoProxy(process.env.NO_PROXY || process.env.no_proxy || "", [browserURL, frontendURL, backendURL]);
process.env.no_proxy = process.env.NO_PROXY;
mkdirSync(screenshotDir, { recursive: true });

const token = await login();
const browser = await chromium.launch({ headless: true, args: ["--no-proxy-server", "--proxy-server=direct://", "--proxy-bypass-list=*"] });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, ignoreHTTPSErrors: true });
await context.addCookies([{ name: "multica_logged_in", value: "1", url: browserURL, sameSite: "Lax" }]);
await context.addInitScript((authToken) => {
  localStorage.setItem("multica_token", authToken);
  localStorage.setItem("multica:chat:isOpen", "false");
}, token);

const page = await context.newPage();
const prompt = await auditPromptPlayground(page);
const agent = await auditAgentPlayground(page);
await browser.close();

const surfaceSimilarity = compareSurfaceSimilarity(prompt, agent);
const failures = [
  ...prompt.failures.map((failure) => `提示词调试场：${failure}`),
  ...agent.failures.map((failure) => `智能体调试场：${failure}`),
  ...crossSurfaceFailures(prompt, agent, surfaceSimilarity),
];
const payload = {
  schema: "multica.goal_test.playground_difference_audit.v1",
  generated_at: generatedAt,
  frontend_url: frontendURL,
  browser_url: browserURL,
  backend_url: backendURL,
  workspace_slug: workspaceSlug,
  account,
  ok: failures.length === 0,
  failures,
  surface_similarity: surfaceSimilarity,
  screenshots: {
    prompt_playground: prompt.screenshot,
    agent_playground: agent.screenshot,
  },
  prompt_playground: prompt,
  agent_playground: agent,
};

const jsonPath = path.join(artifactRoot, `playground-difference-audit-${stamp}.json`);
const markdownPath = path.join(artifactRoot, `playground-difference-audit-${stamp}.md`);
writeFileSync(jsonPath, `${JSON.stringify(payload, null, 2)}\n`);
writeFileSync(markdownPath, renderMarkdown(payload));
writeFileSync(path.join(artifactRoot, "playground-difference-audit-latest.json"), `${JSON.stringify(payload, null, 2)}\n`);
writeFileSync(path.join(artifactRoot, "playground-difference-audit-summary.md"), renderMarkdown(payload));

console.log(JSON.stringify({ ok: payload.ok, json: jsonPath, markdown: markdownPath, screenshots: payload.screenshots, failures }, null, 2));
if (!payload.ok) process.exitCode = 1;

async function auditPromptPlayground(page) {
  const url = `${browserURL}/${workspaceSlug}/training/prompt-playground`;
  await page.goto(url, { waitUntil: "domcontentloaded", timeout: 15_000 });
  await page.waitForSelector('[data-testid="prompt-playground-page-shell"]', { timeout: 10_000 });
  await page.waitForSelector('[data-testid="prompt-playground-template-lab"]', { timeout: 10_000 });
  const screenshot = path.join(screenshotDir, "prompt-playground.png");
  await page.screenshot({ path: screenshot, fullPage: true });
  const boxes = await boxesFor(page, {
    shell: "prompt-playground-page-shell",
    workbench: "prompt-playground-workbench",
    promptList: "prompt-playground-prompt-list",
    templateLab: "prompt-playground-template-lab",
    sourcePanel: "prompt-playground-source-panel",
    variableChecklist: "prompt-playground-variable-checklist",
    renderedOutput: "prompt-playground-rendered-output",
    inspectionBoard: "prompt-playground-inspection-board",
    templateCard: "prompt-playground-template-card",
    qualityGate: "prompt-playground-quality-gate",
  });
  const text = await page.locator("body").innerText({ timeout: 5_000 });
  const forbidden = await countsFor(page, ["agent-playground-run-console", "agent-playground-task-payload", "agent-playground-task-pipeline"]);
  const failures = [
    ...requireText(text, ["本地渲染 · 不启动智能体", "本地模板目录", "质检工作单", "模板质检台", "模板源", "变量样本", "保存质检记录", "质检结论"]),
    ...nonZeroBoxFailures(boxes, ["shell", "workbench", "promptList", "inspectionBoard", "templateCard", "templateLab", "sourcePanel", "variableChecklist", "renderedOutput", "qualityGate"]),
    ...forbiddenCountFailures(forbidden),
  ];
  if (boxes.promptList && boxes.templateLab && boxes.promptList.x >= boxes.templateLab.x) {
    failures.push("模板列表应在模板质检台左侧");
  }
  if (boxes.sourcePanel && boxes.variableChecklist && boxes.renderedOutput) {
    const sourceLeft = boxes.sourcePanel.x;
    const variableLeft = boxes.variableChecklist.x;
    const outputLeft = boxes.renderedOutput.x;
    if (!(sourceLeft < variableLeft && variableLeft < outputLeft)) {
      failures.push("模板源、变量样本、渲染文本应形成三栏质检台");
    }
  }
  return {
    url,
    final_url: page.url(),
    ok: failures.length === 0,
    failures,
    screenshot,
    boxes,
    forbidden_counts: forbidden,
    visible_contracts: pickVisibleLines(text, ["本地渲染", "本地模板目录", "质检工作单", "模板质检", "模板源", "变量样本", "质检结论", "真实任务"]),
    surface_signature: buildPromptSurfaceSignature(boxes),
  };
}

async function auditAgentPlayground(page) {
  const url = `${browserURL}/${workspaceSlug}/training/agent-playground`;
  await page.goto(url, { waitUntil: "domcontentloaded", timeout: 15_000 });
  await page.waitForSelector('[data-testid="agent-playground-page-shell"]', { timeout: 10_000 });
  await page.waitForSelector('[data-testid="agent-playground-run-console"]', { timeout: 10_000 });
  const screenshot = path.join(screenshotDir, "agent-playground.png");
  await page.screenshot({ path: screenshot, fullPage: true });
  const boxes = await boxesFor(page, {
    shell: "agent-playground-page-shell",
    workbench: "agent-playground-workbench",
    executionStage: "agent-playground-execution-stage",
    promptList: "agent-playground-prompt-list",
    targetQueue: "agent-playground-target-queue",
    targetQueueItem: "agent-playground-target-queue-item",
    runConsole: "agent-playground-run-console",
    executionTopology: "agent-playground-execution-topology",
    executionBus: "agent-playground-execution-bus",
    readinessLane: "agent-playground-readiness-lane",
    configComparison: "agent-playground-config-comparison",
    modelMatrix: "agent-playground-model-matrix",
    toolEnvDiff: "agent-playground-tool-env-diff",
    runComparison: "agent-playground-run-comparison",
    taskPayload: "agent-playground-task-payload",
    observabilityContract: "agent-playground-observability-contract",
  });
  const text = await page.locator("body").innerText({ timeout: 5_000 });
  const forbidden = await countsFor(page, ["prompt-playground-template-lab", "prompt-playground-source-panel", "prompt-playground-variable-checklist"]);
  const failures = [
    ...requireText(text, ["真实任务 · 写回观测证据", "真实任务发射台", "执行目标池", "入队目标", "创建真实任务", "执行节点", "Trace", "用量", "执行配置对比", "模型参数矩阵", "工具与环境差异", "MCP", "环境变量", "最近运行横向对比", "观测回写契约"]),
    ...nonZeroBoxFailures(boxes, ["shell", "workbench", "executionStage", "promptList", "targetQueue", "targetQueueItem", "runConsole", "executionTopology", "executionBus", "readinessLane", "configComparison", "modelMatrix", "toolEnvDiff", "runComparison", "taskPayload", "observabilityContract"]),
    ...forbiddenCountFailures(forbidden),
  ];
  if (boxes.executionStage && boxes.promptList && boxes.executionStage.x >= boxes.promptList.x) {
    failures.push("执行控制台应在目标池左侧");
  }
  if (boxes.runConsole && boxes.taskPayload && boxes.runConsole.y >= boxes.taskPayload.y) {
    failures.push("真实任务发射台应先于任务载荷区域");
  }
  return {
    url,
    final_url: page.url(),
    ok: failures.length === 0,
    failures,
    screenshot,
    boxes,
    forbidden_counts: forbidden,
    visible_contracts: pickVisibleLines(text, ["真实任务", "执行目标池", "入队目标", "执行节点", "Trace", "用量", "执行配置", "模型参数", "工具与环境", "MCP", "环境变量", "横向对比", "观测回写", "本地渲染", "模板源"]),
    surface_signature: buildAgentSurfaceSignature(boxes),
  };
}

function crossSurfaceFailures(prompt, agent, similarity) {
  const failures = [];
  const promptHasThreeColumns =
    prompt.boxes.sourcePanel &&
    prompt.boxes.variableChecklist &&
    prompt.boxes.renderedOutput &&
    prompt.boxes.sourcePanel.x < prompt.boxes.variableChecklist.x &&
    prompt.boxes.variableChecklist.x < prompt.boxes.renderedOutput.x;
  const agentHasRightTargetPool =
    agent.boxes.executionStage &&
    agent.boxes.promptList &&
    agent.boxes.executionStage.x < agent.boxes.promptList.x;
  if (!promptHasThreeColumns) failures.push("提示词调试场没有形成模板源/变量样本/渲染文本三栏结构");
  if (!agentHasRightTargetPool) failures.push("智能体调试场没有形成左执行控制台/右目标池结构");
  if (prompt.boxes.workbench && agent.boxes.workbench && Math.abs(prompt.boxes.workbench.width - agent.boxes.workbench.width) < 4) {
    // Width can be similar because both are full-width shells; the inner layout must differ.
    const promptInnerColumns = [prompt.boxes.promptList?.width, prompt.boxes.templateLab?.width].filter(Boolean).join("/");
    const agentInnerColumns = [agent.boxes.executionStage?.width, agent.boxes.promptList?.width].filter(Boolean).join("/");
    if (promptInnerColumns === agentInnerColumns) failures.push("两个调试场内层列宽完全相同，容易退化成同构页面");
  }
  if (prompt.surface_signature?.prompt_list_side === agent.surface_signature?.prompt_list_side) {
    failures.push("两个调试场的目标列表位于同侧，首屏结构容易同构");
  }
  if (prompt.surface_signature?.primary_pattern === agent.surface_signature?.primary_pattern) {
    failures.push(`两个调试场主工作区结构签名相同：${prompt.surface_signature.primary_pattern}`);
  }
  if (similarity.visible_contract_jaccard > 0.45) {
    failures.push(`两个调试场可见契约文本相似度过高：${similarity.visible_contract_jaccard}`);
  }
  return failures;
}

function compareSurfaceSimilarity(prompt, agent) {
  const promptTokens = visibleContractTokens(prompt.visible_contracts);
  const agentTokens = visibleContractTokens(agent.visible_contracts);
  const shared = promptTokens.filter((token) => agentTokens.includes(token));
  const union = Array.from(new Set([...promptTokens, ...agentTokens]));
  return {
    visible_contract_jaccard: union.length === 0 ? 0 : roundNumber(shared.length / union.length, 3),
    shared_tokens: shared.slice(0, 20),
    prompt_signature: prompt.surface_signature,
    agent_signature: agent.surface_signature,
  };
}

function visibleContractTokens(lines) {
  const stopWords = new Set(["的", "和", "与", "在", "到", "为", "不", "已", "可", "个", "条"]);
  return Array.from(
    new Set(
      lines
        .join("\n")
        .split(/[^\p{Script=Han}A-Za-z0-9_]+/u)
        .map((token) => token.trim())
        .filter((token) => token.length >= 2 && !stopWords.has(token)),
    ),
  );
}

function buildPromptSurfaceSignature(boxes) {
  return {
    prompt_list_side: boxes.promptList && boxes.templateLab && boxes.promptList.x < boxes.templateLab.x ? "left" : "unknown",
    primary_pattern: "left-list-three-column-local-lab",
    exclusive_regions: ["prompt-playground-template-lab", "prompt-playground-source-panel", "prompt-playground-variable-checklist", "prompt-playground-quality-gate"],
  };
}

function buildAgentSurfaceSignature(boxes) {
  return {
    prompt_list_side: boxes.executionStage && boxes.promptList && boxes.executionStage.x < boxes.promptList.x ? "right" : "unknown",
    primary_pattern: "left-execution-console-right-target-pool",
    exclusive_regions: ["agent-playground-run-console", "agent-playground-execution-bus", "agent-playground-task-payload", "agent-playground-tool-env-diff"],
  };
}

async function boxesFor(page, ids) {
  const result = {};
  for (const [key, id] of Object.entries(ids)) {
    const box = await page.getByTestId(id).first().boundingBox().catch(() => null);
    result[key] = box ? roundBox(box) : null;
  }
  return result;
}

async function countsFor(page, ids) {
  const result = {};
  for (const id of ids) {
    result[id] = await page.getByTestId(id).count();
  }
  return result;
}

function nonZeroBoxFailures(boxes, keys) {
  return keys.flatMap((key) => {
    const box = boxes[key];
    if (!box) return [`缺少布局区域 ${key}`];
    if (box.width <= 0 || box.height <= 0) return [`布局区域 ${key} 尺寸无效`];
    return [];
  });
}

function forbiddenCountFailures(counts) {
  return Object.entries(counts)
    .filter(([, count]) => count !== 0)
    .map(([id, count]) => `不应出现 ${id}，实际 ${count} 个`);
}

function requireText(text, required) {
  return required.filter((item) => !text.includes(item)).map((item) => `缺少页面文本：${item}`);
}

function pickVisibleLines(text, keywords) {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean)
    .filter((line) => keywords.some((keyword) => line.includes(keyword)))
    .slice(0, 20);
}

function roundBox(box) {
  return {
    x: Math.round(box.x),
    y: Math.round(box.y),
    width: Math.round(box.width),
    height: Math.round(box.height),
  };
}

function roundNumber(value, digits) {
  const scale = 10 ** digits;
  return Math.round(value * scale) / scale;
}

async function login() {
  const response = await fetch(`${backendURL}/auth/login`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ account, password }),
  });
  if (!response.ok) {
    throw new Error(`login failed: ${response.status} ${await response.text()}`);
  }
  const data = await response.json();
  if (!data.token) throw new Error("login response did not include token");
  return data.token;
}

function renderMarkdown(payload) {
  const lines = [
    "# 调试场差异审计",
    "",
    `生成时间：${payload.generated_at}`,
    `浏览器入口：${payload.browser_url}`,
    `工作区：${payload.workspace_slug}`,
    `结论：${payload.ok ? "通过" : "未通过"}`,
    "",
    "## 截图",
    "",
    `- 提示词调试场：${payload.screenshots.prompt_playground}`,
    `- 智能体调试场：${payload.screenshots.agent_playground}`,
    "",
    "## 结构差异",
    "",
    `- 提示词调试场：${payload.prompt_playground.visible_contracts.join("；")}`,
    `- 智能体调试场：${payload.agent_playground.visible_contracts.join("；")}`,
    `- 可见契约文本相似度：${payload.surface_similarity.visible_contract_jaccard}`,
    `- 提示词调试场结构签名：${payload.prompt_playground.surface_signature.primary_pattern}，列表位置 ${payload.prompt_playground.surface_signature.prompt_list_side}`,
    `- 智能体调试场结构签名：${payload.agent_playground.surface_signature.primary_pattern}，列表位置 ${payload.agent_playground.surface_signature.prompt_list_side}`,
    payload.surface_similarity.shared_tokens.length > 0 ? `- 共享词：${payload.surface_similarity.shared_tokens.join("、")}` : "- 共享词：无",
    "",
  ];
  if (payload.failures.length > 0) {
    lines.push("## 阻断项", "");
    for (const failure of payload.failures) lines.push(`- ${failure}`);
    lines.push("");
  }
  lines.push("## 布局指标", "");
  lines.push("### 提示词调试场", "");
  for (const [key, box] of Object.entries(payload.prompt_playground.boxes)) {
    lines.push(`- ${key}：${box ? `${box.x},${box.y},${box.width},${box.height}` : "缺失"}`);
  }
  lines.push("", "### 智能体调试场", "");
  for (const [key, box] of Object.entries(payload.agent_playground.boxes)) {
    lines.push(`- ${key}：${box ? `${box.x},${box.y},${box.width},${box.height}` : "缺失"}`);
  }
  return `${lines.join("\n")}\n`;
}

function readEnvFile(file) {
  try {
    return Object.fromEntries(
      readFileSync(file, "utf8")
        .split(/\r?\n/)
        .map((line) => line.trim())
        .filter((line) => line && !line.startsWith("#") && line.includes("="))
        .map((line) => {
          const index = line.indexOf("=");
          return [line.slice(0, index), line.slice(index + 1)];
        }),
    );
  } catch {
    return {};
  }
}

function trimSlash(value) {
  return value.replace(/\/+$/, "");
}

function mergeNoProxy(current, urls) {
  const hosts = new Set(
    String(current || "")
      .split(",")
      .map((item) => item.trim())
      .filter(Boolean),
  );
  for (const url of urls) {
    try {
      const parsed = new URL(url);
      if (parsed.hostname) hosts.add(parsed.hostname);
    } catch {
      // Ignore non-URL values.
    }
  }
  return Array.from(hosts).join(",");
}
