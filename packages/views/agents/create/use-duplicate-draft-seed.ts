"use client";

// 本文件是“复制 Agent”创建表单的草稿播种 hook：只在运行环境查询已定案之后、
// 且只播种一次，把源 Agent 转成可编辑草稿——避免对空运行环境列表播种导致的
// 模型/推理/档位被错误清空。

import { useEffect, useRef } from "react";
import { buildDuplicateDraft, type AgentDraft } from "@multica/core/agents";
import type { Agent, RuntimeDevice } from "@multica/core/types";

/**
 * 只播种一次“复制草稿”，且只在运行环境列表真正应答之后才播。
 *
 * 顺序很关键：Agent 列表与运行环境列表是两条独立查询，冷启动（直达
 * `?duplicate=<id>` 链接，或刷新）时，Agent 可能先到而运行环境还在路上。此刻播种
 * 会拿 `[]` 去跑 `buildDuplicateDraft`，把源运行环境判成不可用，进而清掉
 * “同运行环境复制必须保留”的模型 / 推理 / 档位——而且是永久性的，因为播种只发生
 * 一次。反过来每次运行环境变化都重播，又会覆盖用户已经做的修改，所以门槛设在
 * “查询已可判定”，而不是“数据变了”。
 */
export function useDuplicateDraftSeed({
  source,
  runtimesSettled,
  runtimes,
  currentUserId,
  fallbackRuntimeId,
  nameSuffix,
  onSeed,
}: {
  source: Agent | null;
  /** 运行环境查询已 resolve 或失败——pending 期间为 `false`。 */
  runtimesSettled: boolean;
  runtimes: RuntimeDevice[];
  currentUserId: string | null;
  fallbackRuntimeId: string;
  nameSuffix: string;
  /** 播种结果回调；第二参表示运行时是否被重置（与源 Agent 不同）。 */
  onSeed: (draft: AgentDraft, runtimeReset: boolean) => void;
}): void {
  const seededRef = useRef(false);
  // 该回调每次渲染都重建是刻意的（它闭包捕获了 setState），所以把它挡在
  // effect 依赖列表之外。
  const onSeedRef = useRef(onSeed);
  onSeedRef.current = onSeed;

  useEffect(() => {
    if (seededRef.current || !source || !runtimesSettled) return;
    seededRef.current = true;
    const draft = buildDuplicateDraft(source, {
      runtimes,
      currentUserId,
      fallbackRuntimeId,
      nameSuffix,
    });
    onSeedRef.current(draft, draft.runtimeId !== source.runtime_id);
  }, [
    currentUserId,
    fallbackRuntimeId,
    nameSuffix,
    runtimes,
    runtimesSettled,
    source,
  ]);
}
