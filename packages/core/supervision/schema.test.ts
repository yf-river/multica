// @vitest-environment node
import { describe, expect, it } from "vitest";
import { EMPTY_SUPERVISION, supervisionSnapshotSchema, supervisionEvidencePageSchema, supervisionDetailSchema } from "./schema";

describe("supervision response contracts", () => {
  it("keeps empty, incomplete and unknown server fields distinct", () => {
    expect(supervisionSnapshotSchema.safeParse({}).success).toBe(false);
    expect(supervisionSnapshotSchema.parse({ ...EMPTY_SUPERVISION, future: "supported" }).future).toBe("supported");
    expect(supervisionSnapshotSchema.parse(EMPTY_SUPERVISION).summary.covered).toBe(0);
    expect(supervisionDetailSchema.safeParse({ findings: [] }).success).toBe(false);
    const batchSchema = supervisionDetailSchema.shape.batches.element;
    const batch = { id: "b", task_id: "t", status: "completed", error: "", rules_version: "v4", created_at: "now", completed_at: null, material_count: 1 };
    expect(batchSchema.parse(batch).instructions).toBeUndefined();
    expect(batchSchema.parse({ ...batch, instructions: null }).instructions).toBeNull();
    expect(batchSchema.parse({ ...batch, instructions: "原始指令", instructions_sha256: "hash" }).instructions).toBe("原始指令");
    expect(batchSchema.safeParse({ ...batch, instructions: 123 }).success).toBe(false);
  });
  it("preserves missing evidence rather than replacing it with an empty success", () => {
    const page = supervisionEvidencePageSchema.parse({ kind: "message", id: "m1", issue_id: "i1", text: "", offset: 0, end: 0, length: 0, deleted: true });
    expect(page.deleted).toBe(true);
    expect(supervisionEvidencePageSchema.safeParse({ ...page, offset: "0" }).success).toBe(false);
    expect(supervisionEvidencePageSchema.safeParse({ ...page, snapshot: "true" }).success).toBe(false);
    expect(supervisionEvidencePageSchema.parse({ ...page, snapshot: true, batch_id: "batch" }).batch_id).toBe("batch");
  });
});
