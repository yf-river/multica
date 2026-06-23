import type { Dispatch, SetStateAction } from "react";
import type {
  CreatePromptEvaluationAssetRequest,
  CreatePromptLibraryItemRequest,
  PromptEvaluationAssetType,
  PromptEvaluationRuntimeReadiness,
  PromptLibraryItem,
  PromptLibraryStatus,
  PromptLibraryVariable,
} from "@multica/core/types";

export const DEFAULT_AGENT_MODEL = "gpt-5.3-codex-spark";

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
    名称: `${prompt.name} 基准用例`,
    变量: values,
    期望包含: Object.values(values).filter(Boolean),
  };
  const basePayload = {
    schema_version: 1,
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
      数据集: [casePayload],
      字段说明: ["名称", "变量", "期望包含"],
      中文语义: "用于提示词调试和实验复现的基准样本。",
    };
  }
  if (assetType === "测试套件") {
    return {
      ...basePayload,
      通过标准: ["变量完整", "渲染内容包含期望关键词", "输出保持中文"],
    };
  }
  if (assetType === "实验") {
    return {
      ...basePayload,
      实验对象: prompt.name,
      对比维度: ["命中率", "缺失变量", "中文一致性"],
      基线输出: rendered,
    };
  }
  return {
    ...basePayload,
    调试输出: rendered,
    运行结果: {
      状态: "已记录",
      运行时间: new Date().toISOString(),
    },
  };
}

export function buildAgentDebugPackageRequest(
  prompt: PromptLibraryItem,
  values: Record<string, string>,
  rendered: string,
  expectedOutput: string,
  readiness: PromptEvaluationRuntimeReadiness,
): CreatePromptEvaluationAssetRequest {
  return {
    prompt_id: prompt.id,
    name: `${prompt.name} 智能体调试包 ${new Date().toLocaleString("zh-CN")} #${Date.now()}`,
    description: `智能体调试场记录：${buildAgentExecutionStatus(readiness)}`,
    asset_type: "实验",
    payload: {
      schema_version: 1,
      语义版本: "multica.training_evaluation.v1",
      cases: [
        {
          名称: "智能体调试场用例",
          变量: values,
          期望包含: splitList(expectedOutput),
        },
      ],
      调试包: {
        提示词: prompt.name,
        变量: values,
        上下文: rendered,
        期望输出: expectedOutput,
        执行方式: buildAgentExecutionStatus(readiness),
      },
      运行环境: {
        目标运行时: "Codex",
        目标模型: readiness.model || DEFAULT_AGENT_MODEL,
        状态: readiness.status,
        说明: readiness.detail,
        修复路径: readiness.fix,
        运行时标识: readiness.runtime?.id ?? null,
      },
      对比维度: ["上下文完整性", "期望输出覆盖", "中文语义一致性"],
    },
    status: "启用",
  };
}

export function variablesToText(variables: PromptLibraryVariable[]): string {
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

function buildAgentExecutionStatus(readiness: PromptEvaluationRuntimeReadiness): string {
  const model = readiness.model || DEFAULT_AGENT_MODEL;
  if (readiness.status === "就绪") {
    return `Codex 运行时已在线，目标模型 ${model}；此记录是实验包快照，点击“创建真实智能体任务”后会入队并采集 trace、token、成本和输出`;
  }
  return `${readiness.label}，目标模型 ${model}；未创建真实智能体任务`;
}
