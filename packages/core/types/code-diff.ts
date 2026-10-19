import { z } from "zod";
import { parseWithFallback } from "../api/schema";

// The result is an API boundary: malformed or older results hide the card.
export const CodeDiffSchema = z.object({
  before: z.string(),
  after: z.string(),
  files: z.array(z.object({
    path: z.string(),
    added: z.number().int().nonnegative(),
    deleted: z.number().int().nonnegative(),
    binary: z.boolean(),
    truncated: z.boolean(),
    patch: z.string(),
  })),
  truncated: z.boolean(),
  unavailable: z.boolean(),
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
