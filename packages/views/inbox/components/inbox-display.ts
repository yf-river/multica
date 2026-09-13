// 收件箱条目「展示标题 / 结果详情」的纯函数：把快速创建等系统消息的标题清洗成
// 用户可读文案，并处理详情面板在列表选中滞后时的选行解析。

import type { InboxItem } from "@multica/core/types";

/** 把多行文本压成单行（空白折叠后去首尾）。 */
function singleLine(value: string | null | undefined): string {
  return (value ?? "").replace(/\s+/g, " ").trim();
}

/** 转义正则特殊字符，供构造字面匹配用。 */
function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** 去掉「Created ABC-123:」式快速创建标题前缀，让行首直接是用户想看的内容。 */
export function stripQuickCreatePrefix(title: string, identifier?: string): string {
  const normalized = singleLine(title);
  if (!normalized) return "";

  if (identifier) {
    const exactPrefix = new RegExp(
      `^Created\\s+${escapeRegExp(identifier)}:\\s*`,
      "i",
    );
    const withoutExactPrefix = normalized.replace(exactPrefix, "");
    if (withoutExactPrefix !== normalized) return withoutExactPrefix.trim();
  }

  return normalized.replace(/^Created\s+[A-Z][A-Z0-9]*-\d+:\s*/i, "").trim();
}

/** 计算收件箱条目的展示标题：快速创建类优先用清洗后的标题或原始提示词，其余回退原文。 */
export function getInboxDisplayTitle(item: InboxItem): string {
  const details = item.details ?? {};

  if (item.type === "quick_create_done") {
    const cleanedTitle = stripQuickCreatePrefix(item.title, details.identifier);
    if (cleanedTitle) return cleanedTitle;

    const prompt = singleLine(details.original_prompt);
    if (prompt) return prompt;
  }

  if (isQuickCreateOutcome(item.type)) {
    const prompt = singleLine(details.original_prompt);
    if (prompt) return prompt;
  }

  return item.title;
}

/**
 * 两种「非成功」的快速创建结果。它们共享同一行形态（原始提示词 + 补救入口），
 * 但绝不能共用失败措辞：unconfirmed（未确认）意味着结果没被核实，
 * 而不是失败了。
 */
export function isQuickCreateOutcome(type: InboxItem["type"]): boolean {
  return type === "quick_create_failed" || type === "quick_create_unconfirmed";
}

/** 取快速创建失败/未确认时的详情：优先 error 字段，其次正文。 */
export function getQuickCreateOutcomeDetail(item: InboxItem): string {
  const details = item.details ?? {};
  return singleLine(details.error) || singleLine(item.body);
}

/**
 * 决定详情面板在「选中滞后于列表」的窗口期渲染哪一行。
 *
 * 面板的 key 用 `useDeferredValue` 延迟，这样装载任务详情按 transition 优先级
 * 而不是阻塞点击——因此存在一段列表高亮已移动、面板还没移动的窗口。正常情况下
 * 面板应继续展示它正在展示的那一行——这正是延迟的意义，进度条盖住的也是它。
 *
 * 例外是旧 key 已经解析不到了：归档操作在同一次提交里把选中移到邻行并移除被
 * 处理行，已经没有可「留住」的对象。此时渲染 miss 会在两条任务之间闪一下空态，
 * 所以这种情况直接跳到最新的选中行。
 *
 * @param visibleItems 当前列表可见条目
 * @param selectedKey 列表当前选中 key
 * @param detailKey 面板正在展示（延迟中）的 key
 * @returns 应该展示的条目；无可展示时 null
 */
export function resolveDetailItem(
  visibleItems: InboxItem[],
  selectedKey: string,
  detailKey: string,
): InboxItem | null {
  const byKey = (key: string) =>
    visibleItems.find((i) => (i.issue_id ?? i.id) === key) ?? null;
  const deferred = detailKey ? byKey(detailKey) : null;
  if (deferred) return deferred;
  // 没有可留住的旧行：要么面板本为空（还没选中），要么它展示的那行已消失。
  // 只有第二种情况才往前落到最新选中行。
  if (!detailKey || detailKey === selectedKey) return null;
  return selectedKey ? byKey(selectedKey) : null;
}
