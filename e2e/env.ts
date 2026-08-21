import { existsSync } from "fs";
import { resolve } from "path";
import { config } from "dotenv";

const envCandidates = [".env.worktree", ".env"];

for (const filename of envCandidates) {
  const path = resolve(process.cwd(), filename);
  if (existsSync(path)) {
    config({ path });
    break;
  }
}

if (!process.env.PLAYWRIGHT_BASE_URL && process.env.FRONTEND_PORT) {
  process.env.PLAYWRIGHT_BASE_URL = `http://localhost:${process.env.FRONTEND_PORT}`;
}

const noProxyHosts = new Set(
  (process.env.NO_PROXY ?? process.env.no_proxy ?? "")
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean),
);

for (const key of ["PLAYWRIGHT_BASE_URL", "FRONTEND_ORIGIN", "REMOTE_API_URL", "NEXT_PUBLIC_API_URL", "NEXT_PUBLIC_WS_URL"]) {
  const value = process.env[key];
  if (!value) continue;
  try {
    const url = new URL(value);
    if (url.hostname) {
      noProxyHosts.add(url.hostname);
    }
  } catch {
    // Ignore non-URL values; test commands may set these indirectly.
  }
}

for (const host of ["localhost", "127.0.0.1"]) {
  noProxyHosts.add(host);
}

const noProxy = Array.from(noProxyHosts).join(",");
process.env.NO_PROXY = noProxy;
process.env.no_proxy = noProxy;
