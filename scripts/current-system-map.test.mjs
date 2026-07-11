import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const inventory = JSON.parse(
  fs.readFileSync(path.join(root, "docs/architecture/current-system-map.json"), "utf8"),
);

function collectEvidencePaths(value, key = "", result = new Set()) {
  if (Array.isArray(value)) {
    for (const item of value) collectEvidencePaths(item, key, result);
    return result;
  }
  if (!value || typeof value !== "object") {
    if (
      typeof value === "string" &&
      (key === "source" || key === "sources" || key === "referenceSources" || key === "registration" || key === "implementation" || key === "generatedSource")
    ) {
      result.add(value.split("#", 1)[0]);
    }
    return result;
  }
  for (const [childKey, child] of Object.entries(value)) {
    collectEvidencePaths(child, childKey, result);
  }
  return result;
}

test("generated evidence never depends on ignored build output", () => {
  const paths = [...collectEvidencePaths(inventory)].filter((item) => fs.existsSync(path.join(root, item)));
  const check = spawnSync("git", ["check-ignore", "--stdin"], {
    cwd: root,
    encoding: "utf8",
    input: `${paths.join("\n")}\n`,
  });
  assert.equal(check.stdout.trim(), "", `ignored evidence leaked into inventory:\n${check.stdout}`);
  assert.doesNotMatch(JSON.stringify(inventory), /apps\/desktop\/out|\/(?:\.next|dist|build|coverage)\//);
});

test("runtime env helpers and Vite main-process configuration are inventoried", () => {
  const names = new Set(inventory.environment.variables.map((item) => item.name));
  const expected = [
    "MULTICA_AGENT_IDLE_WATCHDOG",
    "MULTICA_AGENT_TIMEOUT",
    "MULTICA_AGENT_TOOL_WATCHDOG",
    "MULTICA_CLAUDE_ARGS",
    "MULTICA_CODEBUDDY_ARGS",
    "MULTICA_CODEX_ARGS",
    "MULTICA_CODEX_MEMORY",
    "MULTICA_CODEX_MIN_TASK_INTERVAL",
    "MULTICA_CODEX_MULTI_AGENT",
    "MULTICA_CODEX_SEMANTIC_INACTIVITY_TIMEOUT",
    "MULTICA_DAEMON_MAX_CONCURRENT_TASKS",
    "MULTICA_GC_ARTIFACT_PATTERNS",
    "MULTICA_GC_ARTIFACT_TTL",
    "MULTICA_GC_INTERVAL",
    "MULTICA_GC_ORPHAN_TTL",
    "MULTICA_GC_TTL",
    "VITE_API_URL",
    "VITE_WS_URL",
    "VITE_APP_URL",
  ];
  assert.deepEqual(expected.filter((name) => !names.has(name)), []);
});

test("implicit database and websocket contracts are visible", () => {
  assert.equal(inventory.persistence.database.functions.length, 9);
  assert.equal(inventory.persistence.database.triggers.length, 4);
  assert.ok(inventory.persistence.database.indexes.length >= 180);
  assert.deepEqual(inventory.websocket.goWithoutProductionReference, [
    "pull_request:linked",
    "pull_request:unlinked",
  ]);
});

test("external and non-page HTTP surfaces are present without Once.Do false positives", () => {
  const systems = new Set(inventory.externalIo.externalSystems.map((item) => item.id));
  assert.ok(systems.has("public-skill-registries"));
  assert.ok(systems.has("user-git-remotes"));

  const outbound = inventory.externalIo.autoDetected.find((item) => item.kind === "outbound-http")?.sources ?? [];
  assert.ok(outbound.includes("server/internal/handler/skill_import_clawhub.go"));
  assert.ok(!outbound.includes("server/internal/auth/jwt.go"));
  assert.ok(!outbound.includes("server/internal/auth/cookie.go"));

  assert.deepEqual(inventory.frontend.webRouteHandlers, [{
    method: "GET",
    path: "/favicon.ico",
    source: "apps/web/app/favicon.ico/route.ts",
  }]);
  assert.equal(inventory.frontend.webProxy?.source, "apps/web/proxy.ts");
  assert.deepEqual(
    inventory.frontend.webRewrites.map((rewrite) => `${rewrite.lane}:${rewrite.source}->${rewrite.destinationTemplate}`),
    [
      "afterFiles:/api/:path*->{remoteApiUrl}/api/:path*",
      "afterFiles:/auth/:path*->{remoteApiUrl}/auth/:path*",
      "afterFiles:/uploads/:path*->{remoteApiUrl}/uploads/:path*",
      "afterFiles:/ws->{remoteApiUrl}/ws",
      "beforeFiles:/docs->{docsUrl}/docs",
      "beforeFiles:/docs/:path*->{docsUrl}/docs/:path*",
    ],
  );
  assert.deepEqual(
    inventory.backend.auxiliaryHttpRoutes.map((route) => `${route.server}:${route.path}`).sort(),
    [
      "CLI loopback OAuth callback:/callback",
      "daemon localhost control listener:/health",
      "daemon localhost control listener:/repo/checkout",
      "daemon localhost control listener:/shutdown",
      "metrics listener:/metrics",
    ],
  );
});

test("maintained domain flows stay anchored to current routes, tables and sources", () => {
  const routeKeys = new Set(
    inventory.backend.chiRoutes.map((route) => `${route.method} ${route.path}`),
  );
  const tableNames = new Set(
    inventory.persistence.database.tables.map((table) => table.name),
  );
  const flows = [
    {
      file: "chat-send-flow.md",
      routes: [
        "POST /api/chat/sessions",
        "POST /api/chat/sessions/{sessionId}/messages",
      ],
      tables: ["chat_idempotency_record"],
      sources: [
        "packages/core/chat/pending-operation-store.ts",
        "server/internal/handler/chat.go",
        "server/internal/service/task_enqueue.go",
      ],
    },
    {
      file: "project-create-flow.md",
      routes: ["POST /api/projects"],
      tables: ["project", "project_create_request", "project_resource"],
      sources: [
        "packages/core/projects/mutations.ts",
        "packages/views/modals/create-project.tsx",
        "server/internal/handler/project.go",
        "server/pkg/db/queries/project_create_request.sql",
      ],
    },
    {
      file: "autopilot-flow.md",
      routes: [
        "POST /api/autopilots",
        "POST /api/autopilots/{id}/trigger",
        "POST /api/webhooks/autopilots/{token}",
      ],
      tables: ["autopilot", "autopilot_run", "autopilot_trigger"],
      sources: [
        "packages/core/autopilots/mutations.ts",
        "packages/core/autopilots/pending-operation-store.ts",
        "server/internal/handler/autopilot.go",
        "server/internal/handler/autopilot_triggers.go",
        "server/internal/service/autopilot.go",
      ],
    },
  ];

  const index = fs.readFileSync(
    path.join(root, "docs/architecture/domain-flows.md"),
    "utf8",
  );
  for (const flow of flows) {
    const flowPath = path.join(root, "docs/architecture", flow.file);
    assert.ok(fs.existsSync(flowPath), `missing maintained flow: ${flow.file}`);
    assert.match(index, new RegExp(flow.file.replaceAll(".", "\\.")));
    const content = fs.readFileSync(flowPath, "utf8");
    for (const route of flow.routes) assert.ok(routeKeys.has(route), `${flow.file}: stale route ${route}`);
    for (const table of flow.tables) assert.ok(tableNames.has(table), `${flow.file}: stale table ${table}`);
    for (const source of flow.sources) {
      assert.ok(fs.existsSync(path.join(root, source)), `${flow.file}: missing source ${source}`);
      assert.ok(content.includes(`\`${source}\``), `${flow.file}: undocumented source ${source}`);
    }
  }
});
