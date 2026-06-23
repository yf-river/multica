import { spawnSync } from "node:child_process";
import { writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const script = process.argv[2];
if (!script) {
  console.error("usage: node scripts/run-model-fallback-e2e.mjs <script>");
  process.exit(2);
}

const primaryModel = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_MODEL") || "gpt-5.3-codex-spark";
const fallbackModel = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_FALLBACK_MODEL") || "gpt-5.4-mini";
const attempts = [];

const primary = runAttempt(primaryModel);
attempts.push(summarizeAttempt(primary));
if (primary.exitCode === 0 && !isExternalDependencyFailure(primary.parsed)) {
  printFinal(primary.parsed, attempts);
  process.exit(0);
}
if (primary.exitCode !== 0 && !isExternalDependencyFailure(primary.parsed)) {
  printFinal(wrapperFailure("主模型执行失败且不是可解释外部依赖失败", attempts), attempts);
  process.exit(1);
}

const fallback = runAttempt(fallbackModel);
attempts.push(summarizeAttempt(fallback));
if (fallback.exitCode !== 0 && !isExternalDependencyFailure(fallback.parsed)) {
  printFinal(wrapperFailure("fallback 模型执行失败且不是可解释外部依赖失败", attempts), attempts);
  process.exit(1);
}

printFinal(fallback.parsed || wrapperFailure("fallback 模型未输出 JSON 证据", attempts), attempts);

function runAttempt(model) {
  const started = Date.now();
  const res = spawnSync("node", [script], {
    cwd: process.cwd(),
    shell: false,
    encoding: "utf8",
    env: {
      ...process.env,
      MULTICA_PROMPT_EVALUATION_AGENT_MODEL: model,
    },
    maxBuffer: 1024 * 1024 * 16,
    timeout: Number(trimEnv("ACCEPTANCE_MODEL_ATTEMPT_TIMEOUT_MS") || 360_000),
    killSignal: "SIGTERM",
  });
  return {
    model,
    exitCode: res.status,
    signal: res.signal || null,
    duration_ms: Date.now() - started,
    stdout: res.stdout || "",
    stderr: res.stderr || "",
    parsed: parseLastJSONObject(res.stdout) || parseLastJSONObject(res.stderr),
  };
}

function summarizeAttempt(attempt) {
  return {
    model: attempt.model,
    exit_code: attempt.exitCode,
    signal: attempt.signal,
    duration_ms: attempt.duration_ms,
    result: attempt.parsed?.result || "",
    external_dependency_failure: isExternalDependencyFailure(attempt.parsed),
    error: attempt.parsed?.error || "",
    stdout_tail: tail(attempt.stdout),
    stderr_tail: tail(attempt.stderr),
  };
}

function printFinal(parsed, modelAttempts) {
  const final = {
    ...(parsed || {}),
    model_attempts: modelAttempts,
    fallback_used: modelAttempts.length > 1,
    selected_model: modelAttempts[modelAttempts.length - 1]?.model || primaryModel,
  };
  persistFinalEvidence(final);
  console.log(JSON.stringify(final, null, 2));
}

function persistFinalEvidence(final) {
  const paths = [final?.evidence_path, final?.latest_evidence_path].filter(Boolean);
  if (paths.length === 0) return;
  const content = `${JSON.stringify(final, null, 2)}\n`;
  for (const outputPath of paths) {
    try {
      writeFileSync(outputPath, content);
    } catch {
      // Child scripts may return non-file evidence references; wrapper output remains authoritative.
    }
  }
}

function wrapperFailure(message, modelAttempts) {
  return {
    schema: "multica.model_fallback_e2e.v1",
    result: "failed",
    error: message,
    model_attempts: modelAttempts,
  };
}

function isExternalDependencyFailure(parsed) {
  return parsed?.external_dependency_failure === true || parsed?.result === "external_dependency_failure";
}

function parseLastJSONObject(output) {
  const text = String(output || "").trim();
  for (let index = text.lastIndexOf("{"); index >= 0; index = text.lastIndexOf("{", index - 1)) {
    try {
      return JSON.parse(text.slice(index));
    } catch {
      // Keep scanning. Child scripts may print logs before their final JSON.
    }
  }
  return null;
}

function tail(output) {
  return String(output || "").trim().split(/\r?\n/).filter(Boolean).slice(-12);
}

function trimEnv(name) {
  return (process.env[name] || "").trim();
}
