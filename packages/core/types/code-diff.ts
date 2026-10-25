import { z } from "zod";
import { parseWithFallback } from "../api/schema";

// The result is an API boundary: malformed or older results hide the card.
export const CodeDiffFileSchema = z.object({
  path: z.string(),
  added: z.number().int().nonnegative(),
  deleted: z.number().int().nonnegative(),
  binary: z.boolean(),
  truncated: z.boolean(),
  patch: z.string(),
  // v2 fields. Absent on results captured before roots existed.
  change: z.enum(["added", "modified", "deleted"]).optional(),
  size: z.number().nonnegative().optional(),
  before_size: z.number().nonnegative().optional(),
  mode:z.number().int().nonnegative().optional(),
  before_mode:z.number().int().nonnegative().optional(),
  lines: z.number().int().nonnegative().optional(),
  before_lines: z.number().int().nonnegative().optional(),
  redacted: z.boolean().optional(),
});
export type CodeDiffFile = z.infer<typeof CodeDiffFileSchema>;

export const CodeDiffDirSchema = z.object({
  path: z.string(),
  files: z.number().int().nonnegative(),
  size: z.number().nonnegative(),
  before_files: z.number().int().nonnegative().optional(),
  before_size: z.number().nonnegative().optional(),
  added_children: z.array(z.string()).optional(),
  removed_children: z.array(z.string()).optional(),
  added: z.boolean().optional(),
  removed: z.boolean().optional(),
});
export type CodeDiffDir = z.infer<typeof CodeDiffDirSchema>;

// A root is one independent tree a run touched: the task work directory, a
// worktree created outside it, or a user directory used in place.
export const CodeDiffRootSchema = z.object({
  path: z.string(),
  kind: z.string().default("workdir"),
  label: z.string().default("任务目录"),
  branch: z.string().optional(),
  base: z.string().optional(),
  base_source: z.string().optional(),
  commits: z.number().int().nonnegative().optional(),
  attribution: z.enum(["task", "unassigned", "shared"]).optional(),
  files: z.array(CodeDiffFileSchema).default([]),
  dirs: z.array(CodeDiffDirSchema).optional(),
  truncated: z.boolean().optional(),
  unavailable: z.boolean().optional(),
  reason: z.string().optional(),
  // Paths the capture could not read; the card lists them instead of implying
  // the tree was fully compared.
  skipped: z.array(z.string()).optional(),
});
export type CodeDiffRoot = z.infer<typeof CodeDiffRootSchema>;

export const RunTextChangeSchema = z.object({
  target_kind: z.string(),
  target_id: z.string(),
  target_name: z.string().optional(),
  field: z.string(),
  before: z.string().optional(),
  after: z.string().optional(),
  added: z.number().int().nonnegative(),
  deleted: z.number().int().nonnegative(),
  redacted: z.boolean().optional(),
});
export type RunTextChange = z.infer<typeof RunTextChangeSchema>;

export const CodeDiffSchema = z.object({
  before: z.string(),
  after: z.string(),
  files: z.array(CodeDiffFileSchema),
  truncated: z.boolean(),
  unavailable: z.boolean(),
  reason: z.string().optional(),
  version: z.number().optional(),
  roots: z.array(CodeDiffRootSchema).optional(),
  notes: z.array(z.string()).optional(),
});
export type CodeDiff = z.infer<typeof CodeDiffSchema>;

export function codeDiffFromResult(result: unknown): CodeDiff | null {
  const parsed = parseWithFallback<{ code_diff?: CodeDiff } | null>(
    result,
    z.object({ code_diff: CodeDiffSchema.optional() }),
    null,
    { endpoint: "agent-task.result.code_diff", sensitive: true },
  );
  return parsed?.code_diff ?? null;
}

/**
 * Platform text a run edited (a skill body, an agent prompt). It lives in the
 * database rather than in a file, so it needs its own record next to the code
 * diff — a run that only rewrote a prompt has no changed files at all.
 */
export function textChangesFromResult(result: unknown): RunTextChange[] {
  const parsed = parseWithFallback<{ text_changes?: RunTextChange[] } | null>(
    result,
    z.object({ text_changes: z.array(RunTextChangeSchema).optional() }),
    null,
    { endpoint: "agent-task.result.text_changes", sensitive: true },
  );
  return parsed?.text_changes ?? [];
}

/**
 * v1 results stored a flat file list; wrap them in a single work-directory root
 * so every consumer renders one shape.
 */
export function codeDiffRoots(diff: CodeDiff): CodeDiffRoot[] {
  if (diff.roots && diff.roots.length > 0) return diff.roots;
  return [{
    path: "",
    kind: "workdir",
    label: "任务目录",
    attribution: "task",
    files: diff.files,
    truncated: diff.truncated,
  }];
}
