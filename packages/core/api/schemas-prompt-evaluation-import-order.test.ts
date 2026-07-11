import { describe, expect, it, vi } from "vitest";

describe("prompt evaluation schema module boundaries", () => {
  it("initializes case contracts before asset contracts without a TDZ", async () => {
    vi.resetModules();
    const cases = await import("./schemas-prompt-evaluation-cases");
    const assets = await import("./schemas-prompt-evaluation-assets");

    expect(cases.PromptEvaluationDatasetExportResponseSchema).toBeDefined();
    expect(assets.PromptEvaluationAssetSchema).toBeDefined();
  });

  it("initializes asset contracts before case contracts", async () => {
    vi.resetModules();
    const assets = await import("./schemas-prompt-evaluation-assets");
    const cases = await import("./schemas-prompt-evaluation-cases");

    expect(assets.RestorePromptEvaluationDatasetVersionResponseSchema).toBeDefined();
    expect(cases.PromptEvaluationCaseSchema).toBeDefined();
  });
});
