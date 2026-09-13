// 本文件提供 GitHub PR 侧边栏所需的三组「纯推导」逻辑：
// CI 状态、可合并性、改动统计，输入都来自 GitHub API 快照。
//
// PR 侧边栏一行要呈现两个相互独立的事实，各自都是三态、各自源自 GitHub API
// 快照：
//
//   1. CI 状态   —— 由 `checks_rollup`（主）与各计数推导。
//   2. 可合并性  —— 由 `mergeable` + `merge_state_status` 推导。
//
// 两者刻意解耦：一个 PR 可以同时「检查失败」又「有合并冲突」，两个都要展示；
// 谁也不是从另一个推导出来的。对于已终结的 PR（merged / closed），两个元素
// 都不展示——行首的状态图标已经表达了终结状态，由调用方负责加这道闸。
//
// 所有输入字段都是可选的，因为老后端会缺省快照字段；每条规则都做防御性兜底
// （`?? 0`、`?? []`、显式 `=== "..."` 判断），缺字段时绝不凭空捏造一个「通过」。

import type {
  GitHubPullRequestChecksConclusion,
  GitHubPullRequestChecksRollup,
  GitHubPullRequestMergeable,
  GitHubPullRequestMergeStateStatus,
} from "../types";

// ---------------------------------------------------------------------------
// CI 状态
// ---------------------------------------------------------------------------

// CI 元素的可辨识联合。`none` 表示当前快照里没有任何检查；`unavailable` 表示
// 根本没有可供渲染的当前快照区域。
export type PullRequestChecksStatus =
  | { kind: "failed"; failed: number; total: number; names: string[] }
  | { kind: "pending"; passed: number; total: number; running: number }
  | { kind: "passed"; total: number }
  | { kind: "none" }
  | { kind: "unavailable" };

/** 推导 CI 状态所需的输入，全部来自 GitHub API 快照字段。 */
export interface PullRequestChecksInput {
  snapshot_available?: boolean;
  checks_rollup?: GitHubPullRequestChecksRollup | null;
  checks_conclusion?: GitHubPullRequestChecksConclusion | null;
  checks_total?: number;
  checks_passed?: number;
  checks_failed?: number;
  checks_running?: number;
  checks_pending?: number;
  failed_check_names?: string[];
}

// 优先级（高 → 低）：
//   0. 明确声明快照不可用                   → unavailable
//   1. rollup 为 failure/error，或失败数 > 0 → failed
//   2. rollup 为 pending/expected            → pending
//   3. rollup 为 success                     → passed
//   4. 老式 provider 的粗粒度结论            → 对应粗粒度状态
//   5. 快照存在但 rollup 为空                → none（“还没有检查”）
//   6. 其他                                  → unavailable
//
// 判定失败时同时信任计数和 rollup，这样即使已知的遗留失败其粗粒度结论滞后，
// 也能被呈现出来。显式的「快照不可用」闸门永远优先，被禁用或头不对的 GitHub
// 数据永远不会漏过去。
export function deriveChecksStatus(input: PullRequestChecksInput): PullRequestChecksStatus {
  if (input.snapshot_available === false) {
    return { kind: "unavailable" };
  }
  const rollup = input.checks_rollup ?? null;
  const total = input.checks_total ?? 0;
  const passed = input.checks_passed ?? 0;
  const failed = input.checks_failed ?? 0;
  const running = input.checks_running ?? input.checks_pending ?? 0;
  const names = input.failed_check_names ?? [];

  if (rollup === "failure" || rollup === "error" || failed > 0) {
    return { kind: "failed", failed, total, names };
  }
  if (rollup === "pending" || rollup === "expected") {
    return { kind: "pending", passed, total, running };
  }
  if (rollup === "success") {
    return { kind: "passed", total };
  }
  // Forgejo / Gitea / GitLab 和老版本 GitHub 后端暴露的是 webhook 派生的粗粒度
  // 结论，而不是 GraphQL rollup。这里保留它们已知的 passed/pending/failed
  // 状态，同时避免把「没有 API 快照」误当成当前的「没有检查」。
  if (input.checks_conclusion === "failed") {
    return { kind: "failed", failed, total, names };
  }
  if (input.checks_conclusion === "pending") {
    return { kind: "pending", passed, total, running };
  }
  if (input.checks_conclusion === "passed") {
    return { kind: "passed", total };
  }
  return input.snapshot_available === true ? { kind: "none" } : { kind: "unavailable" };
}

// ---------------------------------------------------------------------------
// 可合并性
// ---------------------------------------------------------------------------

// 可合并性元素的可辨识联合。`none` 表示不渲染任何内容：当 GitHub 还没有裁决
// （mergeable 未知/为空，且没有决定性的 merge_state_status）时，卡片既不宣称
// 「冲突」也不宣称「可合并」。
export type PullRequestMergeStatus =
  | { kind: "conflicting" }
  | { kind: "ready" }
  | { kind: "blocked" }
  | { kind: "behind" }
  | { kind: "unstable" }
  | { kind: "has_hooks" }
  | { kind: "none" };

/** 推导可合并性所需的输入，全部来自 GitHub API 快照字段。 */
export interface PullRequestMergeInput {
  snapshot_available?: boolean;
  mergeable?: GitHubPullRequestMergeable | null;
  merge_state_status?: GitHubPullRequestMergeStateStatus | null;
}

// 优先级（高 → 低）：
//   1. mergeable 为 conflicting，或 merge_state 为 dirty → conflicting
//   2. merge_state 为 clean                             → ready
//   3. merge_state 为 blocked/behind/unstable/hooks     → 如实标注
//   4. 其他                                             → none（不渲染）
//
// `mergeable` 只回答「有没有冲突」；`merge_state_status === dirty` 是 GitHub
// 对同一事实的另一种看法（不可合并的冲突），所以两者都映射成 `conflicting`。
// 「可以合并」只从 `clean` 断定——绝不由 `mergeable === "mergeable"` 推断，
// 因为后者不包含「必需检查」和分支保护等约束。
export function deriveMergeStatus(input: PullRequestMergeInput): PullRequestMergeStatus {
  if (input.snapshot_available === false) return { kind: "none" };
  const mergeable = input.mergeable ?? null;
  const mergeState = input.merge_state_status ?? null;

  if (mergeable === "conflicting" || mergeState === "dirty") return { kind: "conflicting" };
  if (mergeState === "clean") return { kind: "ready" };
  if (mergeState === "blocked") return { kind: "blocked" };
  if (mergeState === "behind") return { kind: "behind" };
  if (mergeState === "unstable") return { kind: "unstable" };
  if (mergeState === "has_hooks") return { kind: "has_hooks" };
  return { kind: "none" };
}

// ---------------------------------------------------------------------------
// 改动统计
// ---------------------------------------------------------------------------

/** 推导改动统计所需的输入。 */
export interface PullRequestStatsInput {
  additions?: number;
  deletions?: number;
  changed_files?: number;
}

// shouldShowPullRequestStats 实现的是「老后端 → 新前端」的守卫：当提供这条
// PR 行的后端还不知道统计列时，所有数字字段都默认为 0。此时渲染 "+0 −0 · 0
// files" 是撒谎（这个 PR 几乎必然有真实改动），所以至少有一个信号非零之前，
// 整条统计行都不显示。
export function shouldShowPullRequestStats(input: PullRequestStatsInput): boolean {
  const a = input.additions ?? 0;
  const d = input.deletions ?? 0;
  const f = input.changed_files ?? 0;
  return a + d + f > 0;
}
