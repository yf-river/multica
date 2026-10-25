// 悬浮聊天窗口的输入框聚焦控制：用一个"nonce"（每次 +1 的信号）通知 ChatInput
// 该把键盘焦点拉进编辑器了，覆盖"该输入新内容了"的各种时刻。
"use client";

import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";

/**
 * 管理悬浮窗口输入框的聚焦 nonce。
 *
 * `focusRequest` 交给 ChatInput：每次数字变化它就把键盘焦点拉进编辑器，
 * `0` 表示惰性（不聚焦）。调用方在"你即将输入新内容"的时刻把它加一——
 * 例如新建聊天、切换 agent、切换项目上下文。
 *
 * 打开悬浮窗口正是这样的时刻之一（MUL-5522）：切换快捷键的整个意义就在于
 * 不用鼠标就能进聊天，如果落到一个还得再点一下的窗口里就白费了——
 * 从 FAB 打开时也同样照顾到这一点。
 *
 * ref 用挂载时的值做种子，因此只有真正的"关闭 → 打开"切换才会触发聚焦。
 * ChatWindow 关闭时仍保持挂载，`isOpen` 又是从存储恢复的；如果把"挂载"当成
 * "打开"，持久化的"已打开"偏好就会在任何页面加载时抢走用户的焦点。
 */
export function useChatInputFocus(isOpen: boolean, windowRef?: RefObject<HTMLElement | null>): {
  focusRequest: number;
  requestInputFocus: () => void;
} {
  const [focusRequest, setFocusRequest] = useState(0);
  const requestInputFocus = useCallback(() => setFocusRequest((n) => n + 1), []);

  // The window stays mounted to preserve drafts and its closing animation.
  // Release its focus before the next keystroke can enter a hidden composer.
  // Do not blur a dialog or page control the user has already moved to.
  useLayoutEffect(() => {
    if (isOpen) return;
    const container = windowRef?.current;
    const active = container?.ownerDocument.activeElement;
    if (active instanceof HTMLElement && container?.contains(active)) active.blur();
  }, [isOpen, windowRef]);

  const wasOpenRef = useRef(isOpen);
  useEffect(() => {
    const wasOpen = wasOpenRef.current;
    wasOpenRef.current = isOpen;
    if (isOpen && !wasOpen) requestInputFocus();
  }, [isOpen, requestInputFocus]);

  return { focusRequest, requestInputFocus };
}
