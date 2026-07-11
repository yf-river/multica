import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { readOptionalTextFile, removeOptionalFile } from "./optional-file";

const temporaryDirectories: string[] = [];

async function temporaryPath(name: string): Promise<string> {
  const directory = await mkdtemp(join(tmpdir(), "multica-optional-file-"));
  temporaryDirectories.push(directory);
  return join(directory, name);
}

afterEach(async () => {
  await Promise.all(
    temporaryDirectories.splice(0).map((directory) =>
      rm(directory, { recursive: true, force: true }),
    ),
  );
});

describe("optional files", () => {
  it("treats only a missing file as empty", async () => {
    const path = await temporaryPath("value");
    await expect(readOptionalTextFile(path)).resolves.toBeNull();

    await writeFile(path, " user-id \n");
    await expect(readOptionalTextFile(path)).resolves.toBe(" user-id \n");
  });

  it("surfaces non-file read errors", async () => {
    const path = await temporaryPath("directory");
    await mkdir(path);

    await expect(readOptionalTextFile(path)).rejects.toThrow(
      `cannot read optional file ${path}`,
    );
  });

  it("ignores a missing remove target but surfaces other remove errors", async () => {
    const missing = await temporaryPath("missing");
    await expect(removeOptionalFile(missing)).resolves.toBeUndefined();

    const directory = await temporaryPath("directory");
    await mkdir(directory);
    await expect(removeOptionalFile(directory)).rejects.toThrow(
      `cannot remove optional file ${directory}`,
    );
  });
});
