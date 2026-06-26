#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { spawnSync } from "node:child_process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = path.join(repoRoot, "artifacts", "acceptance");
const now = new Date().toISOString();
const stamp = now.replace(/[:.]/g, "-");

const roots = ["apps", "packages", "server", "scripts", "e2e", "docs"]
  .filter((root) => fs.existsSync(path.join(repoRoot, root)));
const pattern = "github|github_repo|pull[-_ ]?request|GitHubPullRequest|GitHub";
const rg = spawnSync("rg", ["--json", "-n", "-i", pattern, ...roots], {
  cwd: repoRoot,
  encoding: "utf8",
  maxBuffer: 1024 * 1024 * 64,
});

if (rg.error) throw rg.error;
if (![0, 1].includes(rg.status)) {
  process.stderr.write(rg.stderr || "");
  process.exit(rg.status || 1);
}

const matches = [];
for (const raw of rg.stdout.split(/\r?\n/)) {
  if (!raw.trim()) continue;
  let event;
  try {
    event = JSON.parse(raw);
  } catch {
    continue;
  }
  if (event.type !== "match") continue;
  const file = event.data.path.text;
  const line = event.data.line_number;
  const text = event.data.lines.text.trim();
  matches.push({ file, line, text, ...classify(file, text) });
}

const bySeverity = groupBy(matches, (item) => item.severity);
const byCategory = groupBy(matches, (item) => item.category);
const blockers = matches.filter((item) => item.severity === "blocker");
const review = matches.filter((item) => item.severity === "review");
const report = {
  schema: "multica.goal_e.gongfeng_touchpoint_audit.v1",
  generated_at: now,
  ok: blockers.length === 0,
  scope: "fresh current-state audit for Goal E E-01; this script does not mutate code",
  scanned_roots: roots,
  pattern,
  summary: {
    total_matches: matches.length,
    blockers: blockers.length,
    review: review.length,
    allowed: matches.filter((item) => item.severity === "allowed").length,
    compatibility: matches.filter((item) => item.severity === "compatibility").length,
    by_severity: countMap(bySeverity),
    by_category: countMap(byCategory),
  },
  blocker_files: unique(blockers.map((item) => item.file)),
  review_files: unique(review.map((item) => item.file)),
  blocking_user_visible_main_path: blockers,
  review_items: review,
  compatibility_items: matches.filter((item) => item.severity === "compatibility"),
  allowed_items_sample: matches.filter((item) => item.severity === "allowed").slice(0, 80),
};

fs.mkdirSync(artifactRoot, { recursive: true });
const jsonPath = path.join(artifactRoot, `goal-e-gongfeng-touchpoint-audit-${stamp}.json`);
const mdPath = path.join(artifactRoot, `goal-e-gongfeng-touchpoint-audit-${stamp}.md`);
const latestJSON = path.join(artifactRoot, "goal-e-gongfeng-touchpoint-audit-latest.json");
const latestMD = path.join(artifactRoot, "goal-e-gongfeng-touchpoint-audit-latest.md");
fs.writeFileSync(jsonPath, `${JSON.stringify(report, null, 2)}\n`);
fs.writeFileSync(latestJSON, `${JSON.stringify(report, null, 2)}\n`);
fs.writeFileSync(mdPath, renderMarkdown(report));
fs.writeFileSync(latestMD, renderMarkdown(report));

console.log(JSON.stringify({
  ok: report.ok,
  json: jsonPath,
  markdown: mdPath,
  summary: report.summary,
  blocker_files: report.blocker_files,
}, null, 2));
if (!report.ok) process.exitCode = 1;

function classify(file, text) {
  if (isTestOrFixture(file)) return item("test_or_fixture", "allowed");
  if (isGoModuleImport(text)) return item("go_module_import", "allowed");
  if (isReleaseDistribution(file)) return item("release_distribution", "allowed");
  if (isSkillUpstream(file)) return item("skill_upstream_import", "allowed");
  if (isAgentToolCompatibility(file, text)) return item("agent_tool_compatibility", "allowed");
  if (isHiddenSettingsCompatibility(file)) return item("hidden_settings_compatibility", "compatibility");
  if (isMRComponentInternalCompatibility(file)) return item("mr_component_internal_compatibility", "compatibility");
  if (isBuiltinSkillSourceMapCompatibility(file)) return item("builtin_skill_source_map_compatibility", "compatibility");
  if (isRuntimeCLICompatibility(file, text)) return item("runtime_cli_compatibility", "compatibility");
  if (isBlockingUserVisible(file)) return item("user_visible_main_path", "blocker");
  if (isRuntimePromptOrBuiltinSkill(file)) return item("runtime_prompt_or_builtin_skill", "blocker");
  if (isLegacyProviderCompatibility(file)) return item("legacy_provider_compatibility", "compatibility");
  if (/github_repo|GitHub repo|pull[-_ ]?request|pull request/i.test(text)) {
    return item("unclassified_product_semantics", "review");
  }
  return item("external_reference_or_comment", "allowed");
}

function item(category, severity) {
  return { category, severity };
}

