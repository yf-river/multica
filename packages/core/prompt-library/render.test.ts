import { describe, expect, it } from "vitest";
import { renderPromptTemplate } from "./render";

describe("renderPromptTemplate", () => {
  it("renders Chinese prompt variables", () => {
    const result = renderPromptTemplate({
      content: "请分析 {{issue_title}}，仓库是 {{repo}}。",
      values: {
        issue_title: "登录失败",
        repo: "user-center",
      },
    });

    expect(result.rendered).toBe("请分析 登录失败，仓库是 user-center。");
    expect(result.usedVariables).toEqual(["issue_title", "repo"]);
    expect(result.missingVariables).toEqual([]);
  });

  it("keeps missing placeholders and reports them", () => {
    const result = renderPromptTemplate({
      content: "目标：{{goal}}；验收：{{acceptance}}",
      variables: [{ name: "acceptance", default_value: "测试通过" }],
      values: {},
    });

    expect(result.rendered).toBe("目标：{{goal}}；验收：测试通过");
    expect(result.missingVariables).toEqual(["goal"]);
  });
});
