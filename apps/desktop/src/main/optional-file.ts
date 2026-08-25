import { readFile, rm } from "node:fs/promises";
import { errorMessage, isMissingFileError } from "./error-message";

export async function readOptionalTextFile(path: string): Promise<string | null> {
  try {
    return await readFile(path, "utf-8");
  } catch (error) {
    if (isMissingFileError(error)) return null;
    throw new Error(`cannot read optional file ${path}: ${errorMessage(error)}`, {
      cause: error,
    });
  }
}

export async function removeOptionalFile(path: string): Promise<void> {
  try {
    await rm(path);
  } catch (error) {
    if (isMissingFileError(error)) return;
    throw new Error(`cannot remove optional file ${path}: ${errorMessage(error)}`, {
      cause: error,
    });
  }
}
