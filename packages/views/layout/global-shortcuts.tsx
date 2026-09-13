"use client";

import { useEffect } from "react";
import { useSidebar } from "@multica/ui/components/ui/sidebar";
import {
  getShortcut,
  isEditableShortcutTarget,
  shortcutMatchesEvent,
  SHORTCUT_ACTION_BY_ID,
  useShortcutStore,
  type ShortcutActionId,
} from "@multica/core/shortcuts";
import { useChatStore } from "@multica/core/chat";
import { openCreateIssueWithPreference } from "@multica/core/issues/stores";
import { useModalStore } from "@multica/core/modals";
import { useWorkspacePaths } from "@multica/core/paths";
import { isImeComposing } from "@multica/core/utils";
import { isFloatingChatRouteSuppressed } from "../chat/floating-chat-visibility";
import { useNavigation } from "../navigation";
import { useSearchStore } from "../search/search-store";

/**
 * 布局模块：仪表盘外壳内的全局快捷键处理。
 * 挂载一个文档级 keydown 监听器，执行搜索、创建 issue、切换侧边栏/聊天、
 * 跳转到各页面等产品级快捷键动作。
 */

/** 需要注册为全局快捷键的动作 id 列表。 */
const GLOBAL_ACTIONS: readonly ShortcutActionId[] = [
  "openSearch",
  "createIssue",
  "toggleSidebar",
  "toggleChat",
  "goBack",
  "goForward",
  "goInbox",
  "goChat",
  "goMyIssues",
  "goIssues",
  "goProjects",
  "goAutopilots",
  "goAgents",
  "goSquads",
  "goUsage",
  "goRuntimes",
  "goSkills",
  "goSettings",
];

/**
 * 判断是否应忽略某个全局快捷键事件。
 * 已被 preventDefault、按键重复、或正在输入法组字时都应忽略。
 * @param event 键盘事件。
 * @returns 是否忽略。
 */
export function shouldIgnoreGlobalShortcutEvent(event: KeyboardEvent): boolean {
  return event.defaultPrevented || event.repeat || isImeComposing(event);
}

/** 在仪表盘外壳内执行可配置的产品级快捷键。渲染为空，只挂监听器。 */
export function GlobalShortcuts() {
  const { toggleSidebar } = useSidebar();
  const navigation = useNavigation();
  const workspacePaths = useWorkspacePaths();

  // 订阅覆盖配置，这样在设置里改绑定能立刻刷新监听器闭包；
  // getShortcut 对非 React 调用点仍有用。
  const overrides = useShortcutStore((state) => state.overrides);

  useEffect(() => {
    const chatPath = workspacePaths.chat();
    const destinations: Partial<Record<ShortcutActionId, string>> = {
      goInbox: workspacePaths.inbox(),
      goChat: chatPath,
      goMyIssues: workspacePaths.myIssues(),
      goIssues: workspacePaths.issues(),
      goProjects: workspacePaths.projects(),
      goAutopilots: workspacePaths.autopilots(),
      goAgents: workspacePaths.agents(),
      goSquads: workspacePaths.squads(),
      goUsage: workspacePaths.usage(),
      goRuntimes: workspacePaths.runtimes(),
      goSkills: workspacePaths.skills(),
      goSettings: workspacePaths.settings(),
    };

    // 在按下时读取而不是订阅：这个偏好只在组合键触发的瞬间起作用，而且
    // 浮层在 Chat 标签页里并不存在。不可用的浮层也不该占用这个组合键——
    // 从查找器返回 false 可以让按键保留它在应用之外的含义，而不是为了一个
    // 肉眼可见毫无效果的动作把它吞掉。
    const canToggleFloatingChat = () =>
      useChatStore.getState().floatingChatEnabled &&
      !isFloatingChatRouteSuppressed(navigation.pathname, chatPath);

    const handleKeyDown = (event: KeyboardEvent) => {
      // 组件/编辑器的处理器在这个文档级监听器之前运行。尊重它们的
      // preventDefault，避免在焦点控件已消费同一组合键后，再次触发产品动作。
      if (shouldIgnoreGlobalShortcutEvent(event)) return;

      const actionId = GLOBAL_ACTIONS.find((candidate) => {
        const action = SHORTCUT_ACTION_BY_ID[candidate];
        if (!action.allowInEditable && isEditableShortcutTarget(event.target)) {
          return false;
        }
        if (candidate === "toggleChat" && !canToggleFloatingChat()) return false;
        return shortcutMatchesEvent(getShortcut(candidate), event);
      });
      if (!actionId) return;

      event.preventDefault();
      if (actionId === "openSearch") {
        useSearchStore.getState().toggle();
        return;
      }
      if (actionId === "toggleChat") {
        useChatStore.getState().toggle();
        return;
      }
      if (actionId === "toggleSidebar") {
        toggleSidebar();
        return;
      }
      if (actionId === "goBack") {
        navigation.back();
        return;
      }
      if (actionId === "goForward") {
        // 在适配器上是可选的：没有前进栈的独立窗口会把它留成 undefined，
        // 此时该组合键只是空操作。
        navigation.forward?.();
        return;
      }
      if (actionId === "createIssue") {
        if (useModalStore.getState().modal) return;
        const projectMatch = navigation.pathname.match(
          /^\/[^/]+\/projects\/([^/]+)$/,
        );
        const data = projectMatch
          ? { project_id: projectMatch[1] }
          : undefined;
        openCreateIssueWithPreference(data);
        return;
      }

      const destination = destinations[actionId];
      if (destination && destination !== navigation.pathname) {
        navigation.push(destination);
      }
    };

    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [navigation, overrides, toggleSidebar, workspacePaths]);

  return null;
}
