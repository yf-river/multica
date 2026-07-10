import { describe, expect, it } from "vitest";
import type { PromptEvaluationAsset, PromptEvaluationStructuredCase } from "@multica/core/types";
import {
  buildCaseLibraryCreateRequest,
  buildCaseLibraryUpdateRequest,
  buildCaseSummaries,
  caseLibraryExpectedText,
  caseLibraryInputText,
  caseMatchesSource,
  caseSourceKind,
  emptyManualCaseDraft,
  manualCaseToDraft,
} from "./case-model";

const asset = {
  id: "asset-1",
  workspace_id: "ws-1",
  prompt_id: "prompt-1",
  name: "账号系统评估集",
  description: "登录与账号相关回归用例",
  asset_type: "数据集",
  payload: {},
  status: "启用",
  created_by: null,
  created_at: "2026-07-06T00:00:00Z",
  updated_at: "2026-07-06T01:00:00Z",
} as PromptEvaluationAsset;

const item = {
  id: "case-1",
  workspace_id: "ws-1",
  asset_id: "asset-1",
  prompt_id: "prompt-1",
  case_index: 0,
  case_name: "登录失败复盘",
  variables: { input: "用户登录失败" },
  expected_contains: ["失败原因"],
  assertions: [],
  input: { 内容: "用户登录失败，页面提示 unknown error" },
  expected: { 内容: "说明失败原因" },
  tags: ["账号系统"],
  status: "active",
  source: "manual",
  created_by: null,
  created_at: "2026-07-06T00:00:00Z",
  updated_at: "2026-07-06T01:00:00Z",
} as PromptEvaluationStructuredCase;

describe("case model", () => {
  it("builds an empty manual draft", () => {
    expect(emptyManualCaseDraft()).toEqual({
      caseName: "",
      variablesText: "",
      expectedText: "",
      tagsText: "",
    });
  });

  it("classifies case sources and filters", () => {
    expect(caseSourceKind("manual")).toBe("manual");
    expect(caseSourceKind("trace")).toBe("trace");
    expect(caseSourceKind("payload")).toBe("payload");
    expect(caseSourceKind("other")).toBe("imported");
    expect(caseMatchesSource(item, "all")).toBe(true);
    expect(caseMatchesSource(item, "manual")).toBe(true);
    expect(caseMatchesSource(item, "trace")).toBe(false);
  });

  it("reads case library input and expected text", () => {
    expect(caseLibraryInputText(item)).toBe("用户登录失败，页面提示 unknown error");
    expect(caseLibraryExpectedText(item)).toBe("说明失败原因");
  });

  it("maps structured cases into drafts and create/update requests", () => {
    const draft = manualCaseToDraft(item);
    expect(draft.caseName).toBe("登录失败复盘");
    expect(draft.variablesText).toContain("用户登录失败");
    expect(draft.tagsText).toBe("账号系统");

    const create = buildCaseLibraryCreateRequest(asset, {
      caseName: "边界",
      variablesText: "输入内容",
      expectedText: "期望A\n期望B",
      tagsText: "回归, 账号",
    }, 2);
    expect(create.asset_id).toBe("asset-1");
    expect(create.case_index).toBe(2);
    expect(create.case_name).toBe("边界");
    expect(create.expected_contains).toEqual(["期望A", "期望B"]);
    expect(create.input).toMatchObject({ 内容: "输入内容", 来源: "用例库手工维护" });

    const update = buildCaseLibraryUpdateRequest(item, {
      caseName: "登录失败复盘-改",
      variablesText: "新输入",
      expectedText: "新期望",
      tagsText: "回归",
    }, new Date("2026-07-10T00:00:00Z"));
    expect(update.case_name).toBe("登录失败复盘-改");
    expect(update.variables).toEqual({ input: "新输入" });
    expect(update.tags).toEqual(["回归"]);
  });

  it("summarizes cases by asset and source", () => {
    const summary = buildCaseSummaries([
      item,
      { ...item, id: "case-2", source: "trace", asset_id: "asset-1" },
      { ...item, id: "case-3", source: "payload", asset_id: "asset-2" },
    ]);
    expect(summary.get("asset-1")).toEqual({ total: 2, manual: 1, payload: 0, trace: 1 });
    expect(summary.get("asset-2")).toEqual({ total: 1, manual: 0, payload: 1, trace: 0 });
  });
});
