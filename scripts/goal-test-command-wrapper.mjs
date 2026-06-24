import { createWriteStream, mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { spawn, spawnSync } from "node:child_process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = path.join(repoRoot, "artifacts/acceptance");
const rawLogDir = path.join(artifactRoot, "raw-command-logs");
const metaDir = path.join(artifactRoot, "command-metadata");
const importantPattern = /\b(error|fatal|panic|fail(?:ed|ure)?|timeout|trace|screenshot|expected|received|assert|exception|not ok|ECONNREFUSED|EADDRINUSE)\b|--- FAIL|AssertionError|TypeError|ReferenceError/i;

const options = parseArgs(process.argv.slice(2));
if (!options.id || !options.command) {
  fail("usage: node scripts/goal-test-command-wrapper.mjs --id <command-id> --command <shell-command>");
}

mkdirSync(rawLogDir, { recursive: true });
mkdirSync(metaDir, { recursive: true });

const startedAt = Date.now();
const stamp = new Date().toISOString().replace(/[:.]/g, "-");
const safeID = options.id.replace(/[^A-Za-z0-9_.-]+/g, "-").slice(0, 80) || "command";
const rawLogPath = path.join(rawLogDir, `${stamp}-${safeID}.log`);
const metaPath = path.join(metaDir, `${stamp}-${safeID}.json`);
const rawLog = createWriteStream(rawLogPath, { flags: "wx" });
const summary = createSummaryCollector();
const optimizerRequest = process.env.GOAL_TEST_TOKEN_OPTIMIZER || "builtin";
const optimizer = resolveOptimizer(optimizerRequest, options.command);
const execution = buildExecution(options.command, optimizer);

rawLog.write(`command_id: ${options.id}\n`);
rawLog.write(`command: ${options.command}\n`);
rawLog.write(`executed_command: ${execution.display}\n`);
rawLog.write(`started_at: ${new Date(startedAt).toISOString()}\n`);
rawLog.write(`optimizer: ${optimizer.name}\n`);
if (optimizer.note) rawLog.write(`optimizer_note: ${optimizer.note}\n`);
rawLog.write("\n");

const child = spawn(execution.bin, execution.args, {
  cwd: repoRoot,
  env: process.env,
  stdio: ["ignore", "pipe", "pipe"],
});

child.stdout.on("data", (chunk) => recordChunk("stdout", chunk));
child.stderr.on("data", (chunk) => recordChunk("stderr", chunk));
child.on("error", (error) => {
  const text = `spawn failed: ${error.message}\n`;
  rawLog.write(text);
  summary.addLine("stderr", text.trimEnd());
});

const exit = await waitForExit(child);
rawLog.write(`\nfinished_at: ${new Date().toISOString()}\n`);
rawLog.write(`exit_code: ${exit.code ?? 1}\n`);
if (exit.signal) rawLog.write(`signal: ${exit.signal}\n`);
await closeStream(rawLog);

const rendered = renderSummary({
  id: options.id,
  command: options.command,
  exitCode: exit.code ?? 1,
  signal: exit.signal || "",
  durationMs: Date.now() - startedAt,
  rawLogPath,
  optimizer,
  summary,
});
process.stdout.write(rendered.text);

const metadata = {
  schema: "multica.goal_test.command_wrapper.v1",
  generated_at: new Date().toISOString(),
  command_id: options.id,
  command: options.command,
  exit_code: exit.code ?? 1,
  signal: exit.signal || "",
  duration_ms: Date.now() - startedAt,
  optimizer_requested: optimizerRequest,
  optimizer: optimizer.name,
  optimizer_active: optimizer.active,
  optimizer_note: optimizer.note,
  executed_command: execution.display,
  raw_log_path: rawLogPath,
  raw_bytes: summary.rawBytes,
  summary_bytes: Buffer.byteLength(rendered.text),
  estimated_savings_ratio: savingsRatio(summary.rawBytes, Buffer.byteLength(rendered.text)),
};
writeFileSync(metaPath, `${JSON.stringify(metadata, null, 2)}\n`);
process.stdout.write(`\nGOAL_TEST_TOKEN_OPTIMIZER_RESULT ${JSON.stringify(metadata)}\n`);
process.exit(exit.code ?? 1);

function recordChunk(stream, chunk) {
  rawLog.write(chunk);
  summary.addChunk(stream, chunk);
}

function createSummaryCollector() {
  const firstLines = [];
  const lastLines = [];
  const importantLines = [];
  const pending = { stdout: "", stderr: "" };
  const maxFirst = 30;
  const maxLast = 100;
  const maxImportant = 160;
  return {
    rawBytes: 0,
    lineCount: 0,
    firstLines,
    lastLines,
    importantLines,
    addChunk(stream, chunk) {
      const text = chunk.toString("utf8");
      this.rawBytes += Buffer.byteLength(chunk);
      pending[stream] += text;
      const lines = pending[stream].split(/\r?\n/);
      pending[stream] = lines.pop() || "";
      for (const line of lines) this.addLine(stream, line);
    },
    addLine(stream, line) {
      this.lineCount += 1;
      const item = { stream, line };
      if (firstLines.length < maxFirst) firstLines.push(item);
      lastLines.push(item);
      while (lastLines.length > maxLast) lastLines.shift();
      if (importantPattern.test(line) && importantLines.length < maxImportant) {
        importantLines.push(item);
      }
    },
    flushPending() {
      for (const [stream, value] of Object.entries(pending)) {
        if (value) {
          this.addLine(stream, value);
          pending[stream] = "";
        }
      }
    },
  };
}

function renderSummary({ id, command, exitCode, signal, durationMs, rawLogPath, optimizer, summary }) {
  summary.flushPending();
  const lines = [
    "",
    `==> ${id}`,
    `command: ${command}`,
    `exit: ${exitCode}${signal ? ` signal=${signal}` : ""}`,
    `duration_ms: ${durationMs}`,
    `optimizer: ${optimizer.name}${optimizer.active ? " active" : " inactive"}${optimizer.note ? ` (${optimizer.note})` : ""}`,
    `raw_log: ${rawLogPath}`,
    `raw_bytes: ${summary.rawBytes}`,
    `lines: ${summary.lineCount}`,
  ];

  const success = exitCode === 0;
  const blocks = success
    ? [
        ["first lines", summary.firstLines.slice(0, 12)],
        ["last lines", summary.lastLines.slice(-30)],
      ]
    : [
        ["important lines", summary.importantLines],
        ["last lines", summary.lastLines.slice(-120)],
      ];

  for (const [label, items] of blocks) {
    if (items.length === 0) continue;
    lines.push("", `-- ${label} --`);
    for (const item of dedupeAdjacent(items)) {
      lines.push(formatLine(item));
    }
  }

  if (!success) {
    lines.push("", "Full failure output is preserved in raw_log; inspect it before rerunning broad gates.");
  }

  lines.push("");
  const text = `${lines.join("\n")}\n`;
  return { text };
}

function dedupeAdjacent(items) {
  const output = [];
  let previous = "";
  for (const item of items) {
    const formatted = formatLine(item);
    if (formatted === previous) continue;
    output.push(item);
    previous = formatted;
  }
  return output;
}

function formatLine(item) {
  const prefix = item.stream === "stderr" ? "stderr" : "stdout";
  return `[${prefix}] ${item.line}`;
}

function resolveOptimizer(request, command) {
  const value = String(request || "builtin").trim().toLowerCase();
  if (value === "0" || value === "false" || value === "off" || value === "none") {
    return { name: "passthrough-summary", active: false, note: "external optimizer disabled" };
  }
  if (value === "rtk") {
    const probe = spawnSync("bash", ["-lc", "command -v rtk"], { encoding: "utf8" });
    if (probe.status === 0) {
      if (isSafeRTKCommand(command)) {
        const rewritten = rewriteWithRTK(probe.stdout.trim(), command);
        if (rewritten && rewritten !== command) {
          return { name: "rtk", active: true, bin: probe.stdout.trim(), rewritten_command: rewritten, note: "rtk rewrite enabled for safe command" };
        }
        return { name: "builtin-summary", active: false, note: "rtk installed but did not rewrite this safe command" };
      }
      return { name: "builtin-summary", active: false, note: "rtk detected but command is outside the safe allowlist" };
    }
    return { name: "builtin-summary", active: false, note: "rtk requested but not installed" };
  }
  return { name: "builtin-summary", active: false, note: "raw-preserving default" };
}

function buildExecution(command, optimizer) {
  if (optimizer.name === "rtk" && optimizer.active && optimizer.rewritten_command) {
    return {
      bin: "bash",
      args: ["-lc", optimizer.rewritten_command],
      display: optimizer.rewritten_command,
    };
  }
  return {
    bin: "bash",
    args: ["-lc", command],
    display: command,
  };
}

function isSafeRTKCommand(command) {
  const normalized = command.replace(/\s+/g, " ").trim();
  if (/\b(rg|find|ls|git diff)\b/.test(normalized)) return false;
  return /\b(go test|pnpm (?:--filter [^ ]+ )?(?:exec playwright test|test|typecheck)|make goal-test-(?:smoke|e2e|e2e-all|ui-audit|training-performance-audit)|node scripts\/goal-test-environments\.mjs verify(?:-logs)? )/.test(normalized);
}

function rewriteWithRTK(rtkPath, command) {
  const result = spawnSync(rtkPath, ["rewrite", command], {
    cwd: repoRoot,
    encoding: "utf8",
    maxBuffer: 1024 * 1024,
    env: process.env,
  });
  if (![0, 3].includes(result.status ?? 1)) return "";
  const candidates = result.stdout
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line && !line.startsWith("[rtk]"));
  return candidates[candidates.length - 1] || "";
}

function savingsRatio(rawBytes, summaryBytes) {
  if (rawBytes <= 0) return 0;
  return Number(Math.max(0, 1 - summaryBytes / rawBytes).toFixed(4));
}

function waitForExit(child) {
  return new Promise((resolve) => {
    child.on("close", (code, signal) => resolve({ code, signal }));
  });
}

function closeStream(stream) {
  return new Promise((resolve, reject) => {
    stream.end((error) => {
      if (error) reject(error);
      else resolve();
    });
  });
}

function parseArgs(args) {
  const parsed = { id: "", command: "" };
  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];
    if (arg === "--id") {
      parsed.id = args[index + 1] || "";
      index += 1;
    } else if (arg.startsWith("--id=")) {
      parsed.id = arg.slice("--id=".length);
    } else if (arg === "--command") {
      parsed.command = args[index + 1] || "";
      index += 1;
    } else if (arg.startsWith("--command=")) {
      parsed.command = arg.slice("--command=".length);
    } else {
      fail(`unknown argument ${arg}`);
    }
  }
  return parsed;
}

function fail(message) {
  console.error(message);
  process.exit(2);
}
