import { describe, expect, it } from "vitest";
import { buildRunEvidenceHref } from "./playground-containers";

describe("buildRunEvidenceHref", () => {
  it("keeps debug run evidence on the evaluation run records route", () => {
    expect(buildRunEvidenceHref("/acme/training/evaluation-runs", "run with space")).toBe(
      "/acme/training/evaluation-runs?run=run%20with%20space",
    );
  });
});
