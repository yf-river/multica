// 把服务端下发的任务失败原因（failure_reason）解析成界面可用的文案 key。
import type { TaskFailureReason } from "../types";

/**
 * 为某个服务端下发的 `failure_reason` 挑出界面应渲染的文案 key，
 * 找不到精确匹配时优雅降级而不是直接掉崖。
 *
 * 解析顺序：
 *   1. 精确匹配 —— 界面给这个具体原因备了文案。
 *   2. 粗粒度归族 —— `agent_error.provider_network` 在界面只有族级文案时
 *      降级用 `agent_error`。
 *   3. `undefined` —— 没有可用的；调用方渲染自己的兜底文案。
 *
 * 为什么存在：后端的分类更细（`agent_error.*`），且演进速度独立于已发布的客户端。
 * MUL-5370 之前各界面拿这份精确枚举只做六值粗分类的 Record 查表，于是每个细分原因
 * 都落空、渲染出一句笼统的"出错了"——后端算好的有用分类在最后一步被扔掉。
 * 第 2 步让没见过的 `agent_error.<新东西>` 落到族级文案而不是兜底，这就是仓库里
 * "枚举漂移要降级、不要崩"的规则在文案上的应用。
 *
 * 纯函数、无依赖，移动端可直接 import。
 * @param reason 服务器下发的失败原因。
 * @param copy 界面文案表：key → 文案。
 * @returns 要用的文案 key；没有可用项返回 undefined。
 */
export function resolveFailureReasonKey<K extends string>(
  reason: TaskFailureReason | string | null | undefined,
  copy: Readonly<Partial<Record<K, unknown>>>,
): K | undefined {
  if (!reason) return undefined;
  if (Object.prototype.hasOwnProperty.call(copy, reason)) {
    return reason as K;
  }
  const separator = reason.indexOf(".");
  if (separator > 0) {
    const family = reason.slice(0, separator);
    if (Object.prototype.hasOwnProperty.call(copy, family)) {
      return family as K;
    }
  }
  return undefined;
}
