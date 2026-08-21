import { mkdirSync } from "node:fs";
import path from "node:path";
import process from "node:process";

export function acceptanceDir(repoRoot, override) {
  return path.resolve(override || process.env.GOAL_TEST_ACCEPTANCE_DIR || path.join(repoRoot, "artifacts", "acceptance"));
}

export function acceptancePath(repoRoot, ...parts) {
  return path.join(acceptanceDir(repoRoot), ...parts);
}

export function ensureAcceptanceDir(repoRoot, override) {
  const dir = acceptanceDir(repoRoot, override);
  mkdirSync(dir, { recursive: true });
  return dir;
}
