import { describe, expect, it } from "vitest";
import type { PromptLibraryItem } from "@multica/core/types";
import { buildAssetPayload } from "./prompt-library-request-builders";

const prompt = {
  id: "prompt-1",
  name: "Regression Prompt",
} as PromptLibraryItem;

describe("buildAssetPayload", () => {
  it("writes the current metric contract without retired aliases", () => {
    const payload = buildAssetPayload("测试套件", prompt, { title: "登录失败" });

    expect(payload.metric_contract).toEqual(expect.arrayContaining(["总用例数", "通过率", "评估结论"]));
    expect(payload).not.toHaveProperty("指标口径");
    expect(payload.cases).toEqual([expect.objectContaining({
      case_name: "Regression Prompt 基准用例",
      variables: { title: "登录失败" },
    })]);
  });
});
