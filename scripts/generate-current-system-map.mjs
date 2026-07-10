#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const checkOnly = process.argv.includes("--check");
const unknownArgs = process.argv.slice(2).filter((arg) => arg !== "--check");

if (unknownArgs.length > 0) {
  throw new Error(`Unknown argument(s): ${unknownArgs.join(", ")}`);
}

const OUTPUT_JSON = "docs/architecture/current-system-map.json";
const OUTPUT_MARKDOWN = "docs/architecture/current-system-map.md";
const OVERRIDES_FILE = "docs/architecture/current-system-map-overrides.json";

const repositoryFiles = new Set(
  execFileSync("git", ["ls-files", "--cached", "--others", "--exclude-standard", "-z"], {
    cwd: root,
    encoding: "utf8",
  })
    .split("\0")
    .filter(Boolean)
    .map(normalizePath),
);

function absolute(relativePath) {
  return path.join(root, relativePath);
}

function normalizePath(filePath) {
  return filePath.split(path.sep).join("/");
}

function read(relativePath) {
  return fs.readFileSync(absolute(relativePath), "utf8");
}

function source(relativePath, locator) {
  return locator ? `${relativePath}#${locator}` : relativePath;
}

function compareText(left, right) {
  return left.localeCompare(right, "en");
}

function uniqueSorted(values) {
  return [...new Set(values)].sort(compareText);
}

function walk(relativeDirectory, predicate = () => true) {
  const normalizedDirectory = normalizePath(relativeDirectory).replace(/\/$/, "");
  const prefix = `${normalizedDirectory}/`;
  return [...repositoryFiles]
    .filter((file) => file.startsWith(prefix) && fs.existsSync(absolute(file)) && predicate(file))
    .sort(compareText);
}

function isTestFile(file) {
  return /(?:^|\/)__tests__(?:\/|$)|_test\.go$|\.(?:test|spec)\.[^/]+$/.test(file);
}

function joinRoute(prefix, child) {
  if (!prefix && child === "/") return "/";
  if (child === "/") return prefix || "/";
  const joined = `${prefix || ""}/${child || ""}`.replace(/\/{2,}/g, "/");
  return joined.startsWith("/") ? joined : `/${joined}`;
}

function loadOverrides() {
  const parsed = JSON.parse(read(OVERRIDES_FILE));
  if (parsed.schemaVersion !== 1) {
    throw new Error(`${OVERRIDES_FILE}: schemaVersion must be 1`);
  }

  for (const key of ["chiRoutes", "auxiliaryHttpRoutes", "webPages", "desktopRoutes", "externalSystems", "environmentAliases", "notes"]) {
    if (!Array.isArray(parsed[key])) throw new Error(`${OVERRIDES_FILE}: ${key} must be an array`);
  }
  if (!parsed.websocketEvents || !Array.isArray(parsed.websocketEvents.go) || !Array.isArray(parsed.websocketEvents.typescript)) {
    throw new Error(`${OVERRIDES_FILE}: websocketEvents.go/typescript must be arrays`);
  }

  const aliasDispositions = new Set([
    "intentional-role-specific",
    "external-protocol-required",
    "legacy-removal-candidate",
  ]);
  for (const alias of parsed.environmentAliases) {
    if (!alias.canonical || !Array.isArray(alias.aliases) || !Array.isArray(alias.sources) || !aliasDispositions.has(alias.disposition)) {
      throw new Error(`${OVERRIDES_FILE}: every environment alias needs canonical, aliases, sources, and a valid disposition`);
    }
    for (const evidencePath of alias.sources) {
      if (!repositoryFiles.has(evidencePath) || !fs.existsSync(absolute(evidencePath))) {
        throw new Error(`${OVERRIDES_FILE}: environment alias source does not exist: ${evidencePath}`);
      }
    }
  }

  for (const route of parsed.auxiliaryHttpRoutes) {
    if (!route.method || !route.path || !route.server || !repositoryFiles.has(route.source) || !fs.existsSync(absolute(route.source))) {
      throw new Error(`${OVERRIDES_FILE}: every auxiliary HTTP route needs method, path, server, and a repository source`);
    }
  }

  for (const system of parsed.externalSystems) {
    if (!system.id || !system.kind || !Array.isArray(system.sources) || !Array.isArray(system.environment)) {
      throw new Error(`${OVERRIDES_FILE}: every external system needs id, kind, sources, and environment`);
    }
    for (const evidencePath of system.sources) {
      if (!repositoryFiles.has(evidencePath) || !fs.existsSync(absolute(evidencePath))) {
        throw new Error(`${OVERRIDES_FILE}: external system source does not exist: ${evidencePath}`);
      }
    }
  }

  return parsed;
}

