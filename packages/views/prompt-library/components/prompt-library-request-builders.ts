import type { Dispatch, SetStateAction } from "react";
import type {
  CreatePromptLibraryItemRequest,
  PromptEvaluationAssetType,
  PromptLibraryItem,
  PromptLibraryStatus,
  PromptLibraryVariable,
} from "@multica/core/types";

export type PromptDraft = {
  name: string;
  description: string;
  prompt_type: string;
  content: string;
  variablesText: string;
  tagsText: string;
  status: PromptLibraryStatus;
};

export const emptyDraft = (): PromptDraft => ({
  name: "",
  description: "",
  prompt_type: "通用",
  content: "",
  variablesText: "",
  tagsText: "",
  status: "启用",
});

export function setDraftField<K extends keyof PromptDraft>(
  setDraft: Dispatch<SetStateAction<PromptDraft>>,
  key: K,
  value: PromptDraft[K],
) {
  setDraft((current) => ({ ...current, [key]: value }));
}

export function itemToDraft(item: PromptLibraryItem): PromptDraft {
  return {
    name: item.name,
    description: item.description,
    prompt_type: item.prompt_type,
    content: item.content,
    variablesText: variablesToText(item.variables),
    tagsText: item.tags.join(", "),
    status: item.status,
  };
}

export function requestToDraft(req: CreatePromptLibraryItemRequest): PromptDraft {
  return {
    name: req.name,
    description: req.description ?? "",
    prompt_type: req.prompt_type ?? "通用",
    content: req.content,
    variablesText: variablesToText(req.variables ?? []),
    tagsText: (req.tags ?? []).join(", "),
    status: req.status ?? "启用",
  };
}

export function draftToRequest(draft: PromptDraft): CreatePromptLibraryItemRequest {
  return {
    name: draft.name.trim(),
    description: draft.description.trim(),
    prompt_type: draft.prompt_type.trim() || "通用",
    content: draft.content,
    variables: parseVariables(draft.variablesText),
    tags: splitList(draft.tagsText),
    status: draft.status,
  };
}

export function buildAssetPayload(
  assetType: PromptEvaluationAssetType,
  prompt: PromptLibraryItem,
  values: Record<string, string>,
  rendered: string,
): Record<string, unknown> {
  const casePayload = {
    case_name: `${prompt.name} 基准用例`,
    variables: values,
    expected_contains: Object.values(values).filter(Boolean),
  };
  const basePayload = {
    schema_version: 1,
    schema: "multica.training_evaluation.payload.v1",
    语义版本: "multica.training_evaluation.v1",
    cases: [casePayload],
    指标口径: [
      "总用例数",
      "通过数",
      "失败数",
      "通过率",
      "总耗时",
      "平均耗时",
      "输入 token",
      "输出 token",
      "预估成本",
      "执行Agent",
      "模型",
      "运行时",
      "trace/任务标识",
      "失败原因",
      "评估结论",
    ],
  };
  if (assetType === "数据集") {
    return {
      ...basePayload,
      中文语义: "用于训练与评估的数据集样本。",
    };
  }
  if (assetType === "测试套件") {
    return {
      ...basePayload,
      通过标准: ["变量完整", "渲染内容包含期望关键词", "输出保持中文"],
    };
  }
  return {
    ...basePayload,
    通过标准: ["变量完整", "渲染内容包含期望关键词", "输出保持中文"],
    调试输出: rendered,
  };
}

function variablesToText(variables: PromptLibraryVariable[]): string {
  return variables.map((variable) => `${variable.name}${variable.label ? `=${variable.label}` : ""}`).join(", ");
}

export function valuesToDebugText(variables: PromptLibraryVariable[]): string {
  return variables.map((variable) => `${variable.name}=${variable.default_value ?? ""}`).join("\n");
}

export function parseVariables(value: string): PromptLibraryVariable[] {
  return splitList(value).map((part) => {
    const [name, ...labelParts] = part.split("=");
    const label = labelParts.join("=").trim();
    const variableName = (name ?? "").trim();
    return {
      name: variableName,
      ...(label ? { label } : {}),
    };
  }).filter((variable) => variable.name.length > 0);
}

export function splitList(value: string): string[] {
  return value.split(/[,，]/).map((part) => part.trim()).filter(Boolean);
}

export function parseDebugValues(value: string): Record<string, string> {
  const result: Record<string, string> = {};
  for (const line of value.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const [name, ...valueParts] = trimmed.split("=");
    const key = (name ?? "").trim();
    if (!key) continue;
    result[key] = valueParts.join("=").trim();
  }
  return result;
}
