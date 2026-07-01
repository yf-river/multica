import { execFile } from "node:child_process";
import { readFileSync } from "node:fs";
import fs from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { promisify } from "node:util";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const execFileAsync = promisify(execFile);
const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = acceptanceDir(repoRoot);
const env = {
  ...readEnvFile(path.join(repoRoot, ".run/env/goal-test-int.env")),
  ...process.env,
};

const apiBase = trimSlash(env.GOAL_TEST_BACKEND_URL || env.REMOTE_API_URL || env.NEXT_PUBLIC_API_URL || "http://127.0.0.1:18762");
const account = env.GOAL_TEST_ACCOUNT || env.E2E_ACCOUNT || "goal-test-daemon";
const password = env.GOAL_TEST_PASSWORD || env.E2E_PASSWORD || "e2e-password";
const workspaceSlug = env.GOAL_TEST_WORKSPACE_SLUG || env.E2E_WORKSPACE || "goal-test-daemon";
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");
const canonicalPrefix = "Goal E Canonical Demo";
const roleOrder = ["PM", "01-clarify", "02-design", "03-task-split", "04-implement", "05-verify"];
const projectPaths = {
  usercenter: "ChainWeaver/ida/user-center",
  gateway: "ChainWeaver/ida/gateway",
  "ida-deployment": "ChainWeaver/ida/ida-deployment",
};

await fs.mkdir(artifactRoot, { recursive: true });

const state = {
  schema: "multica.goal_e.canonical_demo_seed.v1",
  generated_at: generatedAt,
  ok: false,
  purpose: "Seed one retained canonical demo scenario through public UI/API paths so the current deployed web page is non-empty and clickable.",
  boundary: "This seed is not final completion evidence. Final gate must still run a real PM+01-05 model execution on the canonical issue.",
  final_real_pm_0105_required: true,
  backend_url: apiBase,
  workspace_slug: workspaceSlug,
  account,
  public_api_steps: [],
  direct_db_steps: [],
  warnings: [],
};

