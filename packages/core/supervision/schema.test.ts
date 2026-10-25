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

it("rejects malformed progress and timing without confusing old responses", () => {
  const coverage = supervisionDetailSchema.shape.coverage;
  expect(coverage.parse({ materials: 10, covered: 2, runs: 1 }).partial).toBeUndefined();
  expect(coverage.safeParse({ materials: 10, covered: 2, runs: 1, partial: "3" }).success).toBe(false);
  expect(coverage.parse({ materials: 10, covered: 2, runs: 1, partial: 1, pending: 7, current: { kind: "message", id: "m", offset: 39000, length: 400000 } }).current?.offset).toBe(39000);
  const batch = supervisionDetailSchema.shape.batches.element;
  expect(batch.safeParse({ id: "b", task_id: null, status: "completed", error: "", rules_version: "v6", created_at: "now", completed_at: null, material_count: 1, queue_seconds: "12" }).success).toBe(false);
});

// 1. Collection failures and missing coverage must survive response parsing as explicit states.
it("parses trace preparation summaries without treating unknown counters as zero", () => {
  const schema = supervisionDetailSchema.shape.trace_materials;
  expect(schema.parse(undefined)).toEqual([]);
  const report = { id: "material", run_id: "run", runtime: "codex", status: "not_applicable", material_version: 1, queried_at: null, gaps: ["request-level attribution unavailable"], matched_requests: 0, local_snapshots: 1, preparation_http_requests: 0, preparation_duration_ms: 3, differences: 0 };
  expect(schema.parse([report])[0]?.gaps).toEqual(report.gaps);
  expect(schema.safeParse([{ ...report, matched_requests: "unknown" }]).success).toBe(false);
  expect(schema.safeParse([{ ...report, differences: -1 }]).success).toBe(false);
  expect(schema.parse([{ ...report, status: "future-state" }])[0]?.status).toBe("future-state");
});
