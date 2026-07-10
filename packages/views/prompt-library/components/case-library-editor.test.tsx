import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { PromptEvaluationAsset, PromptEvaluationStructuredCase } from "@multica/core/types";
import {
  CaseLibraryEditorPanel,
  type CaseLibraryEditorCopy,
  type CaseLibraryEditorPanelProps,
} from "./case-library-editor";

const copy: CaseLibraryEditorCopy = {
  title: "评估数据集",
  loading: "正在读取数据集",
  count: (datasets, cases) => `${datasets} 个数据集 · ${cases} 条用例`,
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
  noDatasets: "暂无数据集",
  noDatasetSearchResults: "当前搜索没有命中数据集",
  noDescription: "无描述",
  updatedAt: (value) => `更新于 ${value}`,
  missingTime: "未记录时间",
  emptyTitle: "还没有评估数据集",
  emptyDescription: "先新建数据集",
  saveDataset: "保存数据集",
  createVersion: "创建版本",
  edit: "编辑",
  delete: "删除",
  addCase: "新增用例",
  versionLabel: "版本说明",
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
  sourceLabel: (source) => source,
  inputPrefix: "输入：",
  expectedPrefix: "期望：",
  missingInput: "未填写输入",
  missingExpected: "未填写期望",
  noTags: "无标签",
  noCases: "暂无用例",
  noCaseFilterResults: "当前筛选没有命中用例",
  datasetVersionSummary: (summary) => `v${summary.version ?? "?"}`,
  draft: {
    nameLabel: "名称",
    namePlaceholder: "用例名称",
    tagsLabel: "标签",
    tagsPlaceholder: "账号系统, 回归",
    inputLabel: "输入",
    inputPlaceholder: "待评估内容",
    expectedLabel: "期望",
    expectedPlaceholder: "期望输出",
    cancel: "取消",
  },
  versionHistory: {
    title: "版本历史",
    loading: "正在读取版本",
    count: (count) => `${count} 个版本快照`,
    noSnapshots: "暂无版本快照",
    emptyDescription: "创建版本后可以固定用例",
    unnamedVersion: "未命名版本",
    version: (version) => `v${version}`,
    latest: "最新",
    rowFingerprint: (count, fingerprint) => `${count} 条用例 · 指纹 ${fingerprint}`,
    missingFingerprint: "未生成",
    missingTime: "未记录时间",
  },
};

const asset: PromptEvaluationAsset = {
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

const structuredCase: PromptEvaluationStructuredCase = {
  id: "case-1",
  workspace_id: "workspace-1",
  asset_id: "asset-1",
  prompt_id: null,
  case_index: 0,
  case_name: "登录失败复盘",
  variables: { input: "用户登录失败" },
  expected_contains: ["失败原因"],
  assertions: [],
  input: { 内容: "用户登录失败" },
  expected: { 内容: "说明失败原因" },
  tags: ["账号系统"],
  status: "active",
  source: "manual",
  created_by: null,
  created_at: "2026-07-06T00:00:00Z",
  updated_at: "2026-07-06T01:00:00Z",
};

function renderEditor(overrides: Partial<CaseLibraryEditorPanelProps> = {}) {
  const props: CaseLibraryEditorPanelProps = {
    assets: [asset],
    cases: [structuredCase],
    loading: false,
    saving: false,
    draft: {
      caseName: "登录失败边界",
      variablesText: "请求超时",
      expectedText: "说明失败原因",
      tagsText: "账号系统",
    },
    onDraftChange: vi.fn(),
    onCreateDataset: vi.fn(),
    creatingDataset: false,
    onUpdateDataset: vi.fn(),
    updatingDatasetId: null,
    onDeleteDataset: vi.fn(),
    deletingDatasetId: null,
    onCreateDatasetVersion: vi.fn(),
    creatingDatasetVersionAssetId: null,
    onCreateCase: vi.fn().mockResolvedValue(undefined),
    creating: false,
    focusedCaseId: null,
    onUpdateCase: vi.fn().mockResolvedValue(undefined),
    updatingCaseId: null,
    onDeleteCase: vi.fn(),
    deletingCaseId: null,
    copy,
    ...overrides,
  };
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <CaseLibraryEditorPanel {...props} />
    </QueryClientProvider>,
  );
  return props;
}

describe("case library editor", () => {
  it("preserves case creation and version creation orchestration", () => {
    const props = renderEditor();

    fireEvent.click(screen.getByRole("button", { name: copy.addCase }));
    expect(screen.getByTestId("case-library-draft-editor")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: copy.saveCase }));
    expect(props.onCreateCase).toHaveBeenCalledWith(asset, props.draft);

    fireEvent.click(screen.getByRole("button", { name: copy.createVersion }));
    fireEvent.click(screen.getByRole("button", { name: copy.saveVersion }));
    expect(props.onCreateDatasetVersion).toHaveBeenCalledWith(asset, copy.defaultVersionLabel);
  });
});
