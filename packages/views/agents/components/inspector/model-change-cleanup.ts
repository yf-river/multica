// 本文件负责在设置页“换模型”时拼出单次 PATCH 请求体，并顺带清理掉新模型不再支持的
// “推理档位 / 服务档位”等按模型绑定的旧值。

import type { RuntimeModel } from "@multica/core/types";
import { findModelCapabilityEntry } from "./model-capability";

/**
 * 该 Agent 运行环境的“逐模型目录”，`null` 表示目录不可信——运行环境离线、发现仍在途、
 * 或发现失败。`[]` 则表示 provider 有响应但一个模型都没提供。
 */
export type ModelCatalog = readonly RuntimeModel[] | null;

/** 类型别名（不用 interface），保持可赋值给检查器 `Record<string, unknown>` 的更新载荷。 */
export type ModelChangeUpdate = {
  model: string;
  thinking_level?: string;
  service_tier?: string;
};

/**
 * 为设置页的一次“模型变更”构建唯一的 PATCH 请求体。
 *
 * `thinking_level` 与目录里的服务档位都是按模型绑定的能力：若新模型从未宣称支持某个值，
 * 却仍保留它，就会留下一个孤儿覆盖，daemon 执行时悄悄丢弃——设置页因此会声称
 * 一个从未真正跑过的档位（MUL-5390）。显式 Standard 是例外：它宣称的能力属于
 * 所装的 Codex CLI 本身。但无脑全清更糟——在两个都支持 Fast 的模型间切换会白扔
 * 用户的选择；目录未知时清空，又会删掉 daemon 本会尊重的值。
 *
 * 所以策略是：只在“可信目录”明确说新模型不支持时才清。目录未知、模型未知、或模型为空
 * （“跟随运行时默认”，该值从 daemon 宿主机自身配置解析，这里无从校验）时，
 * 保留原值不动——它们继续显示在检查器里，用户可以自己显式清掉。
 *
 * 两次清理与模型同行、放进同一次请求：若分成第二次补写，中间会留下一个窗口，
 * Agent 以“UI 从没打算过的 模型/档位 组合”被持久化。
 */
export function buildModelChangeUpdate(input: {
  provider: string;
  model: string;
  thinkingLevel: string;
  serviceTier: string;
  catalog: ModelCatalog;
}): ModelChangeUpdate {
  const update: ModelChangeUpdate = { model: input.model };
  if (!input.thinkingLevel && !input.serviceTier) return update;
  if (input.catalog === null || !input.model) return update;

  const entry = findModelCapabilityEntry(
    input.catalog,
    input.model,
    input.provider,
  );
  if (!entry) return update;

  const supportsThinking = (entry.thinking?.supported_levels ?? []).some(
    (level) => level.value === input.thinkingLevel,
  );
  if (input.thinkingLevel && !supportsThinking) update.thinking_level = "";

  const supportsTier =
    (entry.service_tiers ?? []).some((tier) => tier.id === input.serviceTier) ||
    (input.serviceTier === "default" &&
      input.catalog.some(
        (candidate) =>
          candidate.supports_explicit_standard_service_tier === true,
      ));
  if (input.serviceTier && !supportsTier) update.service_tier = "";

  return update;
}