function isBlockingUserVisible(file) {
  return [
    "packages/views/modals/create-project.tsx",
    "packages/views/projects/components/project-resources-section.tsx",
    "packages/views/settings/components/github-tab.tsx",
    "packages/views/settings/components/github-mark.tsx",
    "packages/views/settings/index.ts",
    "packages/views/issues/components/pull-request-list.tsx",
    "apps/docs/content/docs/github-integration.zh.mdx",
  ].includes(file);
}

function isRuntimePromptOrBuiltinSkill(file) {
  return file === "server/internal/service/builtin_skills/multica-working-on-issues/SKILL.md";
}

function isHiddenSettingsCompatibility(file) {
  return file === "packages/views/settings/components/github-tab.tsx" ||
    file === "packages/views/settings/components/github-mark.tsx";
}

function isMRComponentInternalCompatibility(file) {
  return file === "packages/views/issues/components/pull-request-list.tsx";
}

function isBuiltinSkillSourceMapCompatibility(file) {
  return file === "server/internal/service/builtin_skills/multica-working-on-issues/references/working-on-issues-source-map.md";
}

function isRuntimeCLICompatibility(file, text) {
  return file === "server/internal/service/builtin_skills/multica-working-on-issues/SKILL.md" &&
    /pull-requests|pull_requests/i.test(text) &&
    !/github|GitHub/i.test(text);
}

function isLegacyProviderCompatibility(file) {
  return file.startsWith("packages/core/github/") ||
    file === "packages/core/types/github.ts" ||
    file === "server/internal/handler/github.go" ||
    file.startsWith("server/pkg/db/queries/github") ||
    file.startsWith("server/pkg/db/generated/github") ||
    /server\/migrations\/\d+_github/i.test(file);
}

function isReleaseDistribution(file) {
  return file.startsWith("scripts/install") ||
    file.startsWith("apps/desktop/") ||
    file.startsWith("apps/docs/content/docs/cli/installation") ||
    file.startsWith("apps/docs/content/docs/install-agent-runtime") ||
    file.startsWith("apps/docs/content/docs/desktop-app");
}

function isSkillUpstream(file) {
  return file === "server/internal/handler/skill.go" ||
    file === "server/cmd/multica/cmd_skill.go" ||
    file.startsWith("server/internal/agenttmpl/templates/") ||
    file.startsWith("server/internal/service/builtin_skills/multica-skill-importing/") ||
    file === "docs/agent-quick-create-plan.md" ||
    file === "apps/docs/content/docs/skills.zh.mdx";
}

function isAgentToolCompatibility(file, text) {
  return file.startsWith("server/internal/daemon/execenv/") &&
    (/\.github\/skills|Copilot|docs\.github\.com/i.test(text));
}

function isTestOrFixture(file) {
  return file.startsWith("e2e/") ||
    /(^|\/)__tests__\//.test(file) ||
    /\.(test|spec)\.[cm]?[tj]sx?$/.test(file) ||
    /_test\.go$/.test(file);
}

function isGoModuleImport(text) {
  return /^\s*"github\.com\//.test(text) || /^\s*github\.com\//.test(text);
}

function groupBy(items, fn) {
  const grouped = new Map();
  for (const item of items) {
    const key = fn(item);
    if (!grouped.has(key)) grouped.set(key, []);
    grouped.get(key).push(item);
  }
  return grouped;
}

function countMap(map) {
  return Object.fromEntries([...map.entries()].sort(([left], [right]) => left.localeCompare(right)).map(([key, value]) => [key, value.length]));
}

function unique(items) {
  return [...new Set(items)].sort();
}

function renderMarkdown(data) {
  const lines = [
    "# Goal E Gongfeng Touchpoint Audit",
    "",
    `- Generated: ${data.generated_at}`,
    `- Result: ${data.ok ? "PASS" : "FAIL"}`,
    `- Total matches: ${data.summary.total_matches}`,
    `- Blockers: ${data.summary.blockers}`,
    `- Review: ${data.summary.review}`,
    `- Compatibility: ${data.summary.compatibility}`,
    `- Allowed: ${data.summary.allowed}`,
    "",
    "## Blocker Files",
    "",
    ...data.blocker_files.map((file) => `- \`${file}\``),
    "",
    "## Blocking User-Visible Main Path",
    "",
    "| File | Line | Category | Text |",
    "| --- | ---: | --- | --- |",
  ];
  for (const item of data.blocking_user_visible_main_path) {
    lines.push(`| \`${item.file}\` | ${item.line} | ${item.category} | ${escapeCell(item.text)} |`);
  }
  lines.push("", "## Review Items", "", "| File | Line | Category | Text |", "| --- | ---: | --- | --- |");
  for (const item of data.review_items.slice(0, 120)) {
    lines.push(`| \`${item.file}\` | ${item.line} | ${item.category} | ${escapeCell(item.text)} |`);
  }
  lines.push("");
  return `${lines.join("\n")}\n`;
}

function escapeCell(value) {
  return String(value ?? "").replace(/\|/g, "\\|").replace(/\n/g, "<br>");
}
