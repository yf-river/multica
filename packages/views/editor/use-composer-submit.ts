"use client";

/**
 * 每个 composer 统一的「等待-然后-渲染」悲观提交契约（MUL-5181）。
 *
 * 此前 comment-input、reply-input、comment-card（编辑）、create-issue、
 * quick-create 各自手抄同一套六步「悲观提交」：读 markdown、空/在飞守卫、
 * 重读上传门禁、加锁并转圈、等服务端，成功才清空（失败保留草稿）。各份
 * 抄写逐渐漂移——有的用 ref 做单飞守卫、有的用慢一帧的 state 布尔；有的
 * 提交时重查上传门禁、有的信任禁用按钮。本 hook 把契约集中，让每个界面
 * 行为一致。
 *
 * 它刻意是「等待-然后-渲染」而非乐观更新：composer 保留用户的文本与附件
 * （发送控件锁定并转圈），直到服务端接受提交才清空。慢发送绝不会看起来像
 * 「已发出但输入框还是满的」；被拒绝的发送保留草稿供重试，而不是静默丢弃。
 * 编辑器自身保持可交互（Tiptap 挂载后不能切换 editable），所以调用方的
 * accepted 处理器用提交时刻的快照做守卫：成功只清掉它提交的那份草稿，
 * 请求期间新敲的东西全部幸存。
 *
 * 它还负责发送后的焦点决策（`afterAccepted`）。发送后光标落在哪是每个
 * 界面的产品决定——用户会继续写下去的回复框重新聚焦，顶层评论结束回合、
 * 丢弃光标——但机制各处相同且容易做错（必须在清空后执行、必须躲过对话框
 * 焦点陷阱、不能抢走用户中途移到别处的焦点）。它们在这里只写一次。
 */

import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import type { ContentEditorRef } from "./content-editor";
import type { UploadGate } from "./use-upload-gate";

/**
 * 提交被接受后键盘焦点去向。
 *  - `refocus`: 把光标放回 composer，让用户继续写。
 *  - `blur`: 丢弃光标——发送结束了这个回合，composer 不该再像「还在写」。
 *  - `none`: 焦点停在发送结束时所在的位置。
 */
export type ComposerAfterAccepted = "refocus" | "blur" | "none";

export interface ComposerSubmitOptions {
  editorRef: RefObject<ContentEditorRef | null>;
  uploadGate: UploadGate;
  /**
   * 执行发送。服务端接受提交时 resolve `true`（composer 随后经 `onAccepted`
   * 清空）；resolve `false` 表示保留草稿供重试。抛错按 `false` 处理。
   */
  onSubmit: (content: string) => Promise<boolean>;
  /**
   * 提交被接受后运行一次：清空编辑器、附件与持久化草稿。被拒绝时不运行。
   */
  onAccepted?: () => void;
  /** 在空守卫与 `onSubmit` 之前规范化原始 markdown。 */
  normalize?: (raw: string) => string;
  /**
   * `onAccepted` 清空 composer 后的焦点处理。默认 `none`。
   *
   * 不变量：`onAccepted` 可能拒绝清空的界面必须传一个函数、并在那些路径上
   * resolve 为 `none`。这里的每个 composer 都有「保留请求期间输入文本」的
   * 过期提交守卫——而聊天可以在用户已切到别的会话时即发即忘地发送，共享
   * 编辑器于是落在别人的草稿上。对一个 accepted 处理器刻意没动的文档执行
   * 焦点动作，会把光标从用户还在写的一句话里拽走。字面量模式只在「接受
   * 必然清空编辑器」时才安全。
   */
  afterAccepted?: ComposerAfterAccepted | (() => ComposerAfterAccepted);
  /**
   * composer 的根元素，只被 `refocus` 使用。只有当焦点仍属于本 composer
   * 时才夺回——发送按钮、编辑器自身、或什么都没有。没有它，composer 会把
   * 光标从用户在一次慢发送期间移过去的字段上抢走。省略则无条件重新聚焦。
   */
  containerRef?: RefObject<HTMLElement | null>;
}