try {
  const token = await login();
  const workspace = await getWorkspace(token);
  state.workspace = pick(workspace, ["id", "name", "slug"]);

  const projects = await getJSON(token, "/api/projects");
  const projectItems = arrayFrom(projects, "items", "projects");
  const targetProjects = Object.keys(projectPaths).map((title) => {
    const found = projectItems.find((project) => project.title === title);
    if (!found?.id) throw new Error(`required project missing: ${title}`);
    return found;
  });
  state.projects = targetProjects.map((project) => pick(project, ["id", "title", "status"]));

  const agents = arrayFrom(await getJSON(token, "/api/agents?include_archived=false"));
  const roleAgents = roleOrder.map((name) => {
    const found = agents.find((agent) => agent.name === name && !agent.archived_at);
    if (!found?.id) throw new Error(`required active agent missing: ${name}`);
    if (!found.runtime_id) throw new Error(`agent ${name} has no runtime_id`);
    return found;
  });
  state.agents = roleAgents.map((agent) => pick(agent, ["id", "name", "runtime_id", "model"]));

  const usercenter = targetProjects.find((project) => project.title === "usercenter");
  const usercenterResources = await ensureGongfengResources(token, targetProjects);
  state.project_resources = usercenterResources;

  const issue = await ensureCanonicalIssue(token, usercenter, roleAgents[0]);
  state.issue = pick(issue, ["id", "identifier", "title", "status", "project_id"]);

  const taskEvidence = [];
  for (const agent of roleAgents) {
    const assigned = await putJSON(token, `/api/issues/${issue.id}`, {
      assignee_type: "agent",
      assignee_id: agent.id,
      status: "todo",
      description: `${canonicalPrefix}: retained demo issue. Current role: ${agent.name}.`,
    });
    state.public_api_steps.push({ step: "assign_issue", agent: agent.name, issue_id: issue.id, status: assigned.status });
    const claimed = await claimAndCompleteRoleTask(token, agent, issue.id);
    taskEvidence.push(claimed);
  }
  state.role_task_evidence = taskEvidence;

  const executionTree = await getJSON(token, `/api/issues/${issue.id}/execution-tree`);
  state.issue_execution_tree = {
    node_count: executionTree.issue_summary?.node_count ?? executionTree.summary?.task_count ?? 0,
    total_input_tokens: executionTree.issue_summary?.total_input_tokens ?? 0,
    total_output_tokens: executionTree.issue_summary?.total_output_tokens ?? 0,
    agent_turn_count: executionTree.issue_summary?.agent_turn_count ?? 0,
    deep_link: executionTree.issue_summary?.full_analysis_deep_link ?? `/${workspaceSlug}/issues/${issue.id}`,
  };

  const skillRepo = await ensureSkillRepoFixture();
  state.skill_repo_fixture = { path: skillRepo.repoPath, skill_path: skillRepo.skillPath, changelog_path: skillRepo.changelogPath };
  const localResource = await ensureLocalResource(token, usercenter.id, skillRepo.repoPath);
  state.local_resource = pick(localResource, ["id", "resource_type", "label"]);

  const prompt = await postJSON(token, "/api/prompt-library", {
    name: `${canonicalPrefix} prompt ${stamp}`,
    description: "Retained canonical demo prompt for trace -> eval -> optimizer closure.",
    prompt_type: "需求澄清",
    content: "目标、边界、验收、证据锚点。Final acceptance must inspect the current deployed Web page and reject empty issue/eval/candidate data.",
    variables: [],
    tags: ["Goal E", "canonical-demo"],
    status: "启用",
  });
  const asset = await postJSON(token, "/api/prompt-evaluation-assets", {
    prompt_id: prompt.id,
    name: `${canonicalPrefix} evaluation asset ${stamp}`,
    description: "Retained canonical asset linking issue review to eval, optimizer, skill patch, CHANGELOG, and re-eval.",
    asset_type: "优化运行",
    payload: {
      canonical_demo: true,
      issue_id: issue.id,
      cases: [{
        名称: "canonical issue review should force optimizer candidate",
        变量: { issue_id: issue.id, issue_title: issue.title },
        期望包含: ["__goal_e_missing_marker__"],
        tags: ["canonical-demo", "issue-review"],
      }],
    },
    status: "启用",
  });
  const inventory = await postJSON(token, `/api/prompt-evaluation-assets/${asset.id}/skill-inventory`, {
    source_resource_id: localResource.id,
    skill_root: ".codebuddy/skills",
  });
  const snapshotResult = await postJSON(token, `/api/prompt-evaluation-assets/${asset.id}/skill-snapshot`, {
    source_resource_id: localResource.id,
    skill_path: skillRepo.skillPath,
  });
  const drafts = await postJSON(token, `/api/prompt-evaluation-assets/${asset.id}/skill-case-drafts`, {
    repo_path: skillRepo.repoPath,
    skill_path: skillRepo.skillPath,
    limit: 3,
    auto_approve: true,
  });
  await putJSON(token, `/api/prompt-evaluation-assets/${asset.id}`, {
    payload: {
      ...asset.payload,
      canonical_demo: true,
      issue_id: issue.id,
      skill_case_drafts: drafts.drafts ?? [],
    },
  });
  await postJSON(token, `/api/prompt-evaluation-assets/${asset.id}/run`, {});
  const failedRun = await poll(async () => {
    const runs = arrayFrom(await getJSON(token, `/api/prompt-evaluation-runs?asset_id=${asset.id}&limit=20`), "items");
    return runs.find((run) => run.status === "未通过");
  }, { timeoutMs: 15_000, label: "failed prompt evaluation run" });
  const candidate = await postJSON(token, `/api/prompt-evaluation-runs/${failedRun.id}/optimization-candidates`, {});
  const updatedCandidate = await putJSON(token, `/api/prompt-evaluation-optimization-candidates/${candidate.id}`, {
    candidate_name: `${canonicalPrefix} skill candidate`,
    candidate_content: "Skill patch candidate for retained canonical demo.",
    rationale: "Close the visible trace -> eval -> optimizer loop and keep evidence on the current web page.",
    edit_note: "Canonical demo candidate promoted to skill patch.",
    skill_patch: {
      patch: skillRepo.candidatePatch,
      source_snapshot: snapshotResult.snapshot,
      source_resource_id: localResource.id,
      repo_path: skillRepo.repoPath,
      target_branch: snapshotResult.snapshot?.branch,
      skill_path: skillRepo.skillPath,
      changelog_path: skillRepo.changelogPath,
      expected_improvement: "Make final verification reject archived-only evidence and empty current DB.",
      risk: "Low-risk retained demo skill fixture.",
      verification_plan: "Run freshness, apply + CHANGELOG, then re-eval.",
      publication_status: "draft",
    },
  });
  const freshness = await postJSON(token, `/api/prompt-evaluation-optimization-candidates/${candidate.id}/skill-freshness`, {
    source_resource_id: localResource.id,
  });
  const apply = await postJSON(token, `/api/prompt-evaluation-optimization-candidates/${candidate.id}/skill-apply`, {
    source_resource_id: localResource.id,
    rollback_plan: "Revert the generated patch and CHANGELOG entry in the retained demo fixture.",
  });
  const reEvalAsset = await postJSON(token, `/api/prompt-evaluation-optimization-candidates/${candidate.id}/skill-re-eval-asset`, {
    source_resource_id: localResource.id,
    snapshot: snapshotResult.snapshot,
    include_draft: false,
  });
  const reEvalRun = await postJSON(token, `/api/prompt-evaluation-optimization-candidates/${candidate.id}/skill-re-eval-run`, {
    asset_id: reEvalAsset.asset?.id,
  });

  state.prompt_evaluation = {
    prompt_id: prompt.id,
    asset_id: asset.id,
    inventory_count: inventory.skills?.length ?? inventory.items?.length ?? null,
    snapshot: pick(snapshotResult.snapshot ?? {}, ["base_commit", "branch", "skill_path", "skill_hash", "source_resource_id"]),
    history_case_count: drafts.drafts?.length ?? 0,
    failed_run_id: failedRun.id,
    candidate_id: updatedCandidate.id,
    freshness_status: freshness.status,
    apply_status: apply.apply?.status,
    changelog_path: apply.apply?.changelog_path,
    re_eval_asset_id: reEvalAsset.asset?.id,
    re_eval_run_id: reEvalRun.run?.id,
    re_eval_status: reEvalRun.run?.status,
  };
  state.urls = {
    issue: `/${workspaceSlug}/issues/${issue.id}`,
    project: `/${workspaceSlug}/projects/${usercenter.id}`,
    run_history: `/${workspaceSlug}/training/run-history?run=${failedRun.id}`,
    optimizer: `/${workspaceSlug}/training/optimization-runs`,
    dashboard: `/${workspaceSlug}/training/runs`,
  };
  state.ok =
    state.role_task_evidence.length === roleOrder.length &&
    state.issue_execution_tree.node_count >= roleOrder.length &&
    state.prompt_evaluation.re_eval_status === "通过";
} catch (error) {
  state.error = String(error?.stack || error?.message || error);
  process.exitCode = 1;
} finally {
  const out = path.join(artifactRoot, `goal-e-canonical-demo-seed-${stamp}.json`);
  const latest = path.join(artifactRoot, "goal-e-canonical-demo-seed-latest.json");
  await fs.writeFile(out, `${JSON.stringify(state, null, 2)}\n`);
  await fs.writeFile(latest, `${JSON.stringify(state, null, 2)}\n`);
  console.log(JSON.stringify({ ok: state.ok, artifact: out, latest, issue: state.issue?.id, prompt_evaluation: state.prompt_evaluation ?? null, error: state.error ?? null }, null, 2));
}

