// 悬浮聊天浮层的挂载点：决定 FAB + 聊天窗口是否存在，并在各应用外壳的 dashboard 布局中渲染一次。
"use client";

import { useChatStore } from "@multica/core/chat";
import { useWorkspacePaths } from "@multica/core/paths";
import { useNavigation } from "../navigation";
import { ChatFab } from "./components/chat-fab";
import { ChatWindow } from "./components/chat-window";
import { isFloatingChatRouteSuppressed } from "./floating-chat-visibility";

/**
 * 悬浮聊天浮层（FAB + 窗口）的挂载点。在每个应用外壳的 dashboard 布局中渲染一次；
 * 它持有两个“闸门”，决定这个浮层到底存不存在：
 *
 *  1. 设置 → 聊天 里的偏好开关（`floatingChatEnabled`）。当用户关掉悬浮窗口后，
 *     聊天只存在于它的专属标签页里。
 *  2. 聊天标签页路由本身。在 `/:slug/chat` 上，整页界面已经接管了对话，
 *     所以同一个 `activeSessionId` 的悬浮副本纯属重复——在那里隐藏它。
 */
export function FloatingChat() {
  const enabled = useChatStore((s) => s.floatingChatEnabled);
  const { pathname } = useNavigation();
  const wsPaths = useWorkspacePaths();

  if (!enabled) return null;
  // 在聊天标签页上抑制显示——那里已经整页渲染了同一个对话。
  if (isFloatingChatRouteSuppressed(pathname, wsPaths.chat())) return null;

  return (
    <>
      <ChatWindow />
      <ChatFab />
    </>
  );
}
