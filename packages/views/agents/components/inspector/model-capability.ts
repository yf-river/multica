// 本文件提供一组辅助函数：把 Agent 存的“原始模型串”规整成查能力目录用的 id，
// 再去目录里找对应的能力条目。持久化、发送给运行环境的始终是原始模型串，
// 这里被归一化的只是“查表用的身份”。

import type { RuntimeModel } from "@multica/core/types";

// Claude Code 会给部分运行时原生模型 id 追加“上下文窗口”修饰符
//（例如 claude-opus-5[1m]）。只剥“数字上下文”这种形态，
// 其他带方括号的变体保持原样，宁可漏剥也不误伤。
const CLAUDE_CONTEXT_WINDOW_TAG = /\[[1-9]\d*[km]\]$/;

/**
 * 规整出用于查能力目录的模型 id：claude 系模型剥掉 `[1m]` 这类上下文窗口修饰符，
 * 其他 provider 原样返回。
 */
export function modelIdForCapabilityLookup(
  provider: string,
  model: string,
): string {
  return provider === "claude"
    ? model.replace(CLAUDE_CONTEXT_WINDOW_TAG, "")
    : model;
}

/**
 * 解析用于“能力展示与清理”的目录条目。原始模型仍作为持久化和下发运行时的值，
 * 只有这里的查表身份做了归一化（见上）。
 */
export function findModelCapabilityEntry(
  models: readonly RuntimeModel[],
  model: string,
  provider: string,
): RuntimeModel | undefined {
  if (!model) return undefined;
  const lookupId = modelIdForCapabilityLookup(provider, model);
  // Normalize the catalog side too. Claude discovery reports what the CLI
  // would really run, tag included (`claude-opus-5[1m]`), so comparing a
  // stripped query against raw catalog ids would miss the entry for the very
  // model the user just picked — and hiding a model's own effort picker is how
  // that failure would show up (MUL-6961). Mirrors the daemon's lookup in
  // ValidateThinkingLevelWith.
  return models.find(
    (entry) => modelIdForCapabilityLookup(provider, entry.id) === lookupId,
  );
}
