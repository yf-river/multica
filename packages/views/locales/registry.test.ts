// @vitest-environment node
import { readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { RESOURCES } from "./index";

const LOCALES_DIR = dirname(fileURLToPath(import.meta.url));

describe("Chinese locale registry", () => {
  it("ships only Simplified Chinese resources", () => {
    expect(Object.keys(RESOURCES)).toEqual(["zh-Hans"]);
  });

  it("registers every Chinese namespace", () => {
    const files = readdirSync(resolve(LOCALES_DIR, "zh-Hans"))
      .filter((name) => name.endsWith(".json"))
      .map((name) => name.replace(/\.json$/, ""))
      .sort();
    expect(Object.keys(RESOURCES["zh-Hans"]).sort()).toEqual(files);
  });
});
