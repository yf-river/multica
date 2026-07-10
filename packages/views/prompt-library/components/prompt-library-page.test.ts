import React from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import {
  CaseLibraryEditorPanel,
  promptDraftSyncKey,
  resolvePromptSelection,
} from "./prompt-library-page";
import {
  allPromptTrialVariablesFilled,
  extractPromptVariables,
  summarizePromptTrialVariables,
} from "./prompt-trial-model";
import { draftToRequest, type PromptDraft } from "./prompt-library-request-builders";
import type { PromptEvaluationAsset, PromptEvaluationStructuredCase } from "@multica/core/types";

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

describe("case library editor", () => {
  const baseAsset: PromptEvaluationAsset = {
    id: "asset-1",
    workspace_id: "",
    prompt_id: null,
    name: "账号系统评估集",
    description: "登录与账号相关回归用例",
    asset_type: "数据集",
    payload: {},
    status: "启用",
    created_by: null,
    created_at: "2026-07-06T00:00:00Z",
    updated_at: "2026-07-06T01:00:00Z",
    structure_schema: "",
    structured_case_count: 1,
    structured_variable_count: 0,
    structured_assertion_count: 0,
    linked_dataset_count: 0,
    linked_prompt_count: 0,
    evaluation_dimension_count: 0,
    dataset_row_count: 1,
    test_suite_case_count: 0,
    experiment_dimension_count: 0,
  };
  const baseCase: PromptEvaluationStructuredCase = {
    id: "case-1",
    workspace_id: "ws-1",
    asset_id: "asset-1",
    prompt_id: null,
    case_index: 0,
    case_name: "登录失败复盘",
    variables: { 任务标题: "登录失败" },
    expected_contains: ["失败原因", "下一步建议"],
    assertions: [],
    input: { 内容: "用户登录失败，页面提示 unknown error" },
    expected: { 内容: "说明失败原因\n给出下一步建议" },
    tags: ["账号系统", "回归"],
    status: "active",
    source: "manual",
    created_by: null,
    created_at: "2026-07-06T00:00:00Z",
    updated_at: "2026-07-06T01:00:00Z",
  };

  function renderCaseLibrary(overrides: Partial<React.ComponentProps<typeof CaseLibraryEditorPanel>> = {}) {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const defaultProps: React.ComponentProps<typeof CaseLibraryEditorPanel> = {
      assets: [baseAsset],
      cases: [baseCase],
      loading: false,
      saving: false,
      draft: { caseName: "", variablesText: "", expectedText: "", tagsText: "" },
      onDraftChange: vi.fn(),
      onCreateDataset: vi.fn(),
      creatingDataset: false,
      onUpdateDataset: vi.fn(),
      updatingDatasetId: null,
      onDeleteDataset: vi.fn(),
      deletingDatasetId: null,
      onCreateDatasetVersion: vi.fn(),
      creatingDatasetVersionAssetId: null,
      onCreateCase: vi.fn(),
      creating: false,
      focusedCaseId: null,
      onUpdateCase: vi.fn(),
      updatingCaseId: null,
      onDeleteCase: vi.fn(),
      deletingCaseId: null,
      ...overrides,
    };
    return render(
      React.createElement(
        QueryClientProvider,
        { client: queryClient },
        React.createElement(CaseLibraryEditorPanel, defaultProps),
      ),
    );
  }

  it("renders a single case table without asset governance actions", () => {
    renderCaseLibrary();

    expect(screen.getByTestId("case-library-editor")).toBeInTheDocument();
    expect(screen.getByText("评估数据集")).toBeInTheDocument();
    expect(screen.getAllByText("账号系统评估集").length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText("登录失败复盘")).toBeInTheDocument();
    expect(screen.getByText("用户登录失败，页面提示 unknown error")).toBeInTheDocument();
    expect(screen.getByText(/说明失败原因/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "新增用例" })).toBeInTheDocument();
    expect(screen.queryByText("从 trace 导入用例")).toBeNull();
    expect(screen.queryByText("资产载荷")).toBeNull();
    expect(screen.queryByText("未记录变量和期望")).toBeNull();
    expect(screen.queryByText("版本治理")).toBeNull();
    expect(screen.queryByText("批准 Draft")).toBeNull();
    expect(screen.queryByText("激活评测")).toBeNull();
    expect(screen.queryByText("归档运行证据")).toBeNull();
  });

  it("opens the minimal manual case editor", () => {
    renderCaseLibrary();

    fireEvent.click(screen.getByRole("button", { name: "新增用例" }));

    expect(screen.getByTestId("case-library-draft-editor")).toBeInTheDocument();
    expect(screen.getByText("名称")).toBeInTheDocument();
    expect(screen.getByText("输入")).toBeInTheDocument();
    expect(screen.getByText("期望")).toBeInTheDocument();
    expect(screen.getByText("标签")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "保存用例" })).toBeDisabled();
  });

  it("creates a case only inside the selected dataset", () => {
    const onCreateCase = vi.fn().mockResolvedValue(undefined);
    renderCaseLibrary({
      draft: {
        caseName: "登录失败边界",
        variablesText: "任务标题=登录失败",
        expectedText: "说明失败原因",
        tagsText: "账号系统",
      },
      onCreateCase,
    });

    fireEvent.click(screen.getByRole("button", { name: "新增用例" }));
    fireEvent.click(screen.getByRole("button", { name: "保存用例" }));

    expect(onCreateCase).toHaveBeenCalledWith(
      baseAsset,
      expect.objectContaining({ caseName: "登录失败边界" }),
    );
  });

  it("opens dataset editing controls", () => {
    const onUpdateDataset = vi.fn();
    renderCaseLibrary({ onUpdateDataset });

    fireEvent.click(screen.getByTestId("edit-case-library-dataset-asset-1"));
    fireEvent.change(screen.getByDisplayValue("账号系统评估集"), { target: { value: "账号回归评估集" } });
    fireEvent.click(screen.getByRole("button", { name: "保存数据集" }));

    expect(onUpdateDataset).toHaveBeenCalledWith(baseAsset, expect.objectContaining({
      name: "账号回归评估集",
      description: "登录与账号相关回归用例",
      asset_type: "数据集",
    }));
  });

  it("passes the selected dataset to delete", () => {
    const onDeleteDataset = vi.fn();
    renderCaseLibrary({ onDeleteDataset });

    fireEvent.click(screen.getByTestId("delete-case-library-dataset-asset-1"));

    expect(onDeleteDataset).toHaveBeenCalledWith(baseAsset);
  });

  it("creates dataset versions with a version note", () => {
    const onCreateDatasetVersion = vi.fn();
    renderCaseLibrary({ onCreateDatasetVersion });

    fireEvent.click(screen.getByRole("button", { name: "创建版本" }));
    fireEvent.change(screen.getByPlaceholderText("例如：补充登录失败边界用例"), { target: { value: "补充登录失败边界用例" } });
    fireEvent.click(screen.getByRole("button", { name: "保存版本" }));

    expect(onCreateDatasetVersion).toHaveBeenCalledWith(baseAsset, "补充登录失败边界用例");
  });
});
