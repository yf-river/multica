import path from "node:path";
import process from "node:process";

export function acceptanceDir(repoRoot) {
  return path.resolve(process.env.GOAL_TEST_ACCEPTANCE_DIR || path.join(repoRoot, "artifacts", "acceptance"));
}
