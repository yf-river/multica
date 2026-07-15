import { describe, expect, it } from "vitest";
import {
  promptDraftSyncKey,
  resolvePromptSelection,
} from "./prompt-library-page";
import {
  allPromptTrialVariablesFilled,
  extractPromptVariables,
  summarizePromptTrialVariables,
} from "./prompt-trial-model";
import { draftToRequest, type PromptDraft } from "./prompt-library-request-builders";

describe("prompt library draft request", () => {
  it("keeps only text prompt fields in the request payload", () => {
    const draft: PromptDraft = {
      name: "  登录失败排查  ",
      description: "  用于复盘登录失败问题  ",
      content: "请分析 {{任务标题}}",
    };

    expect(draftToRequest(draft)).toEqual({
      name: "登录失败排查",
      description: "用于复盘登录失败问题",
      prompt_type: "text",
      content: "请分析 {{任务标题}}",
    });
  });
});

describe("prompt library selection stability", () => {
  const items = [{ id: "prompt-1" }, { id: "prompt-2" }, { id: "prompt-3" }];

  it("uses prompt_id before current selection and stored selection", () => {
    expect(resolvePromptSelection(items, "prompt-2", "prompt-3", "prompt-1")).toBe("prompt-3");
  });

  it("keeps the current valid selection during refetches", () => {
    expect(resolvePromptSelection(items, "prompt-2", null, "prompt-1")).toBe("prompt-2");
  });

  it("falls back to stored selection and then the first item", () => {
    expect(resolvePromptSelection(items, "missing", null, "prompt-3")).toBe("prompt-3");
    expect(resolvePromptSelection(items, "missing", null, "also-missing")).toBe("prompt-1");
  });

  it("uses stable draft sync keys across equivalent refetched objects", () => {
    const selected = { id: "prompt-1", version: 2 };
    expect(promptDraftSyncKey(selected, null)).toBe(promptDraftSyncKey({ ...selected }, null));
    expect(promptDraftSyncKey(selected, { id: "version-2" })).toBe(promptDraftSyncKey({ ...selected }, { id: "version-2" }));
  });
});

describe("prompt library template variables", () => {
  it("detects Chinese and latin template variables for trial inputs", () => {
    expect(
      extractPromptVariables("请围绕 {{任务标题}} 分析 {{ 项目背景 }}，负责人：{{owner_name}}。再次确认 {{任务标题}}。"),
    ).toEqual(["任务标题", "项目背景", "owner_name"]);
  });

  it("requires every detected variable before running an agent trial", () => {
    expect(allPromptTrialVariablesFilled(["任务标题", "项目背景"], { 任务标题: "登录失败", 项目背景: "账号系统" })).toBe(true);
    expect(allPromptTrialVariablesFilled(["任务标题", "项目背景"], { 任务标题: "登录失败", 项目背景: " " })).toBe(false);
  });

  it("summarizes trial variables instead of showing a separate user input", () => {
    expect(summarizePromptTrialVariables({ 任务标题: "登录失败", 项目背景: "账号系统", empty: " " })).toBe(
      "任务标题=登录失败，项目背景=账号系统",
    );
    expect(summarizePromptTrialVariables({})).toBeNull();
  });
});