async function ensureGongfengResources(token, projects) {
  const out = [];
  for (const project of projects) {
    const projectPath = projectPaths[project.title];
    const resources = arrayFrom(await getJSON(token, `/api/projects/${project.id}/resources`), "resources");
    let resource = resources.find((item) => item.resource_type === "gongfeng_repo");
    if (!resource) {
      resource = await postJSON(token, `/api/projects/${project.id}/resources`, {
        resource_type: "gongfeng_repo",
        label: `${project.title} v5.0.0_dev`,
        resource_ref: {
          provider: "gongfeng",
          url: `https://git.code.tencent.com/${projectPath}/commits/v5.0.0_dev`,
          project_path: projectPath,
          resource_kind: "commits",
          ref: "v5.0.0_dev",
        },
      });
    }
    const tested = await postJSON(token, `/api/projects/${project.id}/resources/${resource.id}/test`, {});
    const synced = tested.resource_ref?.disabled
      ? tested
      : await postJSON(token, `/api/projects/${project.id}/resources/${resource.id}/sync`, {});
    out.push({
      project_id: project.id,
      project_title: project.title,
      resource_id: synced.id,
      resource_type: synced.resource_type,
      label: synced.label,
      resource_ref: synced.resource_ref,
    });
  }
  return out;
}

