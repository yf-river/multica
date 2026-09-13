"use client";

import type { Label } from "@multica/core/types";
import { X } from "lucide-react";
import { useT } from "../i18n";

/**
 * labels 模块：把单个标签渲染成彩色小胶囊，供 issue 详情侧栏、选择器和
 * 管理弹窗使用。
 */

/**
 * 把标签的 `#rrggbb` 颜色映射成一个可读的文字颜色。
 *
 * 用 ITU-R BT.601 感知亮度公式：高于阈值的颜色配深色文字（#111827），低于
 * 阈值配浅色文字（#f9fafb）。对粉彩与高饱和两种配色都有效，无需硬查表。
 *
 * hex 格式非法时回退成深字配默认背景——在默认 `backgroundColor` 渲染路径下
 * 可读，比纯黑更好（纯黑在深色胶囊上会消失）。
 *
 * 安全不变量：`LabelChip` 直接用 `style={{ backgroundColor: color }}`，信任
 * 后端的颜色格式。后端的 `normalizeColor` 正则把值钉死在 `^#?[0-9a-fA-F]{6}$`。
 * 一旦该正则放宽（命名色、`url(...)` 等），这里就会变成注入点。
 */
function contrastTextColor(hex: string): string {
  const h = hex.replace("#", "");
  if (h.length !== 6) return "#111827";
  const r = parseInt(h.slice(0, 2), 16) / 255;
  const g = parseInt(h.slice(2, 4), 16) / 255;
  const b = parseInt(h.slice(4, 6), 16) / 255;
  const luminance = 0.299 * r + 0.587 * g + 0.114 * b;
  return luminance > 0.55 ? "#111827" : "#f9fafb";
}

/** 标签胶囊组件的属性。 */
interface LabelChipProps {
  label: Label;
  onRemove?: () => void;
  className?: string;
  /**
   * 为 true 时显示完整的标签名、不截断。用于需要用户看清或核对完整名称的
   * 管理/编辑界面。默认（false）把名称截断在 12rem，让胶囊在 issue 侧栏与
   * 将来的看板/列表卡片行里保持紧凑。
   */
  fullName?: boolean;
}

/**
 * 单个标签的彩色胶囊。提供了 `onRemove` 就显示一个调用它的 × 按钮。
 */
export function LabelChip({ label, onRemove, className, fullName }: LabelChipProps) {
  const { t } = useT("labels");
  const textColor = contrastTextColor(label.color);
  const nameClass = fullName ? "break-all" : "truncate max-w-[12rem]";
  return (
    <span
      // `min-w-0` 让胶囊在受宽度约束的容器（创建工具栏的标签药丸）里作为
      // flex 项时可收缩。没有它，胶囊保持内容宽度、父容器把它拦腰截断；
      // 有了它，内层名称截断、胶囊保住两端的圆角。
      className={`inline-flex min-w-0 items-center gap-1 rounded-full px-2 py-0.5 text-caption font-medium ${className ?? ""}`}
      style={{ backgroundColor: label.color, color: textColor }}
      // 视觉截断时 aria-label 把完整名称交给读屏器；title 留给明眼人的
      // 悬停提示。
      aria-label={label.name}
      title={label.name}
    >
      <span className={nameClass}>{label.name}</span>
      {onRemove && (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            onRemove();
          }}
          // bg-current/20 基于计算出的文字颜色，让悬停态在浅色与深色胶囊
          // 背景上都可见。hover:bg-black/10 在深色胶囊（需要浅色文字的那些）
          // 上会看不见。
          className="flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-full hover:bg-current/20 focus:outline-none focus:ring-1 focus:ring-current"
          aria-label={t(($) => $.remove_label, { name: label.name })}
        >
          <X className="h-2.5 w-2.5" strokeWidth={2.5} />
        </button>
      )}
    </span>
  );
}
