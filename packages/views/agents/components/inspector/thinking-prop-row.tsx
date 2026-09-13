"use client";

// 本文件是 Agent 检查器里的“推理档位”行 + General 设置表单里的同款全宽字段：
// 都从运行时的逐模型目录里读出当前模型的 supported_levels，交 ThinkingPicker 渲染。
// 模型没有可声明的推理档位且没有已存值时整行隐藏。

import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import type { RuntimeModel } from "@multica/core/types";
import { runtimeModelsOptions } from "@multica/core/runtimes";
import { PropRow } from "../../../common/prop-row";
import { SettingsRow } from "../../../settings/components/settings-layout";
import { useT } from "../../../i18n";
import { ThinkingPicker } from "./thinking-picker";
import { findModelCapabilityEntry } from "./model-capability";

/**
 * 检查器里的“推理档位”行。当前模型既没宣称 `supported_levels`、又没存过值时隐藏，
 * 这样不暴露推理能力的 provider 永远不会冒出一行空行。若 Agent 已保存过
 * `thinking_level`（换到了不支持推理的运行时，或 daemon / CLI 的目录缩水把条目
 * 弄丢了），仍要渲染这一行——让用户能看到后端仍在发送的孤儿值，并通过选择器的
 * 底部入口显式清掉它。PR1 对“按模型非法”的行为是 daemon 侧警告/丢弃，不是同步
 * 清库，所以前端必须如实呈现已持久化的状态。
 *
 * 复用共享的 runtime-models 查询，和模型选择器打同一份 60s 缓存，检查器热路径上
 * 不再多发一次请求。旁边的 ModelPicker 无条件挂载，共享查询的订阅由检查器挂载时
 * 就已建立——这里返回 null 并不会取消发现。
 */
export function ThinkingPropRow({
  runtimeId,
  runtimeOnline,
  provider,
  model,
  value,
  canEdit,
  onChange,
}: {
  runtimeId: string | null;
  runtimeOnline: boolean;
  /** 运行时 provider 类型（如 "codex"、"claude"）。用于判断“模型为空”时能否
   *  安全预览默认模型的推理目录。 */
  provider: string;
  model: string;
  value: string;
  canEdit: boolean;
  onChange: (next: string) => Promise<void> | void;
}) {
  const { t } = useT("agents");
  const modelsQuery = useQuery(
    runtimeModelsOptions(runtimeOnline ? runtimeId : null),
  );

  const models = modelsQuery.data?.models ?? [];
  const entry = pickModelEntry(models, model, provider);
  const levels = entry?.thinking?.supported_levels ?? [];
  if (levels.length === 0 && !value) return null;

  return (
    <PropRow label={t(($) => $.inspector.prop_thinking)} interactive={false}>
      <ThinkingPicker
        value={value}
        levels={levels}
        canEdit={canEdit}
        onChange={onChange}
      />
    </PropRow>
  );
}

/** General 设置表单里使用的全宽同款字段。 */
export function ThinkingSettingField({
  label,
  runtimeId,
  runtimeOnline,
  provider,
  model,
  value,
  canEdit,
  onChange,
}: {
  label: ReactNode;
  runtimeId: string | null;
  runtimeOnline: boolean;
  provider: string;
  model: string;
  value: string;
  canEdit: boolean;
  onChange: (next: string) => Promise<void> | void;
}) {
  const modelsQuery = useQuery(
    runtimeModelsOptions(runtimeOnline ? runtimeId : null),
  );
  const models = modelsQuery.data?.models ?? [];
  const entry = pickModelEntry(models, model, provider);
  const levels = entry?.thinking?.supported_levels ?? [];

  if (levels.length === 0 && !value) return null;

  return (
    <SettingsRow label={label} size="select-wide">
      <ThinkingPicker
        variant="field"
        showLabel={false}
        value={value}
        levels={levels}
        canEdit={canEdit}
        onChange={onChange}
      />
    </SettingsRow>
  );
}

/** 挑出用来展示能力的模型目录条目：有模型走归一化查表；模型为空走 provider 兜底。 */
function pickModelEntry(
  models: RuntimeModel[],
  model: string,
  provider: string,
): RuntimeModel | undefined {
  if (model) return findModelCapabilityEntry(models, model, provider);
  // 模型为空 = “跟随运行时的默认”。对 codex 来说该默认来自本地 config.toml，
  // 可能是任意已装模型，所以绝不能拿打标 Default 条目的推理目录来预览——
  // 比如 gpt-5.6-sol 单独宣称支持 `ultra`，而实际配置的模型未必支持。
  // 宁可失败关闭（不预览）：这一行直接隐藏，除非还残留着旧档位——那时仍渲染，
  // 好让用户清掉孤儿值。与后端 ValidateThinkingLevel 逻辑呼应。（MUL-4347）
  if (provider === "codex") return undefined;
  return models.find((m) => m.default) ?? models[0];
}
