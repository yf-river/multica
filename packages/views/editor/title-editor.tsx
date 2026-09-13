"use client";

/**
 * TitleEditor——单行标题编辑器（issue 标题等）。
 *
 * 极简 Tiptap 配置：文档只允许一个段落，因此 Enter 不会换行（回车=提交、
 * Esc=失焦，见下方 keymap）。支持把提交快捷键（配置的 send 组合键）接到
 * 显式发送路径、外部 defaultValue 同步（含「聚焦后脏状态保护」），并以
 * `immediatelyRender: false` 把创建推迟到首帧之后，供只读优先宿主使用。
 */

import { forwardRef, useEffect, useImperativeHandle, useRef } from "react";
import { useEditor, EditorContent } from "@tiptap/react";
import { Extension } from "@tiptap/core";
import { Document } from "@tiptap/extension-document";
import { Paragraph } from "@tiptap/extension-paragraph";
import { Text } from "@tiptap/extension-text";
import Placeholder from "@tiptap/extension-placeholder";
import {
  getShortcut,
  isPlainShortcut,
  type ShortcutChord,
} from "@multica/core/shortcuts";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { createSubmitShortcutExtension } from "./extensions/submit-shortcut";
import "./title-editor.css";

// ---------------------------------------------------------------------------
// 类型
// ---------------------------------------------------------------------------

interface TitleEditorProps {
  defaultValue?: string;
  placeholder?: string;
  className?: string;
  autoFocus?: boolean;
  onSubmit?: () => void;
  /**
   * 在配置的 `send` 组合键上触发，与 `onSubmit` 的纯 Enter 路径相互独立。
   * 纯 Enter 提交的宿主传 `onSubmit`；想要显式组合键的宿主（create-issue，
   * MUL-4931）改传它。
   *
   * 纯 Enter 刻意永不在标题里作为触发键，即使 `send` 被配置成纯 Enter：
   * 下面的 keymap 已拥有那个键（「结束编辑」），而这个单行编辑器没有换行
   * 可做交换。遮蔽它会在标题只打了一半时就静默创建——正是 #5532 干掉
   * 的误触发。
   */
  onSubmitShortcut?: () => void;
  onBlur?: (value: string) => void;
  onChange?: (value: string) => void;
  /**
   * Tiptap 实例存在且其 DOM 已挂接时调用一次（`immediatelyRender: false`
   * 把创建推迟到首帧之后）。契约同 ContentEditorProps.onReady。
   */
  onReady?: () => void;
}

interface TitleEditorRef {
  getText: () => string;
  focus: () => void;
  /**
   * 聚焦并把光标放到给定视口坐标下的文档位置——与
   * ContentEditorRef.focusAtCoords 同一契约，让只读优先宿主（useLazyEditor）
   * 能一视同仁地对待两种编辑器。必须在编辑器元素已布局（非 display:none）
   * 时调用。
   */
  focusAtCoords: (coords: { x: number; y: number }) => void;
}

// ---------------------------------------------------------------------------
// 单段落文档——阻止 Enter 产生新行
// ---------------------------------------------------------------------------

const SingleLineDocument = Document.extend({
  content: "paragraph",
});

// ---------------------------------------------------------------------------
// 快捷键：Enter → 提交，Escape → 失焦
// ---------------------------------------------------------------------------

/**
 * `onSubmitShortcut` 是否可以为配置的 `send` 组合键触发。
 *
 * 纯 Enter 刻意被排除。下面的 keymap 已经拥有那个键（「结束编辑」），而
 * 与散文编辑器不同，单行标题没有可与之交换的换行——所以把 `send` 配成纯
 * Enter 会把每次回车都变成「从半截标题创建」。那正是 #5532 移除的走火，
 * 因此标题保持纯 Enter 惰性，只认显式组合键。想要纯 Enter 提交的宿主仍传
 * `onSubmit`。
 */
export function titleShortcutSubmitAllowed(
  sendShortcut: ShortcutChord | null,
): boolean {
  if (!sendShortcut) return false;
  return !isPlainShortcut(sendShortcut, "Enter");
}

/** 标题专用 keymap 扩展（提交/吞换行/失焦）。 */
function createTitleKeymap(opts: {
  onSubmitRef: React.RefObject<(() => void) | undefined>;
}) {
  return Extension.create({
    name: "titleKeymap",
    addKeyboardShortcuts() {
      return {
        Enter: ({ editor }) => {
          opts.onSubmitRef.current?.();
          editor.commands.blur();
          return true;
        },
        "Shift-Enter": () => true, // 吞掉——不允许换行
        Escape: ({ editor }) => {
          editor.commands.blur();
          return true;
        },
      };
    },
  });
}

// ---------------------------------------------------------------------------
// 组件
// ---------------------------------------------------------------------------

/**
 * TitleEditor——单行标题输入框组件（forwardRef 暴露命令式句柄）。
 */
