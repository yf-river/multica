"use client";

import { useT } from "../../i18n";

// 本文件提供一个字数计数器，用于在文本输入框下方显示已输入字数与上限的关系。

/**
 * 字数计数器：显示“当前字数 / 上限”，并在接近或超出上限时给出颜色提示。
 * 超过 90% 时温和警告，超过上限时标红报错。被描述编辑器（弹窗）和创建 Agent
 * 对话框共用，保证两处读法一致。只渲染成一行内联文本，可放在任意 textarea /
 * input 下方而不打乱周边间距。
 *
 * @param length 当前已输入的字数
 * @param max 允许的最大字数上限
 */
export function CharCounter({ length, max }: { length: number; max: number }) {
  const { t } = useT("agents");
  const over = length > max;
  const near = !over && length >= Math.floor(max * 0.9);
  const tone = over
    ? "text-destructive"
    : near
      ? "text-warning"
      : "text-muted-foreground";
  return (
    <div className={`text-right text-caption tabular-nums ${tone}`}>
      {length} / {max}
      {over && t(($) => $.char_counter.over_limit, { count: length - max })}
    </div>
  );
}
