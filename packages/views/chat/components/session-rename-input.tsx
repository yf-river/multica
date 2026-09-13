// 会话标题行内编辑框：挂载即聚焦并全选原标题，Enter 提交、Escape 取消、
// 点击输入框外部也会提交。
"use client";

import { useEffect, useRef, useState } from "react";
import { isImeComposing } from "@multica/core/utils";
import { useT } from "../../i18n";

/**
 * 会话标题的行内编辑器。挂载时聚焦并预选原标题，让用户既能整个替换、
 * 也能用方向键进入已有文字。Enter 提交，Escape 取消，真正的点击输入框外部也会提交。
 *
 * 我们故意不在输入框的 `blur` 事件上提交：历史弹层（history popover）在用户仍与
 * 面板交互时，可能把焦点移到兄弟行或嵌套的操作上。取而代之的是 document 级的
 * `pointerdown` 监听，只在用户真的点到输入框外面时才提交。
 */
export function SessionRenameInput({
  initialValue,
  onSubmit,
  onCancel,
}: {
  initialValue: string;
  onSubmit: (value: string) => void;
  onCancel: () => void;
}) {
  const { t } = useT("chat");
  const [value, setValue] = useState(initialValue);
  const inputRef = useRef<HTMLInputElement>(null);
  // 把最新的值和回调放进 ref，这样只在挂载时注册一次的监听器永远能看到新鲜状态，
  // 而不用每次按键都重新订阅（那会短暂留下一个 pointerdown 未被观察的窗口期）。
  const valueRef = useRef(value);
  valueRef.current = value;
  const onSubmitRef = useRef(onSubmit);
  onSubmitRef.current = onSubmit;

  useEffect(() => {
    inputRef.current?.focus();
    inputRef.current?.select();

    const handlePointerDown = (e: PointerEvent) => {
      const input = inputRef.current;
      if (!input) return;
      if (input.contains(e.target as Node)) return;
      onSubmitRef.current(valueRef.current);
    };
    // 用捕获阶段——在"点击外部"的处理能关掉弹层、卸载本组件之前先提交。
    document.addEventListener("pointerdown", handlePointerDown, true);
    return () => {
      document.removeEventListener("pointerdown", handlePointerDown, true);
    };
  }, []);

  return (
    <input
      ref={inputRef}
      type="text"
      value={value}
      maxLength={200}
      aria-label={t(($) => $.session_history.row_rename_aria)}
      onChange={(e) => setValue(e.target.value)}
      onClick={(e) => e.stopPropagation()}
      onPointerDown={(e) => e.stopPropagation()}
      onKeyDown={(e) => {
        // 让编辑键留在输入框里，而不是被行的键盘选中处理器消费掉。
        e.stopPropagation();
        if (isImeComposing(e)) return;
        if (e.key === "Enter") {
          e.preventDefault();
          onSubmit(value);
        } else if (e.key === "Escape") {
          e.preventDefault();
          onCancel();
        }
      }}
      className="w-full rounded-sm bg-background px-1 py-0.5 text-body outline-none ring-1 ring-border focus-visible:ring-brand"
    />
  );
}
