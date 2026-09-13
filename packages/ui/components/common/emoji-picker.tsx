"use client";
// 本文件是表情选择器组件：基于 emoji-mart 的 Picker 弹出面板，选中的表情以纯文本形式回传给调用方。
// 组件实例化一次后挂在容器 div 里，卸载时清空容器避免重复挂载。

import { useEffect, useRef, useCallback } from "react";
import data from "@emoji-mart/data";
import { Picker } from "emoji-mart";

/**
 * EmojiPicker 组件的 props 定义。
 */
interface EmojiPickerProps {
  /** 用户选中某个表情时的回调，参数是表情对应的 unicode 字符（如 "😀"） */
  onSelect: (emoji: string) => void;
}

/**
 * EmojiPicker：表情选择面板。
 *
 * 注意：Picker 是命令式构造的 DOM 组件（不是 React 组件），所以用一个空 div 做挂载点，
 * 在 effect 里手动 appendChild 上去，并在卸载时清空容器。
 *
 * @param props 见 EmojiPickerProps 定义
 * @returns 承载表情面板的容器 div
 */
export function EmojiPicker({ onSelect }: EmojiPickerProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  // 用 ref 保存最新回调：onSelect 每次渲染都可能变，但 Picker 只创建一次，
  // 通过 ref 中转可以让它永远调到最新的 onSelect，又不必为此重建 Picker。
  const onSelectRef = useRef(onSelect);
  onSelectRef.current = onSelect;

  // 只依赖 emoji.native 的解包函数保持引用稳定，作为 effect 的稳定依赖
  const handleSelect = useCallback((emoji: { native: string }) => {
    onSelectRef.current(emoji.native);
  }, []);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const picker = new Picker({
      data,
      onEmojiSelect: handleSelect,
      theme: "auto",
      set: "native",
      previewPosition: "none",
      skinTonePosition: "search",
      maxFrequentRows: 2,
    });

    container.appendChild(picker as unknown as Node);

    // 卸载时清空容器，避免二次挂载时出现重复/残留的 Picker
    return () => {
      container.replaceChildren();
    };
  }, [handleSelect]);

  return <div ref={containerRef} />;
}
