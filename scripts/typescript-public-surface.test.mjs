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
    return entry.isFile() && /\.tsx?$/.test(entry.name) ? [target] : [];
  });
}

function parse(file) {
  return ts.createSourceFile(file, fs.readFileSync(file, "utf8"), ts.ScriptTarget.Latest, true);
}

function findPublicMethods(file, className) {
  const source = parse(file);
  const methods = new Set();
  const visit = (node) => {
    if (!ts.isClassDeclaration(node) || node.name?.text !== className) {
      ts.forEachChild(node, visit);
      return;
    }
    for (const member of node.members) {
      if (!ts.isMethodDeclaration(member) || !ts.isIdentifier(member.name)) continue;
      if (member.modifiers?.some((modifier) => modifier.kind === ts.SyntaxKind.PrivateKeyword)) continue;
      methods.add(member.name.text);
    }
  };
  visit(source);
  return methods;
}

function findReferencedMethods(methods, roots) {
  const referenced = new Set();
  const visit = (node) => {
    if (ts.isPropertyAccessExpression(node) && methods.has(node.name.text)) {
      referenced.add(node.name.text);
    }
    ts.forEachChild(node, visit);
  };
  for (const searchRoot of roots) {
    for (const file of listTypeScriptFiles(searchRoot)) visit(parse(file));
  }
  return referenced;
}

function assertNoUnusedPublicMethods(file, className, roots) {
  const methods = findPublicMethods(file, className);
  const referenced = findReferencedMethods(methods, roots);
  const unused = [...methods].filter((name) => !referenced.has(name)).sort();
  assert.deepEqual(unused, [], `unused ${className} methods: ${unused.join(", ")}`);
}

function exportedDeclarationNames(file) {
  const names = [];
  const source = parse(file);
  const isExported = (node) => node.modifiers?.some((modifier) => modifier.kind === ts.SyntaxKind.ExportKeyword);
  for (const statement of source.statements) {
    if (!isExported(statement)) continue;
    if (ts.isVariableStatement(statement)) {
      for (const declaration of statement.declarationList.declarations) {
        if (ts.isIdentifier(declaration.name)) names.push(declaration.name.text);
      }
      continue;
    }
    if (
      (ts.isInterfaceDeclaration(statement) ||
        ts.isTypeAliasDeclaration(statement) ||
        ts.isClassDeclaration(statement) ||
        ts.isFunctionDeclaration(statement)) &&
      statement.name
    ) {
      names.push(statement.name.text);
    }
  }
  return names;
}

test("Core type and schema exports have a cross-file consumer", () => {
  const coreRoot = path.join(root, "packages/core");
  const contractFiles = [
    ...listTypeScriptFiles(path.join(coreRoot, "types")),
    ...listTypeScriptFiles(path.join(coreRoot, "api")).filter(
      (file) => path.basename(file).startsWith("schemas-") && !file.endsWith(".test.ts"),
    ),
  ];
  const candidates = new Map();
  for (const file of contractFiles) {
    for (const name of exportedDeclarationNames(file)) candidates.set(name, file);
  }

  const consumed = new Set();
  const visit = (file, node) => {
    const isReExport = node.parent && ts.isExportSpecifier(node.parent);
    if (ts.isIdentifier(node) && !isReExport) {
      const sourceFile = candidates.get(node.text);
      if (sourceFile && sourceFile !== file) consumed.add(node.text);
    }
    ts.forEachChild(node, (child) => visit(file, child));
  };
  for (const searchRoot of [path.join(root, "packages"), path.join(root, "apps"), e2eRoot]) {
    for (const file of listTypeScriptFiles(searchRoot)) visit(file, parse(file));
  }

  const internalOnly = [...candidates.keys()].filter((name) => !consumed.has(name)).sort();
  assert.deepEqual(internalOnly, [], `Core exports without a cross-file consumer: ${internalOnly.join(", ")}`);
});

test("TestApiClient has no unused public fixture helpers", () => {
  assertNoUnusedPublicMethods(path.join(e2eRoot, "fixtures.ts"), "TestApiClient", [e2eRoot]);
});

test("ApiClient has no unused public endpoint methods", () => {
  assertNoUnusedPublicMethods(path.join(root, "packages/core/api/client.ts"), "ApiClient", [
    path.join(root, "packages"),
    path.join(root, "apps"),
    e2eRoot,
  ]);
});
