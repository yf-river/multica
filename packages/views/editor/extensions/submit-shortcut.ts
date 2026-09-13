/**
 * 发送/提交快捷键扩展：把用户配置的「发送」快捷键接进编辑器（含把普通 Enter
 * 变为发送的场景），并在 Enter 已被占用为发送时，让 Shift+Enter 重放编辑器
 * 原生的 Enter 行为（换行），保证发送功能不侵蚀正常编辑能力。
 * 前半部分导出 shouldHandleSubmitShortcut / shouldReplayNativeEnter 两个纯判断
 * 函数（便于单测），后半部分的 createSubmitShortcutExtension 把它们装配成
 * Tiptap 扩展。
 */
import { Extension } from "@tiptap/core";
import { Plugin } from "@tiptap/pm/state";
import {
  getShortcut,
  isPlainShortcut,
  shortcutMatchesEvent,
  type ShortcutChord,
} from "@multica/core/shortcuts";
import { isImeComposing } from "@multica/core/utils";

/**
 * 判断一次按键是否应当触发「发送」。
 *
 * 连发（按住不放）、输入法组合中、或与用户配置的发送快捷键不匹配的按键
 * 一律不算。
 */
export function shouldHandleSubmitShortcut(
  event: KeyboardEvent,
  options: {
    configuredShortcut: ShortcutChord | null;
    composing: boolean;
  },
): boolean {
  return (
    !event.repeat &&
    !options.composing &&
    !isImeComposing(event) &&
    shortcutMatchesEvent(options.configuredShortcut, event)
  );
}

/**
 * 当「普通 Enter 变成了发送」后，Shift+Enter 必须继承原 Enter 的职责，
 * 而不是走 Tiptap 默认的硬换行。重放编辑器自身的 Enter 键位，能保留
 * 段落、补丁过的列表退出/续写、代码块换行，以及打开的 suggestion 弹层。
 */
export function shouldReplayNativeEnter(
  event: KeyboardEvent,
  configuredShortcut: ShortcutChord | null,
  composing: boolean,
): boolean {
  return (
    !composing &&
    !isImeComposing(event) &&
    isPlainShortcut(configuredShortcut, "Enter") &&
    event.key === "Enter" &&
    event.shiftKey &&
    !event.metaKey &&
    !event.ctrlKey &&
    !event.altKey
  );
}

/**
 * 带优先级分层的动态按键处理器。suggestion 与斜杠命令插件拥有优先否决权，
 * 而发送动作仍抢在编辑器基础键位之前执行。
 */
export function createSubmitShortcutExtension(
  onSubmit: () => boolean,
) {
  return Extension.create({
    name: "submitShortcut",
    // Mention/斜杠插件用的是 101，让已打开的弹层先否决。
    // 编辑器默认键位是 100；反向扩展顺序让这个后注册的处理器排在它们
    // 前面，同时保住弹层的优先级。
    priority: 100,
    addProseMirrorPlugins() {
      const editor = this.editor;
      let replayingEnter = false;
      return [
        new Plugin({
          props: {
            handleKeyDown(view, event) {
              if (replayingEnter) return false;
              const shortcut = getShortcut("send");

              if (
                shouldReplayNativeEnter(event, shortcut, view.composing)
              ) {
                replayingEnter = true;
                try {
                  return editor.commands.keyboardShortcut("Enter");
                } finally {
                  replayingEnter = false;
                }
              }

              if (
                !shouldHandleSubmitShortcut(event, {
                  configuredShortcut: shortcut,
                  composing: view.composing,
                })
              ) {
                return false;
              }
              return onSubmit();
            },
          },
        }),
      ];
    },
  });
}
