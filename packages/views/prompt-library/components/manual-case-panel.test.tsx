import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { PromptEvaluationAsset, PromptEvaluationStructuredCase } from "@multica/core/types";
import {
  ManualCasePanel,
  type ManualCasePanelCopy,
  type ManualCasePanelProps,
} from "./manual-case-panel";

const copy: ManualCasePanelCopy = {
  title: "结构化评测用例",
  counts: ({ manual, trace, draft, approved, active }) =>
    `手工 ${manual} · trace ${trace} · draft ${draft} · approved ${approved} · active ${active}`,
  filter: {
    title: "用例筛选",
    description: "按来源、标签和关键词定位用例",
    matchCount: (visible, total) => `命中 ${visible} / ${total}`,
    sourceLabel: "来源",
    sourceOptions: { all: "全部", manual: "手工", trace: "trace 导入", payload: "资产载荷" },
    tagsLabel: "标签",
    tagFilterAriaLabel: "筛选用例标签",
    allTags: "全部标签",
    keywordPlaceholder: "搜索名称、变量、期望或标签",
    keywordAriaLabel: "筛选用例关键词",
  },
  noFilterResults: "当前筛选没有命中用例",
  caseName: (index) => `用例 ${index}`,
  sourceName: (source) => source,
  statusName: (status) => status,
  summary: ({ variableNames, expectedValues }) =>
    [...variableNames, ...expectedValues].join(" ") || "未填写输入和期望",
  approveDraft: "批准 Draft",
  activateCase: "激活评测",
  editCase: "编辑用例",
  deleteCase: "删除用例",
  editTags: "编辑标签",
  sourceIssue: (id) => `来源 issue ${id}`,
  openRunReview: "查看运行复盘",
  validation: (value) => JSON.stringify(value),
  evidence: (value) => JSON.stringify(value),
  tagsPlaceholder: "编辑用例标签",
  tagsAriaLabel: "编辑用例标签",
  saveTags: "保存标签",
  cancel: "取消",
  editCaseNamePlaceholder: "编辑用例名称",
  editVariablesPlaceholder: "编辑变量",
  editExpectedPlaceholder: "编辑期望包含",
  editTagsPlaceholder: "编辑标签",
  saveCase: "保存用例",
  noCases: "暂无结构化用例",
  caseNamePlaceholder: "手工用例名称",
  variablesPlaceholder: "变量",
  expectedPlaceholder: "期望包含",
  newTagsPlaceholder: "标签",
  addCase: "新增用例",
};

const asset: PromptEvaluationAsset = {
  id: "asset-1",
  workspace_id: "workspace-1",
  prompt_id: null,
  name: "Login cases",
  description: "",
  asset_type: "数据集",
  payload: {},
  status: "启用",
  created_by: null,
  created_at: "2026-07-09T00:00:00Z",
  updated_at: "2026-07-10T00:00:00Z",
  structure_schema: "",
  structured_case_count: 1,
  structured_variable_count: 1,
  structured_assertion_count: 1,
  linked_dataset_count: 0,
  linked_prompt_count: 0,
  evaluation_dimension_count: 0,
  dataset_row_count: 1,
  test_suite_case_count: 0,
  experiment_dimension_count: 0,
};

const draftCase: PromptEvaluationStructuredCase = {
  id: "case-1",
  workspace_id: "workspace-1",
  asset_id: "asset-1",
  prompt_id: null,
  case_index: 0,
  case_name: "Login failure",
  variables: { title: "Login failure" },
  expected_contains: ["root cause"],
  assertions: [],
  input: {},
  expected: {},
  tags: ["auth"],
  status: "draft",
  source: "manual",
  created_by: null,
  created_at: "2026-07-09T00:00:00Z",
  updated_at: "2026-07-10T00:00:00Z",
};

function renderPanel(overrides: Partial<ManualCasePanelProps> = {}) {
  const props: ManualCasePanelProps = {
    asset,
    cases: [draftCase],
    draft: { caseName: "", variablesText: "", expectedText: "", tagsText: "" },
    onDraftChange: vi.fn(),
    onCreateCase: vi.fn(),
    creating: false,
    focusedCaseId: null,
    focusedIssueId: null,
    focusedIssueRunReviewHref: null,
    runReviewHrefForIssue: (id) => `/run-reviews?issue=${id}`,
    onUpdateCase: vi.fn().mockResolvedValue(undefined),
    updatingCaseId: null,
    onDeleteCase: vi.fn(),
    deletingCaseId: null,
    copy,
    ...overrides,
  };
  render(<ManualCasePanel {...props} />);
  return props;
}

describe("manual case panel", () => {
  it("keeps review transitions and semantic source filtering", () => {
    const props = renderPanel();

    fireEvent.click(screen.getByRole("button", { name: copy.approveDraft }));
    expect(props.onUpdateCase).toHaveBeenCalledWith("case-1", { status: "approved" });

    fireEvent.click(screen.getByRole("button", { name: copy.filter.sourceOptions.trace }));
    expect(screen.getByTestId("dataset-case-filter-empty-asset-1")).toHaveTextContent(
      copy.noFilterResults,
    );
  });
});