async function ensureCanonicalIssue(token, project, pmAgent) {
  const existingItems = arrayFrom(await getJSON(token, `/api/issues?limit=100&project_id=${project.id}`), "items", "issues");
  const existing = existingItems.find((item) => String(item.title || "").startsWith(canonicalPrefix));
  if (existing?.id) return existing;
  return postJSON(token, "/api/issues", {
    title: `${canonicalPrefix}: trace eval optimizer skill loop`,
    description: "Retained canonical demo scenario for the current deployed web page. Final gate must still run real PM+01-05 models.",
    project_id: project.id,
    status: "todo",
    priority: "high",
    assignee_type: "agent",
    assignee_id: pmAgent.id,
  });
}

async function claimAndCompleteRoleTask(token, agent, issueID) {
  const completed = await findIssueTask(token, issueID, agent.id, ["completed"]);
  if (completed) {
    return {
      agent_id: agent.id,
      agent_name: agent.name,
      runtime_id: agent.runtime_id,
      task_id: completed.id,
      issue_id: issueID,
      status: "completed",
      reused: true,
    };
  }
  const claimed = await poll(async () => {
    const existing = await findIssueTask(token, issueID, agent.id, ["dispatched", "running"]);
    if (existing) return existing;
    const res = await postJSON(token, `/api/daemon/runtimes/${agent.runtime_id}/tasks/claim`, {});
    if (!res.task?.id) return null;
    const verified = await findIssueTask(token, issueID, agent.id, ["dispatched", "running"], res.task.id);
    return verified ?? null;
  }, { timeoutMs: 20_000, label: `claim task for ${agent.name}` });
  if (claimed.status !== "running") {
    await postJSON(token, `/api/daemon/tasks/${claimed.id}/start`, {});
  }
  await postJSON(token, `/api/daemon/tasks/${claimed.id}/usage`, {
    usage: [{
      provider: "codex",
      model: agent.model || "gpt-5.4-mini",
      input_tokens: 120 + roleOrder.indexOf(agent.name) * 10,
      output_tokens: 64 + roleOrder.indexOf(agent.name) * 8,
      cache_read_tokens: 12,
      cache_write_tokens: 6,
    }],
  });
  await postJSON(token, `/api/daemon/tasks/${claimed.id}/messages`, {
    messages: [
      {
        seq: 1,
        type: "text",
        content: `${agent.name} 已接收 canonical demo issue，输出阶段目标、风险、验收证据和下一步。`,
      },
      {
        seq: 2,
        type: "tool_use",
        tool: "gongfeng.issue.timeline.inspect",
        content: "检查工蜂资源、issue timeline、评测入口和证据包链接。",
        input: { issue_id: issueID, role: agent.name },
      },
      {
        seq: 3,
        type: "tool_result",
        tool: "gongfeng.issue.timeline.inspect",
        content: `${agent.name} 阶段证据已形成。`,
        output: "ok",
      },
    ],
  });
  await postJSON(token, `/api/daemon/tasks/${claimed.id}/complete`, {
    output: `${agent.name} canonical demo 阶段完成；已留下 usage、messages、tool evidence。`,
    session_id: `goal-e-canonical-${agent.name}-${claimed.id}`,
    work_dir: `/tmp/multica-goal-e-canonical/${agent.name}`,
  });
  return {
    agent_id: agent.id,
    agent_name: agent.name,
    runtime_id: agent.runtime_id,
    task_id: claimed.id,
    issue_id: issueID,
    status: "completed",
  };
}

async function findIssueTask(token, issueID, agentID, statuses, taskID = "") {
  const tree = await getJSON(token, `/api/issues/${issueID}/execution-tree`);
  const tasks = Array.isArray(tree.root?.tasks) ? tree.root.tasks : [];
  return tasks.find((task) =>
    task.id &&
    (!taskID || task.id === taskID) &&
    task.agent_id === agentID &&
    statuses.includes(task.status),
  ) ?? null;
}

async function ensureLocalResource(token, projectID, repoPath) {
  const resources = arrayFrom(await getJSON(token, `/api/projects/${projectID}/resources`), "resources");
  const existing = resources.find((item) => item.resource_type === "local_directory" && item.resource_ref?.local_path === repoPath);
  if (existing?.id) return existing;
  return postJSON(token, `/api/projects/${projectID}/resources`, {
    resource_type: "local_directory",
    label: "Goal E canonical local skill checkout",
    resource_ref: {
      local_path: repoPath,
      daemon_id: "goal-e-canonical-demo",
      label: "Goal E canonical local skill checkout",
    },
  });
}

