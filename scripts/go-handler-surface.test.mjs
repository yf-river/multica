import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const serverRoot = path.join(root, "server");
const handlerRoot = path.join(serverRoot, "internal/handler");

function listProductionGoFiles(dir) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const target = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (entry.name === "vendor" || target === path.join(serverRoot, "pkg/db/generated")) return [];
      return listProductionGoFiles(target);
    }
    return entry.isFile() && entry.name.endsWith(".go") && !entry.name.endsWith("_test.go") ? [target] : [];
  });
}

test("every Handler method has a production reference", () => {
  const files = listProductionGoFiles(serverRoot);
  const definitions = new Set();
  const definitionPattern = /func \(h \*Handler\) ([A-Za-z0-9_]+)\(/g;
  for (const file of files.filter((candidate) => candidate.startsWith(handlerRoot + path.sep))) {
    const source = fs.readFileSync(file, "utf8");
    for (const match of source.matchAll(definitionPattern)) definitions.add(match[1]);
  }

  const references = files
    .map((file) => fs.readFileSync(file, "utf8").replaceAll(definitionPattern, "func (h *Handler) ("))
    .join("\n");
  const unused = [...definitions]
    .filter((name) => !new RegExp(`\\b${name}\\b`).test(references))
    .sort();
  assert.deepEqual(unused, [], `Handler methods without a production reference: ${unused.join(", ")}`);
});
