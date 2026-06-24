"use client";

import { useMemo, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { renderPromptTemplate } from "@multica/core/prompt-library";
import type {
  Agent,
  CreatePromptEvaluationAssetRequest,
  PromptEvaluationAssetType,
  PromptEvaluationRuntimeReadiness,
  PromptLibraryItem,
} from "@multica/core/types";
import {
  buildAgentDebugPackageRequest,
  buildAssetPayload,
  parseDebugValues,
  parseVariables,
  type PromptDraft,
} from "./prompt-library-request-builders";

type UsePromptPlaygroundActionsOptions = {
  draft: PromptDraft;
  selected: PromptLibraryItem | null;
  items: PromptLibraryItem[];
  selectedPromptStorageKey: string | null;
  onAssetsChanged: () => void;
  onCasesChanged: () => void;
  onExperimentDimensionsChanged: () => void;
  onRunsChanged: () => void;
  onSummaryChanged: () => void;
};

type UseAgentPlaygroundActionsOptions = {
  draft: PromptDraft;
  selected: PromptLibraryItem | null;
  agentRuntimeReadiness: PromptEvaluationRuntimeReadiness;
  selectedExecutionAgent: Agent | null;
  onAssetsChanged: () => void;
  onCasesChanged: () => void;
  onExperimentDimensionsChanged: () => void;
  onRunsChanged: () => void;
  onSummaryChanged: () => void;
};

function usePromptDebugState(draft: PromptDraft) {
  const [debugValuesText, setDebugValuesText] = useState("");
  const debugResult = useMemo(
    () => renderPromptTemplate({
      content: draft.content,
      variables: parseVariables(draft.variablesText),
      values: parseDebugValues(debugValuesText),
    }),
    [debugValuesText, draft.content, draft.variablesText],
  );

  return { debugValuesText, setDebugValuesText, debugResult };
}

export function usePromptPlaygroundActions({
  draft,
  selected,
  items,
  selectedPromptStorageKey,
  onAssetsChanged,
  onCasesChanged,
  onExperimentDimensionsChanged,
  onRunsChanged,
  onSummaryChanged,
}: UsePromptPlaygroundActionsOptions) {
  const { debugValuesText, setDebugValuesText, debugResult } = usePromptDebugState(draft);

  const runDebugMut = useMutation({
    mutationFn: async (data: CreatePromptEvaluationAssetRequest) => {
      const asset = await api.createPromptEvaluationAsset(data);
      return api.runPromptEvaluationAsset(asset.id);
    },
    onSuccess: () => {
      onAssetsChanged();
      onCasesChanged();
      onRunsChanged();
      onSummaryChanged();
      toast.success("优化运行已记录");
    },
  });

  const createAssetMut = useMutation({
    mutationFn: (data: CreatePromptEvaluationAssetRequest) => api.createPromptEvaluationAsset(data),
    onSuccess: () => {
      onAssetsChanged();
      onCasesChanged();
      onExperimentDimensionsChanged();
      onSummaryChanged();
      toast.success("资产已创建");
    },
  });

  const runDebug = () => {
    if (!selected) {
      toast.error("请先保存提示词");
      return;
    }
    const values = parseDebugValues(debugValuesText);
    runDebugMut.mutate({
      prompt_id: selected.id,
      name: `${selected.name} 优化运行 ${new Date().toLocaleString("zh-CN")}`,
      description: "从提示词调试场记录",
      asset_type: "优化运行",
      payload: {
        cases: [
          {
            名称: "调试场用例",
            变量: values,
            期望包含: debugResult.missingVariables.length === 0 ? debugResult.usedVariables.map((key) => values[key]).filter(Boolean) : [],
          },
        ],
        调试输出: debugResult.rendered,
      },
      status: "启用",
    });
  };

  const createWorkbenchAsset = (assetType: PromptEvaluationAssetType) => {
    let prompt: PromptLibraryItem | null = null;
    if (selectedPromptStorageKey) {
      try {
        const storedId = window.localStorage.getItem(selectedPromptStorageKey);
        prompt = storedId ? items.find((item) => item.id === storedId) ?? null : null;
      } catch {
        prompt = null;
      }
    }
    prompt = prompt ?? selected;
    prompt = prompt ?? items[0] ?? null;
    if (!prompt) {
      toast.error("请先保存提示词");
      return;
    }
    const values = parseDebugValues(debugValuesText);
    const now = new Date().toLocaleString("zh-CN");
    createAssetMut.mutate({
      prompt_id: prompt.id,
      name: `${prompt.name} ${assetType} ${now}`,
      description: `从中文工作台创建的${assetType}`,
      asset_type: assetType,
      payload: buildAssetPayload(assetType, prompt, values, debugResult.rendered),
      status: "启用",
    });
  };

  return {
    debugValuesText,
    setDebugValuesText,
    debugResult,
    runningDebug: runDebugMut.isPending,
    creatingAsset: createAssetMut.isPending,
    runDebug,
    createWorkbenchAsset,
  };
}

export function useAgentPlaygroundActions({
  draft,
  selected,
  agentRuntimeReadiness,
  selectedExecutionAgent,
  onAssetsChanged,
  onCasesChanged,
  onExperimentDimensionsChanged,
  onRunsChanged,
  onSummaryChanged,
}: UseAgentPlaygroundActionsOptions) {
  const { debugValuesText, setDebugValuesText, debugResult } = usePromptDebugState(draft);
  const [agentExpectedText, setAgentExpectedText] = useState("");

  const createAssetMut = useMutation({
    mutationFn: (data: CreatePromptEvaluationAssetRequest) => api.createPromptEvaluationAsset(data),
    onSuccess: () => {
      onAssetsChanged();
      onCasesChanged();
      onExperimentDimensionsChanged();
      onSummaryChanged();
      toast.success("智能体调试包已保存");
    },
  });

  const runAgentMut = useMutation({
    mutationFn: async (data: CreatePromptEvaluationAssetRequest) => {
      const asset = await api.createPromptEvaluationAsset(data);
      return api.runPromptEvaluationAssetAgent(asset.id);
    },
    onSuccess: (result) => {
      onAssetsChanged();
      onCasesChanged();
      onExperimentDimensionsChanged();
      onRunsChanged();
      onSummaryChanged();
      toast.success(`真实智能体任务已入队：${result.task_id}`);
    },
  });

  const saveAgentDebugPackage = () => {
    const prompt = selected;
    if (!prompt) {
      toast.error("请先保存提示词");
      return;
    }
    createAssetMut.mutate(buildAgentDebugPackageRequest(prompt, parseDebugValues(debugValuesText), debugResult.rendered, agentExpectedText, agentRuntimeReadiness, selectedExecutionAgent));
  };

  const runAgentDebugPackage = () => {
    const prompt = selected;
    if (!prompt) {
      toast.error("请先保存提示词");
      return;
    }
    if (!selectedExecutionAgent && agentRuntimeReadiness.status !== "就绪") {
      toast.error(agentRuntimeReadiness.fix);
      return;
    }
    const values = parseDebugValues(debugValuesText);
    runAgentMut.mutate(buildAgentDebugPackageRequest(prompt, values, debugResult.rendered, agentExpectedText, agentRuntimeReadiness, selectedExecutionAgent));
  };

  return {
    debugValuesText,
    setDebugValuesText,
    agentExpectedText,
    setAgentExpectedText,
    debugResult,
    runningAgent: runAgentMut.isPending,
    creatingAgentPackage: createAssetMut.isPending,
    saveAgentDebugPackage,
    runAgentDebugPackage,
  };
}