async function ensureSkillRepoFixture() {
  const repoPath = path.join(repoRoot, ".run", "goal-e-canonical-skill-repo");
  const skillDir = path.join(repoPath, ".codebuddy", "skills", "05-verify");
  const skillPath = ".codebuddy/skills/05-verify/SKILL.md";
  const changelogPath = ".codebuddy/skills/05-verify/CHANGELOG.md";
  await fs.rm(repoPath, { recursive: true, force: true });
  await fs.mkdir(skillDir, { recursive: true });
  await fs.writeFile(path.join(repoPath, "README.md"), "# Goal E canonical skill fixture\n");
  await fs.writeFile(path.join(skillDir, "SKILL.md"), [
    "---",
    "name: 05-verify",
    "description: Verify PM+01-05 delivery evidence.",
    "---",
    "",
    "# 05 Verify",
    "",
    "Verify current Web/API/log evidence. Reject archived-only evidence.",
    "",
  ].join("\n"));
  await fs.writeFile(path.join(skillDir, "CHANGELOG.md"), "# CHANGELOG\n\n");
  await execFileAsync("git", ["init"], { cwd: repoPath });
  await execFileAsync("git", ["config", "user.email", "goal-e-canonical@example.com"], { cwd: repoPath });
  await execFileAsync("git", ["config", "user.name", "Goal E Canonical Demo"], { cwd: repoPath });
  await execFileAsync("git", ["checkout", "-b", "v5.0.0_dev_sop"], { cwd: repoPath });
  await execFileAsync("git", ["add", "."], { cwd: repoPath });
  await execFileAsync("git", ["commit", "-m", "seed canonical verify skill"], { cwd: repoPath });
  await fs.appendFile(
    path.join(skillDir, "SKILL.md"),
    "\nFinal acceptance must inspect the current deployed Web page and reject empty issue/eval/candidate data.\n",
  );
  const { stdout: candidatePatch } = await execFileAsync("git", ["diff", "--", skillPath], { cwd: repoPath });
  await execFileAsync("git", ["checkout", "--", skillPath], { cwd: repoPath });
  return {
    repoPath,
    skillPath,
    changelogPath,
    candidatePatch,
  };
}

async function login() {
  const data = await postJSON(null, "/auth/login", { account, password });
  if (!data?.token) throw new Error("login response did not include token");
  return data.token;
}

async function getWorkspace(token) {
  const data = await getJSON(token, "/api/workspaces");
  const items = Array.isArray(data) ? data : data.items ?? [];
  const found = items.find((item) => item.slug === workspaceSlug);
  if (!found?.id) throw new Error(`workspace ${workspaceSlug} not found`);
  return found;
}

async function getJSON(token, url) {
  return requestJSON(token, url, { method: "GET" });
}

async function postJSON(token, url, body) {
  return requestJSON(token, url, { method: "POST", body: JSON.stringify(body ?? {}) });
}

async function putJSON(token, url, body) {
  return requestJSON(token, url, { method: "PUT", body: JSON.stringify(body ?? {}) });
}

async function requestJSON(token, url, init) {
  const res = await fetch(`${apiBase}${url}`, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: `Bearer ${token}`, "X-Workspace-Slug": workspaceSlug } : {}),
      ...(init.headers ?? {}),
    },
  });
  const text = await res.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { raw: text };
    }
  }
  if (!res.ok) throw new Error(`${init.method || "GET"} ${url} failed: ${res.status} ${text}`);
  state.public_api_steps.push({ method: init.method || "GET", url, status: res.status });
  return data;
}

async function poll(fn, { timeoutMs, label }) {
  const deadline = Date.now() + timeoutMs;
  let last = null;
  while (Date.now() < deadline) {
    last = await fn();
    if (last) return last;
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error(`timed out waiting for ${label}; last=${JSON.stringify(last)}`);
}

function arrayFrom(data, ...keys) {
  if (Array.isArray(data)) return data;
  for (const key of keys) {
    if (Array.isArray(data?.[key])) return data[key];
  }
  return [];
}

function readEnvFile(file) {
  const out = {};
  try {
    const raw = readFileSync(file, "utf8");
    for (const line of raw.split(/\r?\n/)) {
      const trimmed = line.trim();
      if (!trimmed || trimmed.startsWith("#")) continue;
      const idx = trimmed.indexOf("=");
      if (idx === -1) continue;
      out[trimmed.slice(0, idx)] = trimmed.slice(idx + 1);
    }
  } catch {
    return out;
  }
  return out;
}

function trimSlash(value) {
  return String(value || "").replace(/\/+$/, "");
}

function pick(obj, keys) {
  return Object.fromEntries(keys.map((key) => [key, obj?.[key] ?? null]));
}
