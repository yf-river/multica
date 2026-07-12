import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const serverRoot = path.join(root, "server");
const generatedRoot = path.join(serverRoot, "pkg/db/generated");

function listGoFiles(dir) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const target = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (target === generatedRoot || entry.name === "vendor") return [];
      return listGoFiles(target);
    }
    return entry.isFile() && entry.name.endsWith(".go") ? [target] : [];
  });
}

function generatedQueryMethods() {
  const methods = new Set();
  for (const file of fs.readdirSync(generatedRoot)) {
    if (!file.endsWith(".sql.go")) continue;
    const source = fs.readFileSync(path.join(generatedRoot, file), "utf8");
    for (const match of source.matchAll(/func \(q \*Queries\) ([A-Za-z0-9_]+)\(/g)) {
      methods.add(match[1]);
    }
  }
  return methods;
}

test("every generated sqlc query has a Go caller", () => {
  const methods = generatedQueryMethods();
  const sources = listGoFiles(serverRoot).map((file) => fs.readFileSync(file, "utf8"));
  const unused = [...methods]
    .filter((name) => !sources.some((source) => new RegExp(`\\b${name}\\b`).test(source)))
    .sort();
  assert.deepEqual(unused, [], `zero-call sqlc queries: ${unused.join(", ")}`);
});
