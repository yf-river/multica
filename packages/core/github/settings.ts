// 本文件定义 GitHub 集成的特性开关结构，以及从工作区 settings JSONB 中
// 推导这些开关的纯函数。

import type { Workspace } from "../types";

/** GitHub 集成的各项特性开关。所有子开关都以主开关 `enabled` 为前提。 */
export interface GitHubSettings {
  /** 总开关。为 false 时所有 UI 入口和副作用一律关闭。 */
  enabled: boolean;
  /** issue 详情里的 PR 侧边栏是否可见。隐含 `enabled`。 */
  prSidebar: boolean;
  /** agent 提交里是否追加 Co-authored-by 尾注。隐含 `enabled`。 */
  coAuthor: boolean;
  /** 是否根据 webhook 负载自动关联 issue ↔ PR。隐含 `enabled`。 */
  autoLinkPRs: boolean;
}

/**
 * 从工作区的 settings JSONB 纯推导各开关。每个开关都默认「开」，这样早于
 * MUL-2414 创建的工作区继续保持历史上「全开」的行为。
 *
 * @param workspace - 当前工作区对象（可为空）
 * @returns 推导出的 GitHub 设置
 */
export function deriveGitHubSettings(
  workspace: Pick<Workspace, "settings"> | null | undefined,
): GitHubSettings {
  const s = (workspace?.settings ?? {}) as Record<string, unknown>;
  const enabled = s.github_enabled !== false;
  return {
    enabled,
    prSidebar: enabled && s.github_pr_sidebar_enabled !== false,
    coAuthor: enabled && s.co_authored_by_enabled !== false,
    autoLinkPRs: enabled && s.github_auto_link_prs_enabled !== false,
  };
}