const TitleEditor = forwardRef<TitleEditorRef, TitleEditorProps>(
  function TitleEditor(
    {
      defaultValue = "",
      placeholder: placeholderText = "",
      className,
      autoFocus = false,
      onSubmit,
      onSubmitShortcut,
      onBlur,
      onChange,
      onReady,
    },
    ref,
  ) {
    const { t } = useT("editor");
    const onSubmitRef = useRef(onSubmit);
    const onSubmitShortcutRef = useRef(onSubmitShortcut);
    const onBlurRef = useRef(onBlur);
    const onChangeRef = useRef(onChange);
    const onReadyRef = useRef(onReady);

    onSubmitRef.current = onSubmit;
    onSubmitShortcutRef.current = onSubmitShortcut;
    onBlurRef.current = onBlur;
    onChangeRef.current = onChange;
    onReadyRef.current = onReady;

    // `useEditor` 只在挂载时读一次 `extensions`，且没有宿主要求在生命周期内
    // 切换该 prop，所以钉住挂载时刻的答案，而不是让扩展列表依赖「碰巧创建
    // 了编辑器的那次渲染」。
    const shortcutSubmitEnabled = useRef(onSubmitShortcut !== undefined).current;

    const editor = useEditor({
      immediatelyRender: false,
      content: defaultValue
        ? { type: "doc", content: [{ type: "paragraph", content: [{ type: "text", text: defaultValue }] }] }
        : "",
      extensions: [
        SingleLineDocument,
        Paragraph,
        Text,
        Placeholder.configure({
          placeholder: placeholderText,
          showOnlyCurrent: false,
        }),
        createTitleKeymap({ onSubmitRef }),
        // 放最后一个，让它上面的 keymap ProseMirror 插件排在更前面——与
        // ContentEditor 扩展列表相同的布位。它一并带来配置的 `send` 组合键、
        // IME 守卫与连发保护，这些都是手写 keydown 不会有的。
        ...(shortcutSubmitEnabled
          ? [
              createSubmitShortcutExtension(() => {
                const fn = onSubmitShortcutRef.current;
                if (!fn) return false;
                if (!titleShortcutSubmitAllowed(getShortcut("send"))) return false;
                fn();
                return true;
              }),
            ]
          : []),
      ],
      editorProps: {
        attributes: {
          class: cn("title-editor outline-none", className),
          role: "textbox",
          "aria-multiline": "false",
          "aria-label": placeholderText || t(($) => $.title_editor.title_aria_label),
        },
      },
      onUpdate: ({ editor: ed }) => {
        onChangeRef.current?.(ed.getText());
      },
      onBlur: ({ editor: ed }) => {
        onBlurRef.current?.(ed.getText());
      },
    });

    // 通知只读优先宿主：延迟创建的编辑器现已存在。从被动 effect 触发，让它
    // 跑在「编辑器 DOM 已挂接」的 commit 之后——与 ContentEditor 同一模式。
    const readyFiredRef = useRef(false);
    useEffect(() => {
      if (!editor || readyFiredRef.current) return;
      readyFiredRef.current = true;
      onReadyRef.current?.();
    }, [editor]);

    // 挂载后自动聚焦——延迟以等待 Dialog 打开动画
    useEffect(() => {
      if (autoFocus && editor) {
        const timer = setTimeout(() => {
          editor.commands.focus("end");
        }, 50);
        return () => clearTimeout(timer);
      }
      return undefined;
    }, [autoFocus, editor]);

    // 记录最后一次已对账的 `defaultValue`，好区分「聚焦 + 脏」（用户敲了
    // 与外部值不同的内容）与「聚焦 + 干净」（用户只是点进来没输入）。
    const lastDefaultValueRef = useRef(defaultValue);

    // 把外部 `defaultValue` 变化同步进编辑器。
    // Tiptap `useEditor` 只在挂载时消费 `content`，不然 WS 驱动的标题更新会
    // 让编辑器一直显示过期文本——而下一次 blur 会借 onBlur 的「值与 issue.title
    // 比较」静默把外部变化滚回去。
    useEffect(() => {
      if (!editor || editor.isDestroyed) return;
      const prevDefaultValue = lastDefaultValueRef.current;
      lastDefaultValueRef.current = defaultValue;

      // 已经同步——无事可做。
      if (editor.getText() === defaultValue) return;

      // 聚焦 + 脏：编辑器文本偏离上一个外部值，说明用户本会话敲过字。
      // 保留输入。聚焦 + 干净（文本仍等于上一个 defaultValue）则继续往下走，
      // 接受新外部值，而不是让下一次 blur 把它滚回去。
      if (editor.isFocused && editor.getText() !== prevDefaultValue) return;

      editor.commands.setContent(
        defaultValue
          ? {
              type: "doc",
              content: [
                { type: "paragraph", content: [{ type: "text", text: defaultValue }] },
              ],
            }
          : "",
        { emitUpdate: false },
      );
    }, [defaultValue, editor]);

    useImperativeHandle(ref, () => ({
      getText: () => editor?.getText() ?? "",
      focus: () => {
        editor?.commands.focus("end");
      },
      focusAtCoords: (coords: { x: number; y: number }) => {
        if (!editor) return;
        const pos = editor.view.posAtCoords({ left: coords.x, top: coords.y });
        if (pos) editor.commands.focus(pos.pos);
        else editor.commands.focus("end");
      },
    }));

    if (!editor) return null;

    return <EditorContent editor={editor} />;
  },
);

/** 导出 TitleEditor 及其类型。 */
export { TitleEditor, type TitleEditorProps, type TitleEditorRef };
