import { readFile, rm } from "node:fs/promises";

function isMissingFile(error: unknown): boolean {
  return (error as NodeJS.ErrnoException).code === "ENOENT";
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

export async function readOptionalTextFile(path: string): Promise<string | null> {
  try {
    return await readFile(path, "utf-8");
  } catch (error) {
    if (isMissingFile(error)) return null;
    throw new Error(`cannot read optional file ${path}: ${errorMessage(error)}`, {
      cause: error,
    });
  }
}

export async function removeOptionalFile(path: string): Promise<void> {
  try {
    await rm(path);
  } catch (error) {
    if (isMissingFile(error)) return;
    throw new Error(`cannot remove optional file ${path}: ${errorMessage(error)}`, {
      cause: error,
    });
  }
}
