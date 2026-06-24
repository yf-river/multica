import { describe, expect, it } from "vitest";
import {
  isAcceptanceFixtureRecord,
  isAcceptanceFixtureText,
} from "./acceptance-fixtures";

describe("acceptance fixture detection", () => {
  it("detects explicit dev acceptance fixtures", () => {
    expect(isAcceptanceFixtureText("curl 训练闭环提示词 1782200000")).toBe(true);
    expect(isAcceptanceFixtureText("Codex 验收 Agent - gateway")).toBe(true);
    expect(isAcceptanceFixtureText({ name: "页面验收数据集", payload: ["E2E"] })).toBe(true);
  });

  it("keeps named internal production seeds visible", () => {
    expect(
      isAcceptanceFixtureRecord(
        {
          name: "Multica 编码小队",
          description: "用于端到端开发交付的正式小队",
        },
        ["name", "description"],
      ),
    ).toBe(false);
    expect(
      isAcceptanceFixtureRecord(
        {
          name: "用户中心需求澄清提示词",
          description: "用户中心小队队长使用",
        },
        ["name", "description"],
      ),
    ).toBe(false);
  });

  it("does not hide normal Chinese business records", () => {
    expect(isAcceptanceFixtureText("用户中心登录失败处理小队")).toBe(false);
    expect(isAcceptanceFixtureText("订单服务新增接口评估数据集")).toBe(false);
  });
});