function buildFunctionIndex() {
  const files = [
    ...walk("server/internal/handler", (file) => file.endsWith(".go") && !isTestFile(file)),
    ...walk("server/cmd/server", (file) => file.endsWith(".go") && !isTestFile(file)),
  ];
  const index = new Map();

  for (const file of files) {
    const lines = read(file).split("\n");
    lines.forEach((line) => {
      const match = line.match(/^func\s+(?:\([^)]*\)\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*\(/);
      if (!match) return;
      const locations = index.get(match[1]) ?? [];
      locations.push(source(file, match[1]));
      index.set(match[1], locations);
    });
  }

  return index;
}

function codeBraceDelta(line, lexerState) {
  let delta = 0;
  let quote = lexerState.quote;
  let blockComment = lexerState.blockComment;
  let escaped = false;

  for (let index = 0; index < line.length; index += 1) {
    const char = line[index];
    const next = line[index + 1];

    if (blockComment) {
      if (char === "*" && next === "/") {
        blockComment = false;
        index += 1;
      }
      continue;
    }
    if (quote) {
      if (quote !== "`" && escaped) {
        escaped = false;
      } else if (quote !== "`" && char === "\\") {
        escaped = true;
      } else if (char === quote) {
        quote = null;
      }
      continue;
    }
    if (char === "/" && next === "/") break;
    if (char === "/" && next === "*") {
      blockComment = true;
      index += 1;
      continue;
    }
    if (char === '"' || char === "'" || char === "`") {
      quote = char;
      continue;
    }
    if (char === "{") delta += 1;
    if (char === "}") delta -= 1;
  }

  lexerState.quote = quote;
  lexerState.blockComment = blockComment;
  return delta;
}

function resolveHandler(handlerExpression, functionIndex) {
  if (/^func\s*\(/.test(handlerExpression.trim())) {
    return { handler: "anonymous", implementation: null };
  }

  const handlerMethod = handlerExpression.match(/\bh\.([A-Za-z_][A-Za-z0-9_]*)\b/);
  if (handlerMethod) {
    const locations = functionIndex.get(handlerMethod[1]) ?? [];
    return { handler: `h.${handlerMethod[1]}`, implementation: locations[0] ?? null };
  }

  const named = handlerExpression.trim().match(/^([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*)/);
  if (!named) return { handler: handlerExpression.trim(), implementation: null };
  const functionName = named[1].split(".").at(-1);
  const locations = functionIndex.get(functionName) ?? [];
  return { handler: named[1], implementation: locations[0] ?? null };
}

function parseChiRoutes(overrides, functionIndex) {
  const routerFile = "server/cmd/server/router.go";
  const lines = read(routerFile).split("\n");
  const routes = [];
  const scopes = [];
  const lexerState = { quote: null, blockComment: false };
  let depth = 0;

  lines.forEach((line) => {
    while (scopes.length > 0 && scopes.at(-1).depth > depth) scopes.pop();
    const prefix = scopes.at(-1)?.prefix ?? "";
    const trimmed = line.trim();

    if (!trimmed.startsWith("//")) {
      const methodMatch = line.match(/\.(Get|Post|Put|Patch|Delete|Options|Head)\(\s*"([^"]*)"\s*,\s*(.+)$/);
      if (methodMatch) {
        const resolved = resolveHandler(methodMatch[3], functionIndex);
        routes.push({
          method: methodMatch[1].toUpperCase(),
          path: joinRoute(prefix, methodMatch[2]),
          handler: resolved.handler,
          implementation: resolved.implementation,
          registration: source(routerFile, `${methodMatch[1].toUpperCase()} ${joinRoute(prefix, methodMatch[2])}`),
          middlewareWrapper: line.includes(".With("),
          origin: "static",
        });
      }
    }

    const routeScope = trimmed.startsWith("//") ? null : line.match(/\br\.Route\(\s*"([^"]+)"\s*,\s*func\s*\(r\s+chi\.Router\)\s*\{/);
    const nextDepth = depth + codeBraceDelta(line, lexerState);
    if (routeScope && nextDepth > depth) {
      scopes.push({ depth: nextDepth, prefix: joinRoute(prefix, routeScope[1]) });
    }
    depth = nextDepth;
  });

  for (const override of overrides.chiRoutes) {
    routes.push({ ...override, origin: "manual-override" });
  }

  return routes.sort((a, b) =>
    compareText(a.path, b.path) || compareText(a.method, b.method) || compareText(a.registration, b.registration),
  );
}

function webRouteFromPage(file) {
  const relative = file.slice("apps/web/app/".length, -"/page.tsx".length);
  const segments = relative
    .split("/")
    .filter(Boolean)
    .filter((segment) => !(segment.startsWith("(") && segment.endsWith(")")))
    .map((segment) => {
      const optionalCatchAll = segment.match(/^\[\[\.\.\.(.+)\]\]$/);
      if (optionalCatchAll) return `*${optionalCatchAll[1]}?`;
      const catchAll = segment.match(/^\[\.\.\.(.+)\]$/);
      if (catchAll) return `*${catchAll[1]}`;
      const dynamic = segment.match(/^\[(.+)\]$/);
      return dynamic ? `:${dynamic[1]}` : segment;
    });
  return `/${segments.join("/")}`.replace(/\/$/, "") || "/";
}

function parseWebPages(overrides) {
  const pages = walk("apps/web/app", (file) => file.endsWith("/page.tsx") || file === "apps/web/app/page.tsx")
    .map((file) => ({ path: webRouteFromPage(file), source: file, origin: "static" }));
  for (const override of overrides.webPages) pages.push({ ...override, origin: "manual-override" });
  return pages.sort((a, b) => compareText(a.path, b.path) || compareText(a.source, b.source));
}

function webRouteFromHandler(file) {
  const relative = file
    .slice("apps/web/app/".length)
    .replace(/\/route\.(?:ts|tsx|js|jsx)$/, "");
  return `/${relative}`.replace(/\/$/, "") || "/";
}

function parseWebRouteHandlers() {
  return walk("apps/web/app", (file) => /\/route\.(?:ts|tsx|js|jsx)$/.test(file))
    .flatMap((file) => {
      const methods = uniqueSorted(
        [...read(file).matchAll(/export\s+(?:async\s+)?function\s+(GET|POST|PUT|PATCH|DELETE|OPTIONS|HEAD)\s*\(/g)]
          .map((match) => match[1]),
      );
      return methods.map((method) => ({ method, path: webRouteFromHandler(file), source: file }));
    })
    .sort((a, b) => compareText(a.path, b.path) || compareText(a.method, b.method));
}

function parseWebRewrites() {
  const configFile = "apps/web/next.config.ts";
  const resolverFile = "apps/web/config/runtime-urls.ts";
  const content = read(configFile);
  const rewrites = [];
  const pattern = /\{\s*source:\s*"([^"]+)"\s*,\s*destination:\s*`([^`]+)`\s*,?\s*\}/g;
  for (const match of content.matchAll(pattern)) {
    const beforeFilesIndex = content.lastIndexOf("beforeFiles:", match.index);
    const afterFilesIndex = content.lastIndexOf("afterFiles:", match.index);
    const lane = beforeFilesIndex > afterFilesIndex ? "beforeFiles" : "afterFiles";
    const usesDocs = match[2].includes("${docsUrl}");
    const usesRemoteAPI = match[2].includes("${remoteApiUrl}");
    rewrites.push({
      lane,
      source: match[1],
      destinationTemplate: match[2]
        .replace("${docsUrl}", "{docsUrl}")
        .replace("${remoteApiUrl}", "{remoteApiUrl}"),
      environment: usesDocs
        ? ["DOCS_URL"]
        : usesRemoteAPI
          ? ["GOAL_TEST_REMOTE_API_URL", "REMOTE_API_URL", "NEXT_PUBLIC_API_URL", "BACKEND_PORT", "API_PORT", "SERVER_PORT", "PORT"]
          : [],
      sourceFile: configFile,
      resolverSource: usesRemoteAPI ? resolverFile : configFile,
    });
  }
  return rewrites.sort((a, b) => compareText(a.lane, b.lane) || compareText(a.source, b.source));
}

function parseDesktopRoutes(overrides) {
  const routeFile = "apps/desktop/src/renderer/src/routes.tsx";
  const lines = read(routeFile).split("\n");
  const routes = [];
  const stack = [];
  let active = false;

  lines.forEach((line) => {
    if (line.includes("export const appRoutes:")) active = true;
    if (!active) return;
    if (line.includes("Create an independent memory router")) {
      active = false;
      return;
    }

    const events = /\{|\}|path\s*:\s*("(?:[^"\\]|\\.)*")/g;
    for (const match of line.matchAll(events)) {
      if (match[0] === "{") {
        stack.push({ path: null });
      } else if (match[0] === "}") {
        stack.pop();
      } else {
        const literal = JSON.parse(match[1]);
        if (stack.length === 0) stack.push({ path: null });
        const current = stack.at(-1);
        const parents = stack.slice(0, -1).map((entry) => entry.path).filter(Boolean);
        current.path = literal;
        const fullPath = [...parents, literal].reduce((prefix, part) => joinRoute(prefix, part), "");
        routes.push({ literal, path: fullPath, source: source(routeFile, fullPath), origin: "static" });
      }
    }
  });

  for (const override of overrides.desktopRoutes) routes.push({ ...override, origin: "manual-override" });
  return routes.sort((a, b) => compareText(a.path, b.path) || compareText(a.source, b.source));
}

function parseDatabase() {
  const migrationFiles = walk("server/migrations", (file) => file.endsWith(".sql"));
  const migrations = migrationFiles.map((file) => {
    const filename = path.basename(file);
    const match = filename.match(/^(\d+)_(.+)\.(up|down)\.sql$/);
    if (!match) throw new Error(`Unexpected migration filename: ${file}`);
    const content = read(file);
    const tablesCreated = [];
    const functionsCreated = [];
    const triggersCreated = [];
    const indexesCreated = [];
    const tablePattern = /\bCREATE\s+TABLE(?:\s+IF\s+NOT\s+EXISTS)?\s+([A-Za-z0-9_".]+)/gi;
    for (const tableMatch of content.matchAll(tablePattern)) {
      const table = tableMatch[1].replaceAll('"', "").split(".").at(-1);
      tablesCreated.push({ name: table, source: source(file, table) });
    }
    for (const functionMatch of content.matchAll(/\bCREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+([A-Za-z0-9_".]+)\s*\(/gi)) {
      const name = functionMatch[1].replaceAll('"', "");
      functionsCreated.push({ name, source: source(file, name) });
    }
    for (const triggerMatch of content.matchAll(/\bCREATE\s+TRIGGER\s+([A-Za-z0-9_"]+)\s+([\s\S]*?);/gi)) {
      const name = triggerMatch[1].replaceAll('"', "");
      const body = triggerMatch[2];
      const tableMatch = body.match(/\bON\s+([A-Za-z0-9_".]+)/i);
      const functionMatch = body.match(/\bEXECUTE\s+(?:FUNCTION|PROCEDURE)\s+([A-Za-z0-9_".]+)/i);
      triggersCreated.push({
        name,
        table: tableMatch ? tableMatch[1].replaceAll('"', "") : null,
        function: functionMatch ? functionMatch[1].replaceAll('"', "") : null,
        source: source(file, name),
      });
    }
    for (const indexMatch of content.matchAll(/\bCREATE\s+(UNIQUE\s+)?INDEX(?:\s+IF\s+NOT\s+EXISTS)?\s+([A-Za-z0-9_".]+)\s+ON\s+(?:ONLY\s+)?([A-Za-z0-9_".]+)/gi)) {
      const name = indexMatch[2].replaceAll('"', "");
      indexesCreated.push({
        name,
        table: indexMatch[3].replaceAll('"', ""),
        unique: Boolean(indexMatch[1]),
        source: source(file, name),
      });
    }
    return {
      version: Number(match[1]),
      name: match[2],
      direction: match[3],
      source: file,
      tablesCreated,
      functionsCreated,
      triggersCreated,
      indexesCreated,
      droppedFunctions: [...content.matchAll(/\bDROP\s+FUNCTION(?:\s+IF\s+EXISTS)?\s+([A-Za-z0-9_".]+)/gi)]
        .map((item) => item[1].replaceAll('"', "")),
      droppedTriggers: [...content.matchAll(/\bDROP\s+TRIGGER(?:\s+IF\s+EXISTS)?\s+([A-Za-z0-9_"]+)/gi)]
        .map((item) => item[1].replaceAll('"', "")),
      droppedIndexes: [...content.matchAll(/\bDROP\s+INDEX(?:\s+IF\s+EXISTS)?\s+([A-Za-z0-9_".]+)/gi)]
        .map((item) => item[1].replaceAll('"', "")),
    };
  }).sort((a, b) => a.version - b.version || compareText(a.direction, b.direction));

  const tableByName = new Map();
  const functionByName = new Map();
  const triggerByName = new Map();
  const indexByName = new Map();
  for (const migration of migrations.filter((item) => item.direction === "up")) {
    for (const table of migration.tablesCreated) {
      if (!tableByName.has(table.name)) tableByName.set(table.name, table);
    }
    for (const name of migration.droppedFunctions) functionByName.delete(name);
    for (const name of migration.droppedTriggers) triggerByName.delete(name);
    for (const name of migration.droppedIndexes) indexByName.delete(name);
    for (const item of migration.functionsCreated) functionByName.set(item.name, item);
    for (const item of migration.triggersCreated) triggerByName.set(item.name, item);
    for (const item of migration.indexesCreated) indexByName.set(item.name, item);
  }

  return {
    migrations,
    tables: [...tableByName.values()].sort((a, b) => compareText(a.name, b.name)),
    functions: [...functionByName.values()].sort((a, b) => compareText(a.name, b.name)),
    triggers: [...triggerByName.values()].sort((a, b) => compareText(a.name, b.name)),
    indexes: [...indexByName.values()].sort((a, b) => compareText(a.name, b.name)),
  };
}

function parseSqlc() {
  const modules = walk("server/pkg/db/queries", (file) => file.endsWith(".sql")).map((file) => {
    const content = read(file);
    const queries = [];
    const queryPattern = /^--\s+name:\s+([A-Za-z_][A-Za-z0-9_]*)\s+:(\S+)/gm;
    for (const match of content.matchAll(queryPattern)) {
      queries.push({ name: match[1], command: match[2], source: source(file, match[1]) });
    }
    return {
      module: path.basename(file, ".sql"),
      source: file,
      generatedSource: `server/pkg/db/generated/${path.basename(file, ".sql")}.sql.go`,
      queries: queries.sort((a, b) => compareText(a.name, b.name)),
    };
  });

  return {
    config: "server/sqlc.yaml",
    generatedPackage: "server/pkg/db/generated",
    modules: modules.sort((a, b) => compareText(a.module, b.module)),
  };
}

function parseWebsocketEvents(overrides) {
  const goFile = "server/pkg/protocol/events.go";
  const tsFile = "packages/core/types/events.ts";
  const goEvents = [];
  const tsEvents = [];

  read(goFile).split("\n").forEach((line) => {
    const match = line.match(/^\s*(Event[A-Za-z0-9_]+)\s*=\s*"([^"]+)"/);
    if (match) goEvents.push({ name: match[2], constant: match[1], source: source(goFile, match[1]), origin: "static" });
  });

  const tsContent = read(tsFile);
  const union = tsContent.match(/export\s+type\s+WSEventType\s*=([\s\S]*?);/);
  if (!union) throw new Error(`Unable to find WSEventType in ${tsFile}`);
  for (const match of union[1].matchAll(/"([^"]+)"/g)) {
    tsEvents.push({ name: match[1], source: source(tsFile, match[1]), origin: "static" });
  }

  for (const event of overrides.websocketEvents.go) goEvents.push({ ...event, origin: "manual-override" });
  for (const event of overrides.websocketEvents.typescript) tsEvents.push({ ...event, origin: "manual-override" });

  const goReferenceFiles = walk("server", (file) =>
    file.endsWith(".go") && !isTestFile(file) && file !== goFile,
  );
  const typescriptReferenceFiles = [
    ...walk("packages", (file) => /\.(?:ts|tsx)$/.test(file) && !isTestFile(file) && file !== tsFile),
    ...walk("apps", (file) => /\.(?:ts|tsx)$/.test(file) && !isTestFile(file)),
  ];
  const uncommented = new Map();
  const contentWithoutComments = (file) => {
    if (!uncommented.has(file)) {
      uncommented.set(file, read(file).replace(/\/\*[\s\S]*?\*\//g, "").replace(/\/\/.*$/gm, ""));
    }
    return uncommented.get(file);
  };
  for (const event of goEvents) {
    const quotedEvent = new RegExp(`["']${event.name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}["']`);
    event.referenceSources = goReferenceFiles.filter((file) =>
      (event.constant && new RegExp(`\\b${event.constant}\\b`).test(contentWithoutComments(file))) ||
        quotedEvent.test(contentWithoutComments(file)),
    );
    event.staticReferenceMissing = event.referenceSources.length === 0;
  }
  for (const event of tsEvents) {
    const quotedEvent = new RegExp(`["']${event.name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}["']`);
    event.referenceSources = typescriptReferenceFiles.filter((file) => quotedEvent.test(contentWithoutComments(file)));
    event.staticReferenceMissing = event.referenceSources.length === 0;
  }

  goEvents.sort((a, b) => compareText(a.name, b.name));
  tsEvents.sort((a, b) => compareText(a.name, b.name));
  const goNames = new Set(goEvents.map((event) => event.name));
  const tsNames = new Set(tsEvents.map((event) => event.name));

  return {
    go: goEvents,
    typescript: tsEvents,
    shared: [...goNames].filter((name) => tsNames.has(name)).sort(compareText),
    goOnly: [...goNames].filter((name) => !tsNames.has(name)).sort(compareText),
    typescriptOnly: [...tsNames].filter((name) => !goNames.has(name)).sort(compareText),
    goWithoutProductionReference: goEvents.filter((event) => event.staticReferenceMissing).map((event) => event.name),
    typescriptWithoutLiteralReference: tsEvents.filter((event) => event.staticReferenceMissing).map((event) => event.name),
  };
}

function countMatches(content, pattern) {
  return [...content.matchAll(pattern)].length;
}

function stateScope(file) {
  const [first, second] = file.split("/");
  return first === "packages" || first === "apps" ? `${first}/${second}` : first;
}

function parseStateManagement() {
  const sourceFiles = [
    ...walk("packages", (file) => /\.(?:ts|tsx)$/.test(file) && !isTestFile(file)),
    ...walk("apps/web", (file) => /\.(?:ts|tsx)$/.test(file) && !isTestFile(file)),
    ...walk("apps/desktop/src/renderer/src", (file) => /\.(?:ts|tsx)$/.test(file) && !isTestFile(file)),
  ];
  const zustandStores = [];
  const reactQueryConsumers = [];
  const queryKeyFactories = [];

  for (const file of sourceFiles) {
    const content = read(file);
    const definesZustandStore = /(?<!\.)\b(?:create|createStore|createWithEqualityFn)\s*(?:<|\()/.test(content);
    if (/from\s+["']zustand(?:\/[^"']*)?["']/.test(content) && definesZustandStore) {
      const exportedNames = [];
      for (const match of content.matchAll(/export\s+(?:const|function)\s+([A-Za-z_][A-Za-z0-9_]*(?:Store|store)[A-Za-z0-9_]*)/g)) {
        exportedNames.push(match[1]);
      }
      zustandStores.push({
        source: file,
        scope: stateScope(file),
        exports: uniqueSorted(exportedNames),
        persisted: /\bpersist\s*\(/.test(content),
      });
    }

    if (content.includes("@tanstack/react-query")) {
      const operations = {
        useQuery: countMatches(content, /\buseQuery\s*(?:<[^;{}()]*>)?\s*\(/g),
        useInfiniteQuery: countMatches(content, /\buseInfiniteQuery\s*(?:<[^;{}()]*>)?\s*\(/g),
        useMutation: countMatches(content, /\buseMutation\s*(?:<[^;{}()]*>)?\s*\(/g),
        queryOptions: countMatches(content, /\bqueryOptions\s*(?:<[^;{}()]*>)?\s*\(/g),
        infiniteQueryOptions: countMatches(content, /\binfiniteQueryOptions\s*(?:<[^;{}()]*>)?\s*\(/g),
      };
      reactQueryConsumers.push({ source: file, scope: stateScope(file), operations });
    }

    for (const match of content.matchAll(/export\s+const\s+([A-Za-z_][A-Za-z0-9_]*Keys)\s*=/g)) {
      queryKeyFactories.push({ name: match[1], source: source(file, match[1]) });
    }
  }

  zustandStores.sort((a, b) => compareText(a.source, b.source));
  reactQueryConsumers.sort((a, b) => compareText(a.source, b.source));
  queryKeyFactories.sort((a, b) => compareText(a.name, b.name) || compareText(a.source, b.source));

  const operationTotals = reactQueryConsumers.reduce((totals, consumer) => {
    for (const [operation, count] of Object.entries(consumer.operations)) totals[operation] += count;
    return totals;
  }, { useQuery: 0, useInfiniteQuery: 0, useMutation: 0, queryOptions: 0, infiniteQueryOptions: 0 });

  return { zustandStores, reactQuery: { operationTotals, queryKeyFactories, consumers: reactQueryConsumers } };
}

function addEnvironmentReference(references, name, file, kind) {
  if (!references.has(name)) references.set(name, []);
  references.get(name).push({ source: file, kind });
}

function environmentConstants(content) {
  const constants = new Map();
  for (const match of content.matchAll(/\b([A-Za-z_][A-Za-z0-9_]*)\s*(?:string\s*)?=\s*"([A-Z][A-Z0-9_]*)"/g)) {
    constants.set(match[1], match[2]);
  }
  return constants;
}

function environmentClassification(references) {
  const sources = references.map((reference) => reference.source);
  if (sources.every((file) => file === ".env.example")) return "example-only";
  if (sources.some((file) => file.startsWith("server/") || file.startsWith("apps/") || file.startsWith("packages/"))) {
    return "runtime";
  }
  if (sources.some((file) => file.startsWith("docker-compose"))) return "deployment";
  return "tooling-or-test";
}

function parseEnvironment(overrides) {
  const references = new Map();
  const runtimeFiles = [
    ...walk("server", (file) => file.endsWith(".go") && !isTestFile(file)),
    ...walk("apps", (file) => /\.(?:ts|tsx|js|mjs|cjs)$/.test(file) && !isTestFile(file)),
    ...walk("packages", (file) => /\.(?:ts|tsx|js|mjs|cjs)$/.test(file) && !isTestFile(file)),
    ...walk("scripts", (file) => /\.(?:js|mjs|cjs)$/.test(file) && !isTestFile(file)),
  ];

  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    if (entry.isFile() && /^(?:docker-compose.*\.ya?ml)$/.test(entry.name)) runtimeFiles.push(entry.name);
  }

  for (const file of uniqueSorted(runtimeFiles)) {
    const content = read(file);
    const patterns = file.endsWith(".go")
      ? [
          { kind: "go-runtime", pattern: /\bos\.(?:Getenv|LookupEnv)\(\s*"([A-Z][A-Z0-9_]*)"\s*\)/g },
          {
            kind: "go-env-helper",
            pattern: /\b(?:env[A-Za-z0-9_]*|mustEnv|[A-Za-z_][A-Za-z0-9_]*(?:Env|FromEnv)[A-Za-z0-9_]*|probe)\(\s*"([A-Z][A-Z0-9_]*)"/g,
          },
        ]
      : /\.(?:ts|tsx|js|mjs|cjs)$/.test(file)
        ? [
            { kind: "node-runtime", pattern: /\bprocess\.env\.([A-Z][A-Z0-9_]*)\b/g },
            { kind: "node-runtime", pattern: /\bprocess\.env\[['"]([A-Z][A-Z0-9_]*)['"]\]/g },
            { kind: "vite-runtime", pattern: /\bimport\.meta\.env\.([A-Z][A-Z0-9_]*)\b/g },
            { kind: "vite-runtime", pattern: /\b[A-Za-z_][A-Za-z0-9_]*\.((?:VITE|NEXT_PUBLIC)_[A-Z0-9_]*)\b/g },
          ]
        : [
            { kind: "template-reference", pattern: /\$\{([A-Z][A-Z0-9_]*)(?::[-+?][^}]*)?\}/g },
          ];
    for (const { kind, pattern } of patterns) {
      for (const match of content.matchAll(pattern)) {
        addEnvironmentReference(references, match[1], file, kind);
      }
    }

    if (file.endsWith(".go")) {
      const constants = environmentConstants(content);
      const symbolicPatterns = [
        { kind: "go-runtime-constant", pattern: /\bos\.(?:Getenv|LookupEnv)\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)/g },
        {
          kind: "go-env-helper-constant",
          pattern: /\b(?:env[A-Za-z0-9_]*|mustEnv|[A-Za-z_][A-Za-z0-9_]*(?:Env|FromEnv)[A-Za-z0-9_]*|probe)\(\s*([A-Za-z_][A-Za-z0-9_]*)/g,
        },
      ];
      for (const { kind, pattern } of symbolicPatterns) {
        for (const match of content.matchAll(pattern)) {
          const name = constants.get(match[1]);
          if (name) addEnvironmentReference(references, name, file, kind);
        }
      }
    }
  }

  if (fs.existsSync(absolute(".env.example"))) {
    const file = ".env.example";
    read(file).split("\n").forEach((line) => {
      const match = line.match(/^\s*#?\s*([A-Z][A-Z0-9_]*)=/);
      if (match) addEnvironmentReference(references, match[1], file, "example-declaration");
    });
  }

  const variables = [...references.entries()].map(([name, refs]) => ({
    name,
    references: [...new Map(refs.map((ref) => [`${ref.source}|${ref.kind}`, ref])).values()].sort((a, b) =>
      compareText(a.source, b.source) || compareText(a.kind, b.kind),
    ),
  })).map((variable) => ({
    ...variable,
    classification: environmentClassification(variable.references),
  })).sort((a, b) => compareText(a.name, b.name));

  return {
    variables,
    aliases: [...overrides.environmentAliases].sort((a, b) => compareText(a.canonical, b.canonical)),
  };
}

function firstEvidence(file, content, pattern) {
  const match = pattern.exec(content);
  pattern.lastIndex = 0;
  return match ? file : null;
}

function parseExternalIo(overrides) {
  const categories = new Map();
  const add = (kind, evidence) => {
    if (!evidence) return;
    if (!categories.has(kind)) categories.set(kind, []);
    categories.get(kind).push(evidence);
  };

  for (const file of walk("server", (candidate) =>
    candidate.endsWith(".go") && !isTestFile(candidate) && !candidate.startsWith("server/pkg/db/generated/"),
  )) {
    const content = read(file);
    add("postgresql", firstEvidence(file, content, /github\.com\/jackc\/pgx\/v5/));
    add("redis", firstEvidence(file, content, /github\.com\/redis\/go-redis\/v9/));
    add("object-storage", firstEvidence(file, content, /github\.com\/aws\/aws-sdk-go-v2\/service\/s3/));
    add("websocket", firstEvidence(file, content, /github\.com\/gorilla\/websocket/));
    const importsNetHTTP = /["']net\/http["']/.test(content);
    const createsHTTPRequest = /\bhttp\.(?:NewRequest(?:WithContext)?|Get|Post)\s*\(/.test(content);
    const httpClientNames = [...content.matchAll(/\b([A-Za-z_][A-Za-z0-9_]*)\s+\*http\.Client\b/g)]
      .map((match) => match[1]);
    const usesTypedHTTPClient = httpClientNames.some((name) =>
      new RegExp(`\\b${name}\\.(?:Do|Get|Post)\\s*\\(`).test(content),
    );
    add("outbound-http", importsNetHTTP && (createsHTTPRequest || usesTypedHTTPClient) ? file : null);
    add("filesystem", firstEvidence(file, content, /os\.(?:ReadFile|WriteFile|Open|OpenFile|Create|Mkdir|MkdirAll|Remove|RemoveAll|Rename)\s*\(/));
    add("subprocess", firstEvidence(file, content, /exec\.Command(?:Context)?\s*\(/));
  }

  const autoDetected = [...categories.entries()].map(([kind, sources]) => ({
    kind,
    sources: uniqueSorted(sources),
  })).sort((a, b) => compareText(a.kind, b.kind));

  const externalSystems = [...overrides.externalSystems]
    .map((system) => ({ ...system, sources: [...system.sources].sort(compareText), environment: [...system.environment].sort(compareText) }))
    .sort((a, b) => compareText(a.id, b.id));

  return { autoDetected, externalSystems };
}

function aggregateByScope(items) {
  const counts = new Map();
  for (const item of items) counts.set(item.scope, (counts.get(item.scope) ?? 0) + 1);
  return [...counts.entries()].map(([scope, count]) => ({ scope, count })).sort((a, b) => compareText(a.scope, b.scope));
}

function buildMap(overrides) {
  const functionIndex = buildFunctionIndex();
  const chiRoutes = parseChiRoutes(overrides, functionIndex);
  const webPages = parseWebPages(overrides);
  const webRouteHandlers = parseWebRouteHandlers();
  const webRewrites = parseWebRewrites();
  const desktopRoutes = parseDesktopRoutes(overrides);
  const database = parseDatabase();
  const sqlc = parseSqlc();
  const websocket = parseWebsocketEvents(overrides);
  const stateManagement = parseStateManagement();
  const environment = parseEnvironment(overrides);
  const externalIo = parseExternalIo(overrides);
  const webPaths = new Set(webPages.map((page) => page.path));
  const desktopPaths = new Set(desktopRoutes.map((route) => route.path));

  return {
    schemaVersion: 1,
    generatedBy: "scripts/generate-current-system-map.mjs",
    inputs: {
      chiRouter: "server/cmd/server/router.go",
      webPages: "apps/web/app/**/page.tsx",
      webRouteHandlers: "apps/web/app/**/route.ts",
      webProxy: "apps/web/proxy.ts",
      webRewrites: "apps/web/next.config.ts + apps/web/config/runtime-urls.ts",
      desktopRouter: "apps/desktop/src/renderer/src/routes.tsx",
      migrations: "server/migrations/*.sql",
      sqlc: "server/sqlc.yaml + server/pkg/db/queries/*.sql",
      goWebsocketEvents: "server/pkg/protocol/events.go",
      typescriptWebsocketEvents: "packages/core/types/events.ts",
      manualOverrides: OVERRIDES_FILE,
    },
    summary: {
      chiRoutes: chiRoutes.length,
      webPages: webPages.length,
      webRouteHandlers: webRouteHandlers.length,
      webRewrites: webRewrites.length,
      desktopRouteLiterals: desktopRoutes.length,
      databaseTables: database.tables.length,
      databaseFunctions: database.functions.length,
      databaseTriggers: database.triggers.length,
      databaseIndexes: database.indexes.length,
      migrations: database.migrations.length,
      sqlcModules: sqlc.modules.length,
      sqlcQueries: sqlc.modules.reduce((total, module) => total + module.queries.length, 0),
      goWebsocketEvents: websocket.go.length,
      typescriptWebsocketEvents: websocket.typescript.length,
      zustandStores: stateManagement.zustandStores.length,
      reactQueryConsumerFiles: stateManagement.reactQuery.consumers.length,
      environmentVariables: environment.variables.length,
      manualExternalSystems: externalIo.externalSystems.length,
    },
    backend: {
      chiRoutes,
      auxiliaryHttpRoutes: [...overrides.auxiliaryHttpRoutes]
        .sort((a, b) => compareText(a.server, b.server) || compareText(a.path, b.path)),
    },
    frontend: {
      webPages,
      webRouteHandlers,
      webRewrites,
      webProxy: repositoryFiles.has("apps/web/proxy.ts") && fs.existsSync(absolute("apps/web/proxy.ts")) ? {
        source: "apps/web/proxy.ts",
        matcher: read("apps/web/proxy.ts").match(/matcher:\s*\["([^"]+)"\]/)?.[1] ?? null,
      } : null,
      desktopRoutes,
      routeDifferences: {
        webOnly: [...webPaths].filter((route) => !desktopPaths.has(route)).sort(compareText),
        desktopOnly: [...desktopPaths].filter((route) => !webPaths.has(route)).sort(compareText),
      },
    },
    persistence: { database, sqlc },
    websocket,
    stateManagement: {
      ...stateManagement,
      zustandByScope: aggregateByScope(stateManagement.zustandStores),
      reactQueryByScope: aggregateByScope(stateManagement.reactQuery.consumers),
    },
    environment,
    externalIo,
    manualNotes: [...overrides.notes].sort((a, b) => compareText(a.id, b.id)),
  };
}

function escapeTable(value) {
  if (value === null || value === undefined || value === "") return "—";
  return String(value).replaceAll("|", "\\|").replaceAll("\n", " ");
}

function table(headers, rows) {
  return [
    `| ${headers.map(escapeTable).join(" | ")} |`,
    `| ${headers.map(() => "---").join(" | ")} |`,
    ...rows.map((row) => `| ${row.map(escapeTable).join(" | ")} |`),
  ].join("\n");
}

function renderMarkdown(systemMap) {
  const s = systemMap.summary;
  const reactQueryScope = systemMap.stateManagement.reactQueryByScope;
  const goEventNames = systemMap.websocket.go.map((event) => `\`${event.name}\``);
  const tsEventNames = systemMap.websocket.typescript.map((event) => `\`${event.name}\``);

  return `# Generated current-system inventory

> Generated by \`pnpm generate:current-system-map\`. Do not edit this file or
> \`current-system-map.json\` by hand. Edit source code or
> \`current-system-map-overrides.json\`, regenerate, and commit the result.

This document is the deterministic static inventory that feeds the maintained
architecture map; it is not, by itself, a claim that domain calls and recovery
semantics are fully documented. CI runs
\`pnpm check:current-system-map\` so architecture drift is visible in the same
change that causes it. The JSON companion contains every discovered record and
is the machine-readable source for audits; this Markdown file keeps the same
evidence reviewable by humans.

## Inventory summary

${table(["Surface", "Count"], [
  ["Go Chi routes", s.chiRoutes],
  ["Next.js pages", s.webPages],
  ["Next.js route handlers", s.webRouteHandlers],
  ["Next.js rewrites", s.webRewrites],
  ["Desktop route literals", s.desktopRouteLiterals],
  ["Database tables", s.databaseTables],
  ["Database functions", s.databaseFunctions],
  ["Database triggers", s.databaseTriggers],
  ["Database indexes", s.databaseIndexes],
  ["Migration files (up + down)", s.migrations],
  ["sqlc modules", s.sqlcModules],
  ["sqlc queries", s.sqlcQueries],
  ["Go WebSocket events", s.goWebsocketEvents],
  ["TypeScript WebSocket events", s.typescriptWebsocketEvents],
  ["Zustand store definitions", s.zustandStores],
  ["React Query consumer files", s.reactQueryConsumerFiles],
  ["Environment variable names", s.environmentVariables],
  ["Manually identified external systems", s.manualExternalSystems],
])}

## Backend HTTP surface

The registration source is always the Chi router. \`Implementation\` is resolved
when a named Go function or \`Handler\` method can be found statically; inline
closures intentionally remain unresolved.

${table(["Method", "Path", "Handler", "Implementation", "Registration"], systemMap.backend.chiRoutes.map((route) => [
  route.method,
  `\`${route.path}\``,
  `\`${route.handler}\``,
  route.implementation ? `\`${route.implementation}\`` : "—",
  `\`${route.registration}\``,
]))}

### Auxiliary HTTP listeners

These routes are current HTTP surfaces but do not live on the main Chi router.

${table(["Server", "Method", "Path", "Source", "Notes"], systemMap.backend.auxiliaryHttpRoutes.map((route) => [
  route.server,
  route.method,
  `\`${route.path}\``,
  `\`${route.source}\``,
  route.notes,
]))}

## Frontend route surface

### Web pages

${table(["Path", "Source"], systemMap.frontend.webPages.map((page) => [`\`${page.path}\``, `\`${page.source}\``]))}

### Web route handlers and proxy

${table(["Method", "Path", "Source"], systemMap.frontend.webRouteHandlers.map((route) => [
  route.method,
  `\`${route.path}\``,
  `\`${route.source}\``,
]))}

- Next proxy: ${systemMap.frontend.webProxy ? `\`${systemMap.frontend.webProxy.source}\` — matcher \`${systemMap.frontend.webProxy.matcher}\`` : "none"}

### Web rewrites

Rewrites are a current data-flow boundary: docs requests go to the docs app,
while API, WebSocket, auth, and upload requests go through the remote-API URL
resolver. Destination values below are symbolic; environment values are never
read by the generator.

${table(["Lane", "Source", "Destination", "Environment", "Evidence"], systemMap.frontend.webRewrites.map((rewrite) => [
  rewrite.lane,
  `\`${rewrite.source}\``,
  `\`${rewrite.destinationTemplate}\``,
  rewrite.environment.map((name) => `\`${name}\``).join(", "),
  `\`${rewrite.sourceFile}\`; \`${rewrite.resolverSource}\``,
]))}

### Desktop route literals

Nested \`RouteObject\` paths are joined to show the effective route. Index-only
routes have no path literal and therefore do not appear in this literal inventory.

${table(["Effective path", "Literal", "Source"], systemMap.frontend.desktopRoutes.map((route) => [
  `\`${route.path}\``,
  `\`${route.literal}\``,
  `\`${route.source}\``,
]))}

### Platform route differences

- Web only: ${systemMap.frontend.routeDifferences.webOnly.map((route) => `\`${route}\``).join(", ") || "none"}
- Desktop only: ${systemMap.frontend.routeDifferences.desktopOnly.map((route) => `\`${route}\``).join(", ") || "none"}

These are inventory differences, not automatically defects: login, workspace
creation, Lark binding, desktop usage, and desktop overlay flows can be
intentionally platform-specific.

## Persistence

### Migrations

${table(["Version", "Name", "Direction", "Tables", "Functions", "Triggers", "Indexes", "Source"], systemMap.persistence.database.migrations.map((migration) => [
  migration.version,
  migration.name,
  migration.direction,
  migration.tablesCreated.map((entry) => entry.name).join(", ") || "—",
  migration.functionsCreated.length,
  migration.triggersCreated.length,
  migration.indexesCreated.length,
  `\`${migration.source}\``,
]))}

### Current tables discovered from up migrations

${systemMap.persistence.database.tables.map((table) => `- \`${table.name}\` — \`${table.source}\``).join("\n")}

### Database functions and implicit trigger flows

${table(["Trigger", "Table", "Function", "Source"], systemMap.persistence.database.triggers.map((trigger) => [
  `\`${trigger.name}\``,
  trigger.table ? `\`${trigger.table}\`` : "—",
  trigger.function ? `\`${trigger.function}\`` : "—",
  `\`${trigger.source}\``,
]))}

- Functions: ${systemMap.persistence.database.functions.map((item) => `\`${item.name}\``).join(", ") || "none"}
- Indexes: ${systemMap.persistence.database.indexes.length} current definitions; full name/table/uniqueness evidence is in the JSON companion.

### sqlc modules

All ${s.sqlcQueries} query names, commands, and stable source anchors are stored in the JSON companion.

${table(["Module", "Queries", "SQL source", "Generated source"], systemMap.persistence.sqlc.modules.map((module) => [
  module.module,
  module.queries.length,
  `\`${module.source}\``,
  `\`${module.generatedSource}\``,
]))}

## WebSocket protocol

- Go (${s.goWebsocketEvents}): ${goEventNames.join(", ")}
- TypeScript (${s.typescriptWebsocketEvents}): ${tsEventNames.join(", ")}
- Go only: ${systemMap.websocket.goOnly.map((event) => `\`${event}\``).join(", ") || "none"}
- TypeScript only: ${systemMap.websocket.typescriptOnly.map((event) => `\`${event}\``).join(", ") || "none"}
- Go declarations with no static production reference: ${systemMap.websocket.goWithoutProductionReference.map((event) => `\`${event}\``).join(", ") || "none"}
- TypeScript declarations with no literal production reference: ${systemMap.websocket.typescriptWithoutLiteralReference.map((event) => `\`${event}\``).join(", ") || "none"}

Each JSON event record carries static production reference sources. A Go event
with no constant or literal reference is a dead-contract candidate. A
TypeScript event without a literal reference may still be handled by the
intentional prefix dispatcher (for example, all \`task:*\` events), so it is a
manual-classification queue rather than proof of dead code. Go-only events
still include daemon-only/backend projection events and possible frontend gaps.

## State ownership

### Zustand sources

${table(["Scope", "Source", "Exported store symbols", "Persisted"], systemMap.stateManagement.zustandStores.map((store) => [
  store.scope,
  `\`${store.source}\``,
  store.exports.map((name) => `\`${name}\``).join(", ") || "(factory/local export)",
  store.persisted ? "yes" : "no",
]))}

### React Query usage

${table(["Scope", "Consumer files"], reactQueryScope.map((entry) => [entry.scope, entry.count]))}

Operation counts: ${Object.entries(systemMap.stateManagement.reactQuery.operationTotals).map(([name, count]) => `\`${name}\` ${count}`).join(", ")}.
The JSON companion lists every consumer file and every discovered exported
\`*Keys\` query-key factory.

## Environment surface

Only variable names and source locations are recorded; values are never read or
written to the generated outputs.

${table(["Variable", "Class", "References", "Representative sources"], systemMap.environment.variables.map((variable) => [
  `\`${variable.name}\``,
  variable.classification,
  variable.references.length,
  variable.references.slice(0, 3).map((reference) => `\`${reference.source}\` (${reference.kind})`).join(", "),
]))}

### Explicit aliases and fallback names

${table(["Canonical", "Aliases", "Disposition", "Reason", "Sources"], systemMap.environment.aliases.map((alias) => [
  `\`${alias.canonical}\``,
  alias.aliases.map((name) => `\`${name}\``).join(", "),
  alias.disposition,
  alias.reason,
  alias.sources.map((item) => `\`${item}\``).join(", "),
]))}

## External I/O

### Automatically detected I/O primitives

${table(["Kind", "Source count", "Representative evidence"], systemMap.externalIo.autoDetected.map((entry) => [
  entry.kind,
  entry.sources.length,
  entry.sources.slice(0, 8).map((item) => `\`${item}\``).join(", "),
]))}

### Manually identified external systems

${table(["System", "Kind", "Environment", "Evidence sources", "Notes"], systemMap.externalIo.externalSystems.map((system) => [
  system.id,
  system.kind,
  system.environment.map((name) => `\`${name}\``).join(", ") || "—",
  system.sources.map((item) => `\`${item}\``).join(", "),
  system.notes,
]))}

## Static-analysis limits and manual review points

${systemMap.manualNotes.map((note) => `- **${note.id}:** ${note.text}`).join("\n")}

When a current runtime surface is dynamic or cannot be recovered safely from
source syntax, add a narrow record to \`${OVERRIDES_FILE}\`. Overrides are part
of the drift check and must include concrete evidence; they are not a place for
future architecture proposals.
`;
}

function writeOrCheck(relativePath, content) {
  if (!checkOnly) {
    fs.mkdirSync(path.dirname(absolute(relativePath)), { recursive: true });
    fs.writeFileSync(absolute(relativePath), content);
    return true;
  }

  if (!fs.existsSync(absolute(relativePath))) {
    console.error(`current-system-map drift: missing ${relativePath}`);
    return false;
  }
  if (read(relativePath) !== content) {
    console.error(`current-system-map drift: ${relativePath} is stale; run pnpm generate:current-system-map`);
    return false;
  }
  return true;
}

const overrides = loadOverrides();
const systemMap = buildMap(overrides);
const jsonOutput = `${JSON.stringify(systemMap, null, 2)}\n`;
const markdownOutput = renderMarkdown(systemMap);
const results = [
  writeOrCheck(OUTPUT_JSON, jsonOutput),
  writeOrCheck(OUTPUT_MARKDOWN, markdownOutput),
];

if (checkOnly) {
  if (results.every(Boolean)) {
    console.log(`current-system-map check passed (${systemMap.summary.chiRoutes} routes, ${systemMap.summary.databaseTables} tables)`);
  } else {
    process.exitCode = 1;
  }
} else {
  console.log(`generated ${OUTPUT_JSON} and ${OUTPUT_MARKDOWN}`);
}
