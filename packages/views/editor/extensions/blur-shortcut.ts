import { Extension } from "@tiptap/core";

/**
 * Esc → 让编辑器失焦。不这么做的话，在 contenteditable 里按 ESC 什么也
 * 不发生（浏览器默认不会让 contenteditable 失焦），用户会被困在编辑器里，
 * 没有任何键盘逃逸出口。
 *
 * @returns 一个 Tiptap 扩展：给编辑器注册 Escape 快捷键并执行 blur。
 */
export function createBlurShortcutExtension() {
  return Extension.create({
    name: "blurShortcut",
    addKeyboardShortcuts() {
      return {
        Escape: ({ editor }) => {
          editor.commands.blur();
          return true;
        },
      };
    },
  });
}
