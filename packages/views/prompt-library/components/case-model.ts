import { isSkillScenarioPayload } from "@multica/core/training";
import type {
  CreatePromptEvaluationCaseRequest,
  PromptEvaluationAsset,
  PromptEvaluationStructuredCase,
  UpdatePromptEvaluationCaseRequest,
} from "@multica/core/types";
import { parseDebugValues, splitList } from "./prompt-library-request-builders";
import { asRecord, stringFromUnknown } from "./record-utils";

export type ManualCaseDraft = {
  caseName: string;
  variablesText: string;
  expectedText: string;
  tagsText: string;
};

export type CaseSourceKind = "manual" | "trace" | "payload" | "imported";
export type CaseSourceFilter = "all" | Exclude<CaseSourceKind, "imported">;

export type CaseSummary = {
  total: number;
  manual: number;
  payload: number;
  trace: number;
};

export type CaseContentSummary = {
  variableNames: string[];
  expectedValues: string[];
};

export type CaseValidation =
  | { kind: "validation"; value: string }
  | { kind: "expected-behavior"; value: string }
  | { kind: "contains"; values: string[] };

export type CaseEvidenceFacts = {
  stageCount: number;
  childLaneCount: number;
  timelineNodeCount: number;
};

export type DatasetVersionSummary = {
  version: string | null;
  rowCount: string | null;
  fingerprint: string | null;
  createdAt: string | null;
};

export function emptyManualCaseDraft(): ManualCaseDraft {
  return {
    caseName: "",
    variablesText: "",
    expectedText: "",
    tagsText: "",
  };
}

export function caseSourceKind(source: string): CaseSourceKind {
  if (source === "manual") return "manual";
  if (source === "trace") return "trace";
  if (source === "payload") return "payload";
  return "imported";
}

export function caseMatchesSource(item: PromptEvaluationStructuredCase, filter: CaseSourceFilter): boolean {
  return filter === "all" || caseSourceKind(item.source) === filter;
}

export function buildManualCaseRequest(
  asset: PromptEvaluationAsset,
  draft: ManualCaseDraft,
  existingCount: number,
): CreatePromptEvaluationCaseRequest {
  const variables = parseDebugValues(draft.variablesText);
  const expectedContains = splitList(draft.expectedText);
  const skillScenario = isSkillScenarioPayload(asset.payload) ? asset.payload : null;
  return {
    asset_id: asset.id,
    prompt_id: asset.prompt_id,
    case_index: existingCount,
    case_name: draft.caseName.trim(),
    variables,
    expected_contains: expectedContains,
    input: {
      变量: variables,
      来源: "训练与评估手工用例",
      ...(skillScenario
        ? {
            skill_scenario: {
              target: skillScenario.target,
              scenario: skillScenario.scenario,
              rubric: skillScenario.rubric,
            },
          }
        : {}),
    },
    expected: {
      期望包含: expectedContains,
      ...(skillScenario
        ? {
            skill_scenario: {
              rubric_keys: skillScenario.rubric.map((item) => item.key),
              target_skill_path: skillScenario.target.skill_path,
            },
          }
        : {}),
    },
    tags: splitList(draft.tagsText),
    status: "active",
  };
}

export function buildCaseLibraryCreateRequest(
  asset: PromptEvaluationAsset,
  draft: ManualCaseDraft,
  existingCount: number,
): CreatePromptEvaluationCaseRequest {
  const inputText = draft.variablesText.trim();
  const expectedText = draft.expectedText.trim();
  const expectedContains = splitExpectationLines(expectedText);
  return {
    asset_id: asset.id,
    prompt_id: asset.prompt_id,
    case_index: existingCount,
    case_name: draft.caseName.trim(),
    variables: inputText ? { input: inputText } : {},
    expected_contains: expectedContains,
    input: {
      内容: inputText,
      来源: "用例库手工维护",
    },
    expected: {
      内容: expectedText,
      期望包含: expectedContains,
    },
    tags: splitList(draft.tagsText),
    status: "active",
  };
}

