import { execFileSync, spawn } from "node:child_process";
import net from "node:net";
import process from "node:process";

export function startProcess(name, cwd, command, { defaultEnv = {}, extraEnv = {} } = {}) {
  const info = {
    name,
    cwd,
    command: command.map(shellQuote).join(" "),
    pid: null,
    stdout: "",
    stderr: "",
  };
  const child = spawn(command[0], command.slice(1), {
    cwd,
    env: { ...process.env, ...defaultEnv, ...extraEnv },
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
  info.pid = child.pid;
  child.stdout.on("data", (chunk) => {
    info.stdout += chunk.toString();
  });
  child.stderr.on("data", (chunk) => {
    info.stderr += chunk.toString();
  });
  return { name, child, info };
}

export async function stopProcess(proc) {
  if (!proc?.child || hasExited(proc.child)) return;
  try {
    process.kill(-proc.info.pid, "SIGTERM");
  } catch {}
  await waitForExit(proc.child, 1500);
  if (hasExited(proc.child)) return;
  try {
    process.kill(-proc.info.pid, "SIGKILL");
  } catch {}
  await waitForExit(proc.child, 1500);
}

export function collectLogs(proc, { lines = 200 } = {}) {
  if (!proc) return null;
  return {
    stdout_tail: tail(proc.info.stdout, lines),
    stderr_tail: tail(proc.info.stderr, lines),
    pid: proc.info.pid,
    command: proc.info.command,
    cwd: proc.info.cwd,
  };
}

export async function getFreePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      server.close(() => resolve(address.port));
    });
  });
}

export async function waitForTcp(address, timeoutMs, proc, { intervalMs = 250 } = {}) {
  const [host, portText] = address.split(":");
  const port = Number(portText);
  const started = Date.now();
  let lastError = null;
  while (Date.now() - started < timeoutMs) {
    if (proc.child.exitCode !== null) {
      throw new Error(`${proc.name} exited before becoming ready: stdout=${proc.info.stdout} stderr=${proc.info.stderr}`);
    }
    try {
      await tryTcp(host, port);
      return;
    } catch (error) {
      lastError = error;
      await sleep(intervalMs);
    }
  }
  throw new Error(`${proc.name} did not open ${address} within ${timeoutMs}ms: ${lastError?.message || lastError}`);
}

export function repoState(repo) {
  return {
    path: repo,
    branch: exec(repo, ["git", "branch", "--show-current"]).trim(),
    commit: exec(repo, ["git", "rev-parse", "HEAD"]).trim(),
    dirty: exec(repo, ["git", "status", "--short"]).trim() !== "",
  };
}

export function shellQuote(value) {
  const s = String(value);
  if (/^[A-Za-z0-9_./:=@+-]+$/.test(s)) return s;
  return `'${s.replace(/'/g, "'\\''")}'`;
}

function tail(text, lines) {
  return String(text || "").split(/\r?\n/).slice(-lines).join("\n");
}

function tryTcp(host, port) {
  return new Promise((resolve, reject) => {
    const socket = net.createConnection({ host, port });
    socket.once("connect", () => {
      socket.end();
      resolve();
    });
    socket.once("error", reject);
    socket.setTimeout(1000, () => {
      socket.destroy(new Error("tcp connect timeout"));
    });
  });
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function waitForExit(child, timeoutMs) {
  if (hasExited(child)) return Promise.resolve();
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, timeoutMs);
    child.once("exit", () => {
      clearTimeout(timer);
      resolve();
    });
  });
}

function hasExited(child) {
  return child.exitCode !== null || child.signalCode !== null;
}

function exec(cwd, command) {
  return execFileSync(command[0], command.slice(1), {
    cwd,
    encoding: "utf8",
    maxBuffer: 1024 * 1024,
  });
}
