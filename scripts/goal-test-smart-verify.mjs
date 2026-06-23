import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { spawnSync } from "node:child_process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = path.join(repoRoot, "artifacts/acceptance");
const timingPath = path.join(artifactRoot, "command-timings.jsonl");
const validModes = new Set(["dev", "precommit", "final"]);

const options = parseArgs(process.argv.slice(2));
if (!validModes.has(options.mode)) fail(`invalid --mode ${options.mode}; expected dev, precommit, or final`);

const currentCommit = gitText(["rev-parse", "--short=12", "HEAD"]);
const changedFiles = listChangedFiles();
const classification = classifyChanges(changedFiles);
const commands = buildCommands(options.mode, classification);
const payload = {
  schema: "multica.goal_test.smart_verify.v1",
  generated_at: new Date().toISOString(),
  mode: options.mode,
  dry_run: options.dryRun,
  current_commit: currentCommit,
  changed_files: changedFiles,
  classification,
  commands: commands.map((command) => ({
    id: command.id,
    command: command.command,
    token_optimized: commandTokenOptimized(command),
    reason: command.reason,
  })),
};

console.log(JSON.stringify(payload, null, 2));

if (options.dryRun) process.exit(0);

mkdirSync(artifactRoot, { recursive: true });

for (const command of commands) {
  const result = command.internal ? runInternal(command) : runShell(command);
  appendTiming(command, result);
  if (result.exit_code !== 0) {
    process.exit(result.exit_code);
  }
}

function parseArgs(args) {
  const parsed = { mode: "dev", dryRun: false };
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];
    if (arg === "--dry-run") {
      parsed.dryRun = true;
    } else if (arg === "--mode") {
      parsed.mode = args[index + 1] || "";
      index += 1;
    } else if (arg.startsWith("--mode=")) {
      parsed.mode = arg.slice("--mode=".length);
    } else {
      fail(`unknown argument ${arg}`);
    }
  }
  return parsed;
}

function listChangedFiles() {
  const tracked = gitLines(["diff", "--name-only", "HEAD", "--"]);
  const untracked = gitLines(["ls-files", "--others", "--exclude-standard"]);
  return Array.from(new Set([...tracked, ...untracked])).sort();
}

function classifyChanges(files) {
  const normalized = files.map((file) => file.replace(/\\/g, "/"));
  const has = (predicate) => normalized.some(predicate);
  const scriptFiles = normalized.filter((file) => /^scripts\/.*\.(?:mjs|js)$/.test(file));
  const e2eSpecs = normalized.filter((file) => /^e2e\/.*\.spec\.ts$/.test(file));
  const docsOnly = normalized.length > 0 && normalized.every(isDocsOnlyFile);
  const trainingRelated = has((file) =>
    file.includes("prompt-library") ||
    file.includes("training") ||
    file.includes("prompt-evaluation") ||
    file.includes("goal-test-training-performance"),
  );
  return {
    empty: normalized.length === 0,
    docs_only: docsOnly,
    docs_or_ledger: has(isDocsOnlyFile),
    script_files: scriptFiles,
    e2e_specs: e2eSpecs,
    e2e_changed: has((file) => file.startsWith("e2e/") || file === "playwright.config.ts"),
    views_changed: has((file) => file.startsWith("packages/views/") || file.startsWith("packages/ui/")),
    web_changed: has((file) => file.startsWith("apps/web/")),
    core_changed: has((file) => file.startsWith("packages/core/")),
    server_changed: has((file) => file.startsWith("server/")),
    migrations_changed: has((file) => file.startsWith("server/migrations/")),
    deploy_changed: has((file) =>
      file === "Makefile" ||
      file.startsWith("deploy/") ||
      file === "scripts/goal-test-environments.mjs" ||
      file === "scripts/goal-test-e2e-preflight.mjs" ||
      file === "scripts/goal-test-ui-audit.mjs" ||
      file === "scripts/goal-test-training-performance-audit.mjs",
    ),
    training_related: trainingRelated,
  };
}

function isDocsOnlyFile(file) {
  return file === "CLAUDE.md" ||
    file === "AGENTS.md" ||
    file === "README.md" ||
    file.startsWith("docs/") ||
    file.endsWith(".md") ||
    file.endsWith(".mdx") ||
    file.endsWith(".jsonl");
}

