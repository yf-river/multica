import React from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import {
  promptDraftSyncKey,
  resolvePromptSelection,
} from "./prompt-library-page";
import {
  CaseLibraryEditorPanel,
  type CaseLibraryEditorCopy,
} from "./case-library-editor";
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


const caseLibraryCopy: CaseLibraryEditorCopy = {
  title: "评估数据集",
  loading: "正在读取数据集",
  count: (datasetCount, caseCount) => `${datasetCount} 个数据集 · ${caseCount} 条用例`,
  createDataset: "新建",
  searchPlaceholder: "搜索数据集、用例、标签",
  searchAriaLabel: "搜索数据集和用例",
  datasetNamePlaceholder: "数据集名称",
  datasetDescriptionPlaceholder: "描述",
  cancel: "取消",
  save: "保存",
  missingDatasetNameError: "请输入数据集名称",
  missingCaseNameError: "请输入用例名称",
  missingCaseInputError: "请输入用例输入",
  noDatasets: "暂无数据集，先新建一个评估数据集。",
  noDatasetSearchResults: "当前搜索没有命中数据集。",
  noDescription: "无描述",
  updatedAt: (value) => `更新于 ${value}`,
  missingTime: "未记录时间",
  emptyTitle: "先选择或新建数据集",
  emptyDescription: "数据集用于集中维护评估用例，并锁定可追溯版本。",
  saveDataset: "保存数据集",
  createVersion: "创建版本",
  edit: "编辑",
  delete: "删除",
  addCase: "新增用例",
  versionLabel: "版本标签",
  versionPlaceholder: "例如：补充登录失败边界用例",
  defaultVersionLabel: "手动快照",
  saveVersion: "保存版本",
  tagFilterAriaLabel: "筛选用例标签",
  allTags: "全部标签",
  matchCount: (visible, total) => `命中 ${visible} / ${total}`,
  newCaseTitle: "新增用例",
  editCaseTitle: "编辑用例",
  saveCase: "保存用例",
  caseCount: (count) => `${count} 条用例`,
  caseName: (index) => `用例 ${index}`,
  sourceLabel: (source) =>
    ({ manual: "手工", trace: "trace导入", payload: "资产载荷", imported: "导入" } as const)[source],
  inputPrefix: "输入：",
  expectedPrefix: "期望：",
  missingInput: "未填写输入",
  missingExpected: "未填写期望",
  noTags: "无标签",
  noCases: "暂无用例，先新增一条评估用例。",
  noCaseFilterResults: "当前筛选没有命中用例。",
  datasetVersionSummary: (summary) =>
    `用例库版本 v${summary.version ?? "?"} · ${summary.rowCount ?? "0"} 行 · 指纹 ${summary.fingerprint ?? "未生成"}`,
  draft: {
    nameLabel: "名称",
    namePlaceholder: "用例名称",
    tagsLabel: "标签",
    tagsPlaceholder: "标签，逗号分隔",
    inputLabel: "输入",
    inputPlaceholder: "评估输入内容",
    expectedLabel: "期望",
    expectedPlaceholder: "期望输出或验收点，可换行",
    cancel: "取消",
  },
  versionHistory: {
    title: "版本历史",
    loading: "正在读取版本",
    count: (count) => `${count} 个版本快照`,
    noSnapshots: "暂无版本快照",
    emptyDescription: "创建版本后，后续评估和调试可以固定使用这批用例。",
    unnamedVersion: "未命名版本",
    version: (version) => `v${version}`,
    latest: "最新",
    rowFingerprint: (rowCount, fingerprint) => `${rowCount} 条用例 · 指纹 ${fingerprint}`,
    missingFingerprint: "未生成",
    missingTime: "未记录时间",
  },
};

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
      onCreateDataset: vi.fn().mockResolvedValue(undefined),
      creatingDataset: false,
      onUpdateDataset: vi.fn().mockResolvedValue(undefined),
      updatingDatasetId: null,
      onDeleteDataset: vi.fn(),
      deletingDatasetId: null,
      onCreateDatasetVersion: vi.fn().mockResolvedValue(undefined),
      creatingDatasetVersionAssetId: null,
      onCreateCase: vi.fn(),
      creating: false,
      focusedCaseId: null,
      onUpdateCase: vi.fn(),
      updatingCaseId: null,
      onDeleteCase: vi.fn(),
      deletingCaseId: null,
      copy: caseLibraryCopy,
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

  it("creates a case only inside the selected dataset", async () => {
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
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "保存用例" }));
    });

    expect(onCreateCase).toHaveBeenCalledWith(
      baseAsset,
      expect.objectContaining({ caseName: "登录失败边界" }),
    );
  });

  it("opens dataset editing controls", async () => {
    const onUpdateDataset = vi.fn().mockResolvedValue(undefined);
    renderCaseLibrary({ onUpdateDataset });

    fireEvent.click(screen.getByTestId("edit-case-library-dataset-asset-1"));
    fireEvent.change(screen.getByDisplayValue("账号系统评估集"), { target: { value: "账号回归评估集" } });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "保存数据集" }));
    });

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

  it("creates dataset versions with a version note", async () => {
    const onCreateDatasetVersion = vi.fn().mockResolvedValue(undefined);
    renderCaseLibrary({ onCreateDatasetVersion });

    fireEvent.click(screen.getByRole("button", { name: "创建版本" }));
    fireEvent.change(screen.getByPlaceholderText("例如：补充登录失败边界用例"), { target: { value: "补充登录失败边界用例" } });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "保存版本" }));
    });

    expect(onCreateDatasetVersion).toHaveBeenCalledWith(baseAsset, "补充登录失败边界用例");
  });
});
