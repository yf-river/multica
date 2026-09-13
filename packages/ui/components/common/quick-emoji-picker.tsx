"use client";
// 本文件是「快捷表情选择」：一个笑脸小按钮，点开弹出常用表情的快捷面板，
// 一列常用表情 + 一个「更多表情」入口。完整表情面板是懒加载的，只有点「更多」
// 才真正下载。

import { useState, lazy, Suspense } from "react";
import { SmilePlus } from "lucide-react";
import { Popover, PopoverTrigger, PopoverContent } from "@multica/ui/components/ui/popover";

// 完整表情面板体积不小，懒加载：只有用户点「More emojis...」时才拉取代码
const EmojiPicker = lazy(() =>
  import("./emoji-picker").then((m) => ({ default: m.EmojiPicker })),
);

/** 快捷面板里预设的常用表情 */
const QUICK_EMOJIS = ["👍", "👌", "❤️", "✅", "🎉", "😕", "🚀", "👀"];

/**
 * QuickEmojiPicker 组件的 props 定义。
 */
interface QuickEmojiPickerProps {
  /** 选中表情（或点快捷表情）时的回调，参数为 unicode 表情字符 */
  onSelect: (emoji: string) => void;
  /** 弹出面板的对齐方向：'start' 靠左 / 'end' 靠右 */
  align?: "start" | "end";
  /** 额外的触发按钮 class 名 */
  className?: string;
}

/**
 * QuickEmojiPicker：两层结构的表情快捷选择器。
 * 第一层是预设的 8 个常用表情，第二层（showFull）切换到懒加载的完整表情面板。
 * 选完或关闭面板都会把状态复位到第一层，下次打开仍是快捷层。
 *
 * @param props 见 QuickEmojiPickerProps 定义
 * @returns 快捷表情触发按钮与弹出面板
 */
function QuickEmojiPicker({ onSelect, align = "start", className }: QuickEmojiPickerProps) {
  const [open, setOpen] = useState(false);
  const [showFull, setShowFull] = useState(false);

  const handleOpenChange = (v: boolean) => {
    setOpen(v);
    // 面板关闭时把内部层级复位到快捷层，避免下次打开还停留在完整面板
    if (!v) setShowFull(false);
  };

  const handleSelect = (emoji: string) => {
    onSelect(emoji);
    setOpen(false);
    setShowFull(false);
  };

  return (
    <Popover open={open} onOpenChange={handleOpenChange}>
      <PopoverTrigger
        render={
          <button
            type="button"
            className={`inline-flex items-center justify-center h-6 w-6 rounded-full text-muted-foreground hover:bg-accent hover:text-foreground transition-colors ${className ?? ""}`}
          >
            <SmilePlus className="h-3.5 w-3.5" />
          </button>
        }
      />
      <PopoverContent align={align} className="w-auto p-0">
        {showFull ? (
          <Suspense fallback={<div className="p-4 text-body text-muted-foreground">Loading...</div>}>
            <EmojiPicker onSelect={handleSelect} />
          </Suspense>
        ) : (
          <div className="p-2">
            <div className="flex gap-1">
              {QUICK_EMOJIS.map((emoji) => (
                <button
                  key={emoji}
                  type="button"
                  onClick={() => handleSelect(emoji)}
                  className="h-8 w-8 flex items-center justify-center rounded hover:bg-accent text-title-sm transition-colors"
                >
                  {emoji}
                </button>
              ))}
            </div>
            <button
              type="button"
              onClick={() => setShowFull(true)}
              className="mt-1.5 w-full text-caption text-muted-foreground hover:text-foreground text-center py-1 rounded hover:bg-accent transition-colors"
            >
              More emojis...
            </button>
          </div>
        )}
      </PopoverContent>
    </Popover>
  );
}

export { QuickEmojiPicker };