function buildCommands(mode, info) {
  const commands = [];
  const add = (id, command, reason) => commands.push({ id, command, reason });
  const addInternal = (id, reason, fn) => commands.push({ id, internal: true, reason, fn });

  if (info.docs_or_ledger || info.docs_only) {
    addInternal("validate-docs", "Validate changed JSONL ledger files and document-only changes without deploying.", () => validateDocs());
  }

  for (const scriptFile of info.script_files) {
    add(`node-check:${scriptFile}`, `node --check ${shellQuote(scriptFile)}`, `Syntax-check changed script ${scriptFile}.`);
  }

  if (info.views_changed) {
    add("views-typecheck", "pnpm --filter @multica/views exec tsc --noEmit --pretty false", "Views/UI changed; run the shared views typecheck.");
  }
  if (info.web_changed) {
    add("web-typecheck", "pnpm --filter @multica/web typecheck", "Web app changed; run the web typecheck.");
  }
  if (info.core_changed) {
    add("core-training-test", "pnpm --filter @multica/core test -- training/index.test.ts", "Core changed; run the focused training/core tests used by goal-test slices.");
  }
  if (info.server_changed) {
    add("server-focused-test", "cd server && go test ./internal/handler ./internal/service", "Server changed; run focused backend service and handler tests.");
  }

  if (info.e2e_specs.length > 0) {
    add("changed-e2e-specs", goalTestPlaywrightCommand(`${info.e2e_specs.map(shellQuote).join(" ")} --project=chromium`), "E2E specs changed; run only changed specs against the goal-test int environment.");
  } else if (shouldRunFocusedTrainingE2E(mode, info)) {
    add(
      "focused-training-e2e",
      goalTestPlaywrightCommand("e2e/navigation.spec.ts --project=chromium --grep 'training playgrounds keep (distinct request boundaries|selected prompt storage isolated by surface)'"),
      "Training UI changed; run the narrow training playground boundary E2E.",
    );
  }

  if (mode === "precommit") {
    add("goal-test-smoke", "make goal-test-smoke", "Precommit gate: verify preflight, current environment, and current log window.");
  }

  if (mode === "final") {
    const deployDecision = shouldDeploy(info);
    if (deployDecision.deploy) {
      add("goal-test-deploy-dev", "make goal-test-deploy-dev", deployDecision.reason);
    } else {
      add("goal-test-verify-current", "node scripts/goal-test-environments.mjs verify int && node scripts/goal-test-environments.mjs verify-logs int", deployDecision.reason);
    }
    if (info.views_changed || info.web_changed || info.training_related || info.e2e_changed) {
      add("goal-test-ui-audit", "make goal-test-ui-audit", "Final UI gate for changed browser-facing behavior.");
    }
    if (info.training_related) {
      add("goal-test-training-performance-audit", "make goal-test-training-performance-audit", "Final training/evaluation route performance gate.");
    }
  }

  if (commands.length === 0) {
    add("goal-test-smoke", "make goal-test-smoke", "No changed files detected; run the fast environment smoke gate.");
  }

  return dedupeCommands(commands);
}

function shouldRunFocusedTrainingE2E(mode, info) {
  if (mode === "dev" && !info.training_related) return false;
  return info.training_related && (info.views_changed || info.web_changed || info.core_changed);
}

function shouldDeploy(info) {
  const metadata = readDeploymentMetadata();
  const deployedCommit = metadata?.commit || metadata?.build_version || "";
  const deployWorthy = info.server_changed || info.migrations_changed || info.web_changed || info.views_changed || info.core_changed || info.deploy_changed;
  if (!metadata) {
    return { deploy: true, reason: "Final gate: deployment metadata is missing, so deploy once." };
  }
  if (deployWorthy) {
    return { deploy: true, reason: "Final gate: code or deployment-affecting files changed, so deploy once." };
  }
  if (deployedCommit !== currentCommit) {
    return { deploy: true, reason: `Final gate: deployed commit ${deployedCommit || "unknown"} differs from current ${currentCommit}.` };
  }
  return { deploy: false, reason: `Final gate: current commit ${currentCommit} is already deployed; verify without redeploying.` };
}

function goalTestPlaywrightCommand(args) {
  return [
    "set -a",
    ". .run/env/goal-test-int.env",
    "set +a",
    [
      "PLAYWRIGHT_BASE_URL=http://9.134.129.162:13682",
      "NEXT_PUBLIC_API_URL=${GOAL_TEST_BACKEND_URL:-http://9.134.129.162:${PORT:-18762}}",
      "E2E_ACCOUNT=goal-test-daemon",
      "E2E_NAME='goal-test 验收账号'",
      "E2E_WORKSPACE=goal-test-daemon",
      "E2E_WORKSPACE_NAME='goal-test 联调工作区'",
      "E2E_PASSWORD=e2e-password",
      `pnpm exec playwright test ${args}`,
    ].join(" "),
  ].join("; ");
}

function readDeploymentMetadata() {
  const file = path.join(repoRoot, ".run/deployments/goal-test-int.json");
  if (!existsSync(file)) return null;
  try {
    return JSON.parse(readFileSync(file, "utf8"));
  } catch {
    return null;
  }
}

function validateDocs() {
  const started = Date.now();
  const jsonlFiles = changedFiles.filter((file) => file.endsWith(".jsonl"));
  for (const file of jsonlFiles) {
    const fullPath = path.join(repoRoot, file);
    if (!existsSync(fullPath)) continue;
    const lines = readFileSync(fullPath, "utf8").split(/\r?\n/);
    for (const [index, line] of lines.entries()) {
      if (!line.trim()) continue;
      try {
        JSON.parse(line);
      } catch (error) {
        return {
          exit_code: 2,
          duration_ms: Date.now() - started,
          error: `${file}:${index + 1} is not valid JSON: ${error instanceof Error ? error.message : String(error)}`,
        };
      }
    }
  }
  return {
    exit_code: 0,
    duration_ms: Date.now() - started,
    output: jsonlFiles.length > 0 ? `validated ${jsonlFiles.length} JSONL file(s)` : "no JSONL files changed",
  };
}

