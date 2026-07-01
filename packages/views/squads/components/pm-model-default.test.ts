import { describe, expect, it } from "vitest";
import type { RuntimeModel } from "@multica/core/types";
import { preferredPMModel } from "./pm-model-default";

function model(input: Partial<RuntimeModel> & { id: string }): RuntimeModel {
  return {
    id: input.id,
    label: input.label ?? input.id,
    provider: input.provider ?? "",
    default: input.default ?? false,
    thinking: input.thinking,
  };
}

describe("preferredPMModel", () => {
  it("prefers the first model whose provider is deepseek", () => {
    expect(
      preferredPMModel([
        model({ id: "claude-sonnet-4.6", provider: "anthropic" }),
        model({ id: "deepseek-v4-pro-ioa", provider: "deepseek" }),
        model({ id: "deepseek-v4-flash-ioa", provider: "deepseek" }),
      ]),
    ).toBe("deepseek-v4-pro-ioa");
  });

  it("falls back to a DeepSeek model matched by id or label", () => {
    expect(
      preferredPMModel([
        model({ id: "glm-5.2-ioa", provider: "zhipu" }),
        model({ id: "vendor-deepseek-v4", label: "DeepSeek V4" }),
      ]),
    ).toBe("vendor-deepseek-v4");
  });

  it("uses the first returned model when no DeepSeek model exists", () => {
    expect(
      preferredPMModel([
        model({ id: "claude-sonnet-4.6", provider: "anthropic" }),
        model({ id: "gpt-5.5", provider: "openai" }),
      ]),
    ).toBe("claude-sonnet-4.6");
  });

  it("returns empty when the runtime has no models", () => {
    expect(preferredPMModel([])).toBe("");
  });
});