export function buildCaseLibraryUpdateRequest(
  item: PromptEvaluationStructuredCase,
  draft: ManualCaseDraft,
  now: Date = new Date(),
): UpdatePromptEvaluationCaseRequest {
  const inputText = draft.variablesText.trim();
  const expectedText = draft.expectedText.trim();
  const expectedContains = splitExpectationLines(expectedText);
  return {
    asset_id: item.asset_id,
    prompt_id: item.prompt_id,
    case_index: item.case_index,
    case_name: draft.caseName.trim(),
    variables: inputText ? { input: inputText } : {},
    expected_contains: expectedContains,
    input: {
      内容: inputText,
      来源: "用例库手工维护",
      最近人工维护: now.toISOString(),
    },
    expected: {
      内容: expectedText,
      期望包含: expectedContains,
    },
    tags: splitList(draft.tagsText),
    status: item.status,
  };
}

export function buildManualCaseUpdateRequest(
  asset: PromptEvaluationAsset,
  item: PromptEvaluationStructuredCase,
  draft: ManualCaseDraft,
  now: Date = new Date(),
): UpdatePromptEvaluationCaseRequest {
  const variables = parseDebugValues(draft.variablesText);
  const expectedContains = splitList(draft.expectedText);
  const skillScenario = isSkillScenarioPayload(asset.payload) ? asset.payload : null;
  return {
    asset_id: asset.id,
    prompt_id: asset.prompt_id,
    case_index: item.case_index,
    case_name: draft.caseName.trim(),
    variables,
    expected_contains: expectedContains,
    input: {
      变量: variables,
      来源: "训练与评估手工用例",
      最近人工维护: now.toISOString(),
      ...(skillScenario
        ? {
            skill_scenario: {
              target: skillScenario.target,
              scenario: skillScenario.scenario,
              rubric: skillScenario.rubric,
            },
          }
        : {}),
    },
    expected: {
      期望包含: expectedContains,
      ...(skillScenario
        ? {
            skill_scenario: {
              rubric_keys: skillScenario.rubric.map((entry) => entry.key),
              target_skill_path: skillScenario.target.skill_path,
            },
          }
        : {}),
    },
    tags: splitList(draft.tagsText),
    status: item.status,
  };
}

export function buildCaseTagUpdateRequest(
  asset: PromptEvaluationAsset,
  item: PromptEvaluationStructuredCase,
  tagsText: string,
): UpdatePromptEvaluationCaseRequest {
  return {
    asset_id: asset.id,
    prompt_id: item.prompt_id ?? asset.prompt_id,
    case_index: item.case_index,
    case_name: item.case_name,
    tags: splitList(tagsText),
    status: item.status,
  };
}

export function manualCaseToDraft(item: PromptEvaluationStructuredCase): ManualCaseDraft {
  return {
    caseName: item.case_name,
    variablesText: caseLibraryInputText(item),
    expectedText: caseLibraryExpectedText(item),
    tagsText: item.tags.map(String).join(", "),
  };
}

export function buildCaseSummaries(cases: PromptEvaluationStructuredCase[]): Map<string, CaseSummary> {
  const counts = new Map<string, CaseSummary>();
  for (const item of cases) {
    const current = counts.get(item.asset_id) ?? { total: 0, manual: 0, payload: 0, trace: 0 };
    current.total += 1;
    const source = caseSourceKind(item.source);
    if (source === "manual") current.manual += 1;
    if (source === "trace") current.trace += 1;
    if (source === "payload") current.payload += 1;
    counts.set(item.asset_id, current);
  }
  return counts;
}

export function buildCasesByAsset(
  cases: PromptEvaluationStructuredCase[],
): Map<string, PromptEvaluationStructuredCase[]> {
  const result = new Map<string, PromptEvaluationStructuredCase[]>();
  for (const item of cases) {
    const bucket = result.get(item.asset_id) ?? [];
    bucket.push(item);
    result.set(item.asset_id, bucket);
  }
  for (const bucket of result.values()) {
    bucket.sort((a, b) => a.case_index - b.case_index || a.case_name.localeCompare(b.case_name));
  }
  return result;
}

export function uniqueSortedStrings(values: string[]): string[] {
  return Array.from(new Set(values.map((value) => value.trim()).filter(Boolean))).sort((a, b) =>
    a.localeCompare(b),
  );
}

export function buildCaseSearchText(item: PromptEvaluationStructuredCase, sourceLabel: string): string {
  return [
    item.case_name,
    item.status,
    item.source,
    sourceLabel,
    ...item.tags.map(String),
    ...caseContentSummary(item).variableNames,
    ...caseContentSummary(item).expectedValues,
    summarizeJsonValue(item.variables),
    summarizeJsonValue(item.expected_contains),
    summarizeJsonValue(item.input),
    summarizeJsonValue(item.expected),
  ]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
}

