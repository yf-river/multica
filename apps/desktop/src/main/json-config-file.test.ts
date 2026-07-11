import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { readOptionalJsonObject } from "./json-config-file";

const temporaryDirectories: string[] = [];

async function temporaryConfigPath(): Promise<string> {
  const directory = await mkdtemp(join(tmpdir(), "multica-json-config-"));
  temporaryDirectories.push(directory);
  return join(directory, "config.json");
}

afterEach(async () => {
  await Promise.all(
    temporaryDirectories.splice(0).map((directory) =>
      rm(directory, { recursive: true, force: true }),
    ),
  );
});

describe("readOptionalJsonObject", () => {
  it("returns an empty object only when the file does not exist", async () => {
    const path = await temporaryConfigPath();

    await expect(readOptionalJsonObject(path)).resolves.toEqual({});
  });

  it("returns every field from a valid object", async () => {
    const path = await temporaryConfigPath();
    await writeFile(path, JSON.stringify({ server_url: "https://example.test", token: "secret" }));

    await expect(readOptionalJsonObject(path)).resolves.toEqual({
      server_url: "https://example.test",
      token: "secret",
    });
  });

  it.each([
    ["malformed JSON", "{broken", "invalid JSON config"],
    ["an array", "[]", "expected an object"],
    ["null", "null", "expected an object"],
  ])("rejects %s instead of treating it as first run", async (_name, content, message) => {
    const path = await temporaryConfigPath();
    await writeFile(path, content);

    await expect(readOptionalJsonObject(path)).rejects.toThrow(message);
  });
});
