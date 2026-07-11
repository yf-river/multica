import { readOptionalTextFile } from "./optional-file";

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/**
 * Read an optional JSON object without turning corruption or I/O failures into
 * an empty configuration. Only a genuinely missing file means "first run".
 */
export async function readOptionalJsonObject(
  path: string,
): Promise<Record<string, unknown>> {
  const raw = await readOptionalTextFile(path);
  if (raw === null) return {};

  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch (error) {
    throw new Error(`invalid JSON config ${path}: ${errorMessage(error)}`, {
      cause: error,
    });
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error(`invalid JSON config ${path}: expected an object`);
  }
  return parsed as Record<string, unknown>;
}
