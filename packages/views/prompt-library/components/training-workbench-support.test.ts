import { describe, expect, it } from "vitest";
import type { PromptEvaluationAsset } from "@multica/core/types";
import { summarizeAssetPayload, summarizeLinkedDatasetVersions } from "./training-workbench-support";

const asset = (payload: Record<string, unknown>) => ({ payload }) as PromptEvaluationAsset;

describe("summarizeLinkedDatasetVersions", () => {
  it("summarizes the canonical binding contract", () => {
    expect(summarizeLinkedDatasetVersions(asset({
      linked_dataset_versions: [{
        dataset_version_id: "version-12345678",
        dataset_asset_id: "dataset-1",
        dataset_name: "Regression Set",
        version: 3,
        row_fingerprint: "abcdef1234567890",
      }],
    }))).toBe("绑定用例库版本：Regression Set v3 · 指纹 abcdef1234");
  });
});

describe("summarizeAssetPayload", () => {
  it("summarizes the canonical cases field", () => {
    expect(summarizeAssetPayload(asset({ cases: [{}, {}] }))).toBe("2 个用例");
  });
});
