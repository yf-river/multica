import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import ts from "typescript";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const e2eRoot = path.join(root, "e2e");

function listTypeScriptFiles(dir) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const target = path.join(dir, entry.name);
    if (entry.isDirectory()) return listTypeScriptFiles(target);
    return entry.isFile() && entry.name.endsWith(".ts") ? [target] : [];
  });
}

function parse(file) {
  return ts.createSourceFile(file, fs.readFileSync(file, "utf8"), ts.ScriptTarget.Latest, true);
}

test("TestApiClient has no unused public fixture helpers", () => {
  const fixtureFile = path.join(e2eRoot, "fixtures.ts");
  const fixtureSource = parse(fixtureFile);
  const methods = new Set();

  fixtureSource.forEachChild((node) => {
    if (!ts.isClassDeclaration(node) || node.name?.text !== "TestApiClient") return;
    for (const member of node.members) {
      if (!ts.isMethodDeclaration(member) || !ts.isIdentifier(member.name)) continue;
      if (member.modifiers?.some((modifier) => modifier.kind === ts.SyntaxKind.PrivateKeyword)) continue;
      methods.add(member.name.text);
    }
  });

  const referenced = new Set();
  const visit = (node) => {
    if (ts.isPropertyAccessExpression(node) && methods.has(node.name.text)) {
      referenced.add(node.name.text);
    }
    ts.forEachChild(node, visit);
  };
  for (const file of listTypeScriptFiles(e2eRoot)) visit(parse(file));

  const unused = [...methods].filter((name) => !referenced.has(name)).sort();
  assert.deepEqual(unused, [], `unused TestApiClient helpers: ${unused.join(", ")}`);
});
