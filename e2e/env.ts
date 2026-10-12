import { existsSync, readFileSync } from "fs";
import { resolve } from "path";
import { config } from "dotenv";

const envCandidates = process.env.MULTICA_LOCAL_DEV === '1' ? ['.env.worktree', '.env'] : [];

for (const filename of envCandidates) {
  const path = resolve(process.cwd(), filename);
  if (existsSync(path)) {
    config({ path });
    break;
  }
}

if (process.env.MULTICA_LOCAL_DEV !== '1') {
  const deployment = JSON.parse(readFileSync(resolve(process.cwd(), '.run/deployments/goal-test-prod.json'), 'utf8'));
  process.env.FRONTEND_ORIGIN = deployment.frontend_url;
  process.env.E2E_API_BASE_URL = deployment.backend_url;
  process.env.MULTICA_ENV_ID = 'prod';
}