function runInternal(command) {
  console.log(`\n==> ${command.id}`);
  const result = command.fn();
  if (result.output) console.log(result.output);
  if (result.error) console.error(result.error);
  return result;
}

function runShell(command) {
  if (commandTokenOptimized(command)) {
    return runTokenOptimizedShell(command);
  }
  console.log(`\n==> ${command.command}`);
  const started = Date.now();
  const result = spawnSync("bash", ["-lc", command.command], {
    cwd: repoRoot,
    stdio: "inherit",
    env: process.env,
  });
  return {
    exit_code: result.status ?? 1,
    signal: result.signal || "",
    duration_ms: Date.now() - started,
  };
}

function runTokenOptimizedShell(command) {
  const started = Date.now();
  const result = spawnSync("node", [
    "scripts/goal-test-command-wrapper.mjs",
    "--id",
    command.id,
    "--command",
    command.command,
  ], {
    cwd: repoRoot,
    encoding: "utf8",
    maxBuffer: 16 * 1024 * 1024,
    env: process.env,
  });
  if (result.stdout) process.stdout.write(result.stdout);
  if (result.stderr) process.stderr.write(result.stderr);
  const metadata = parseWrapperMetadata(result.stdout || "");
  return {
    exit_code: result.status ?? metadata?.exit_code ?? 1,
    signal: result.signal || metadata?.signal || "",
    duration_ms: metadata?.duration_ms ?? Date.now() - started,
    token_optimized: true,
    optimizer: metadata?.optimizer || "",
    optimizer_note: metadata?.optimizer_note || "",
    raw_log_path: metadata?.raw_log_path || "",
    raw_bytes: metadata?.raw_bytes ?? 0,
    summary_bytes: metadata?.summary_bytes ?? 0,
    estimated_savings_ratio: metadata?.estimated_savings_ratio ?? 0,
  };
}

function appendTiming(command, result) {
  const line = {
    schema: "multica.goal_test.command_timing.v1",
    generated_at: new Date().toISOString(),
    mode: options.mode,
    command_id: command.id,
    command: command.command || command.id,
    reason: command.reason,
    current_commit: currentCommit,
    changed_files: changedFiles,
    duration_ms: result.duration_ms,
    exit_code: result.exit_code,
    signal: result.signal || "",
    error: result.error || "",
    token_optimized: result.token_optimized === true,
    optimizer: result.optimizer || "",
    optimizer_note: result.optimizer_note || "",
    raw_log_path: result.raw_log_path || "",
    raw_bytes: result.raw_bytes ?? 0,
    summary_bytes: result.summary_bytes ?? 0,
    estimated_savings_ratio: result.estimated_savings_ratio ?? 0,
  };
  writeFileSync(timingPath, `${JSON.stringify(line)}\n`, { flag: "a" });
}

function dedupeCommands(commands) {
  const seen = new Set();
  return commands.filter((command) => {
    const key = command.command || command.id;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function commandTokenOptimized(command) {
  const optimizerMode = String(process.env.GOAL_TEST_TOKEN_OPTIMIZER || "builtin").trim().toLowerCase();
  if (["0", "false", "off", "none", "raw"].includes(optimizerMode)) return false;
  if (!command.command || command.internal) return false;
  if (command.id.startsWith("node-check:")) return false;
  if (command.id === "validate-docs") return false;
  if (/\b(rg|find|ls|git diff)\b/.test(command.command)) return false;
  return /(?:typecheck|test|e2e|smoke|deploy|audit|verify-current)/.test(command.id) ||
    /\b(go test|pnpm .*test|pnpm exec playwright|make goal-test-|goal-test-environments\.mjs verify)\b/.test(command.command);
}

function parseWrapperMetadata(output) {
  const line = output.split(/\r?\n/).find((item) => item.startsWith("GOAL_TEST_TOKEN_OPTIMIZER_RESULT "));
  if (!line) return null;
  try {
    return JSON.parse(line.slice("GOAL_TEST_TOKEN_OPTIMIZER_RESULT ".length));
  } catch {
    return null;
  }
}

function gitText(args) {
  const result = spawnSync("git", args, { cwd: repoRoot, encoding: "utf8" });
  if (result.status !== 0) fail(result.stderr || `git ${args.join(" ")} failed`);
  return result.stdout.trim();
}

function gitLines(args) {
  const text = gitText(args);
  return text ? text.split(/\r?\n/).filter(Boolean) : [];
}

function shellQuote(value) {
  return `'${String(value).replace(/'/g, "'\\''")}'`;
}

function fail(message) {
  console.error(message);
  process.exit(2);
}