export function caseContentSummary(item: PromptEvaluationStructuredCase): CaseContentSummary {
  return {
    variableNames: Object.keys(item.variables ?? {}),
    expectedValues: item.expected_contains.map(stringFromUnknown).filter(Boolean),
  };
}

export function caseLibraryInputText(item: PromptEvaluationStructuredCase): string {
  const inputRecord = item.input ?? {};
  const variableRecord = item.variables ?? {};
  return (
    stringFromRecord(inputRecord, "内容") ||
    stringFromRecord(inputRecord, "input") ||
    stringFromRecord(variableRecord, "input") ||
    Object.entries(variableRecord)
      .map(([key, value]) => `${key}=${String(value)}`)
      .join("\n")
  ).trim();
}

export function caseLibraryExpectedText(item: PromptEvaluationStructuredCase): string {
  const expectedRecord = item.expected ?? {};
  return (
    stringFromRecord(expectedRecord, "内容") ||
    stringFromRecord(expectedRecord, "expected") ||
    item.expected_contains.map(String).join("\n")
  ).trim();
}

export function caseIssueId(item: PromptEvaluationStructuredCase): string | null {
  const variableIssueId = stringFromUnknown(item.variables.issue_id);
  if (variableIssueId) return variableIssueId;
  const issue = asRecord(item.input.issue);
  const inputIssueId = stringFromUnknown(issue.id);
  if (inputIssueId) return inputIssueId;
  const issueTag = item.tags.map(stringFromUnknown).find((tag) => tag.startsWith("issue:"));
  return issueTag?.slice("issue:".length) || null;
}

export function caseValidation(item: PromptEvaluationStructuredCase): CaseValidation | null {
  const validation = stringFromUnknown(item.expected.validation);
  if (validation) return { kind: "validation", value: validation };
  const expectedBehavior = stringFromUnknown(item.expected.expected_behavior);
  if (expectedBehavior) return { kind: "expected-behavior", value: expectedBehavior };
  const values = item.expected_contains.map(stringFromUnknown).filter(Boolean).slice(0, 5);
  return values.length > 0 ? { kind: "contains", values } : null;
}

export function caseEvidenceFacts(item: PromptEvaluationStructuredCase): CaseEvidenceFacts | null {
  const runReview = asRecord(item.input.run_review);
  const facts: CaseEvidenceFacts = {
    stageCount: Array.isArray(runReview.stage_facts) ? runReview.stage_facts.length : 0,
    childLaneCount: Array.isArray(runReview.child_lanes) ? runReview.child_lanes.length : 0,
    timelineNodeCount: finiteNonNegativeNumber(runReview.timeline_node_count),
  };
  return facts.stageCount > 0 || facts.childLaneCount > 0 || facts.timelineNodeCount > 0 ? facts : null;
}

export function datasetVersionSummary(asset: PromptEvaluationAsset): DatasetVersionSummary | null {
  const raw = asset.payload?.["最近数据集版本"];
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  const record = raw as Record<string, unknown>;
  const summary: DatasetVersionSummary = {
    version: nullableString(record.version),
    rowCount: nullableString(record.row_count),
    fingerprint: nullableString(record.row_fingerprint),
    createdAt: nullableString(record.created_at),
  };
  return summary.version || summary.rowCount || summary.fingerprint ? summary : null;
}

function splitExpectationLines(value: string): string[] {
  return value
    .split(/[\n\r,，]/)
    .map((part) => part.trim())
    .filter(Boolean);
}

function summarizeJsonValue(value: unknown): string {
  if (value === null || value === undefined) return "";
  if (typeof value === "object" && !Array.isArray(value) && Object.keys(value as object).length === 0) {
    return "";
  }
  return JSON.stringify(value) ?? "";
}

function stringFromRecord(record: Record<string, unknown>, key: string): string {
  const value = record[key];
  if (typeof value === "string") return value;
  if (typeof value === "number" && Number.isFinite(value)) return String(value);
  return "";
}

function nullableString(value: unknown): string | null {
  const text = stringFromUnknown(value).trim();
  return text || null;
}

function finiteNonNegativeNumber(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : 0;
}
