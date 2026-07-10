import "./env";

const worker = process.env.TEST_PARALLEL_INDEX ?? process.env.TEST_WORKER_INDEX ?? "0";
const runId = process.env.E2E_RUN_ID ?? `${Date.now().toString(36)}-${process.pid.toString(36)}`;

export const E2E_FIXTURE_PASSWORD = process.env.E2E_FIXTURE_PASSWORD || "MulticaE2E1!";
export const DEFAULT_E2E_ACCOUNT =
  process.env.E2E_ACCOUNT ||
  process.env.E2E_FIXTURE_ACCOUNT ||
  `e2e-${worker}-${runId}`;
export const DEFAULT_E2E_NAME =
  process.env.E2E_NAME ||
  process.env.E2E_FIXTURE_NAME ||
  `E2E User ${worker}`;
export const DEFAULT_E2E_PASSWORD = process.env.E2E_ACCOUNT
  ? process.env.E2E_PASSWORD || E2E_FIXTURE_PASSWORD
  : E2E_FIXTURE_PASSWORD;
export const DEFAULT_E2E_WORKSPACE =
  process.env.E2E_WORKSPACE ||
  process.env.E2E_FIXTURE_WORKSPACE ||
  `e2e-workspace-${worker}-${runId}`;
export const DEFAULT_E2E_WORKSPACE_NAME =
  process.env.E2E_WORKSPACE_NAME ||
  process.env.E2E_FIXTURE_WORKSPACE_NAME ||
  `E2E Workspace ${worker}`;