export interface ComposerSubmit {
  /** 从提交开始到服务端落定一直为 true。驱动锁定 + 转圈。 */
  submitting: boolean;
  /** 从发送按钮与 Cmd/Ctrl+Enter 快捷键调用。 */
  submit: () => Promise<void>;
}

/** 默认规范化：去掉结尾连续空行后 trim。 */
const defaultNormalize = (raw: string) => raw.replace(/(\n\s*)+$/, "").trim();

/**
 * 统一悲观提交 hook。
 * @param opts 见 ComposerSubmitOptions。
 * @returns 见 ComposerSubmit。
 */
export function useComposerSubmit(opts: ComposerSubmitOptions): ComposerSubmit {
  const [submitting, setSubmitting] = useState(false);
  // 同步单飞。`submitting` 慢一帧渲染，同一 tick 内第二次 Enter 会溜过纯
  // state 守卫导致重复发送。
  const inFlight = useRef(false);
  const optsRef = useRef(opts);
  optsRef.current = opts;
  // 焦点 effect 在接受后一帧才落地，届时本 composer 可能已消失（行内编辑
  // 保存即关闭）——绝不去碰已卸载所有者的 ref，也绝不留一帧排队任务。
  const mountedRef = useRef(true);
  const focusFrameRef = useRef<number | null>(null);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      if (focusFrameRef.current !== null) {
        cancelAnimationFrame(focusFrameRef.current);
        focusFrameRef.current = null;
      }
    };
  }, []);

  const runAfterAccepted = useCallback(() => {
    const o = optsRef.current;
    // 现在、而不是在帧内解析模式：调用方从「接受刚落定」的状态做决定
    // （聊天里「我们是否真的清了共享编辑器」的标记），再晚一帧那个状态
    // 可能已经变了。
    const mode =
      typeof o.afterAccepted === "function"
        ? o.afterAccepted()
        : (o.afterAccepted ?? "none");
    if (mode === "none") return;
    // 推迟一帧。`onAccepted` 刚清空编辑器与草稿，对话框里的 composer 还在
    // 与焦点陷阱竞争——若我们同步夺焦，陷阱会在下一 tick 把焦点弹回第一个
    // 可聚焦的头部控件。
    if (focusFrameRef.current !== null) cancelAnimationFrame(focusFrameRef.current);
    focusFrameRef.current = requestAnimationFrame(() => {
      focusFrameRef.current = null;
      if (!mountedRef.current) return;
      if (mode === "blur") {
        optsRef.current.editorRef.current?.blur();
        return;
      }
      const container = optsRef.current.containerRef?.current;
      const active = typeof document === "undefined" ? null : document.activeElement;
      // `body` 表示清空动作已经丢掉了焦点——那仍算「我们的」。
      const ownsFocus =
        !container || !active || active === document.body || container.contains(active);
      if (!ownsFocus) return;
      optsRef.current.editorRef.current?.focus();
    });
  }, []);

  const submit = useCallback(async () => {
    const o = optsRef.current;
    const raw = o.editorRef.current?.getMarkdown() ?? "";
    const content = (o.normalize ?? defaultNormalize)(raw);
    if (!content || inFlight.current) return;
    // 提交时重查上传状态：禁用按钮慢一帧，而 Cmd+Enter / 标题栏 Enter
    // 完全绕过按钮。
    if (o.uploadGate.isBlocked()) return;

    inFlight.current = true;
    setSubmitting(true);
    try {
      const accepted = await o.onSubmit(content);
      if (accepted) {
        o.onAccepted?.();
        runAfterAccepted();
      }
    } catch {
      // 抛错的发送按拒绝处理：保留草稿，让调用方的 onSubmit 自己弹错误
      // toast。
    } finally {
      inFlight.current = false;
      setSubmitting(false);
    }
  }, [runAfterAccepted]);

  return { submitting, submit };
}
