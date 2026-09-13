// 本文件提供读取当前工作区 GitHub 特性开关的 React Hook。

"use client";

import { useMemo } from "react";
import { useCurrentWorkspace } from "../paths";
import { deriveGitHubSettings, type GitHubSettings } from "./settings";

/**
 * 读取当前工作区 settings JSONB 里的 GitHub 特性开关。
 *
 * 下游组件应该用这个 hook，而不是直接戳 `workspace.settings`，这样才能保证
 * 每个开关的兜底语义（见 deriveGitHubSettings）处处一致。
 *
 * @returns 当前工作区的 GitHub 设置
 */
export function useGitHubSettings(): GitHubSettings {
  const workspace = useCurrentWorkspace();
  return useMemo(() => deriveGitHubSettings(workspace), [workspace]);
}
