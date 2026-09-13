// 本文件定义 onboarding 引导步骤的规范顺序，是「哪一步在下一步之前」的唯一
// 真源。

import type { OnboardingStep } from "./types";

/**
 * 已持久化引导步骤的规范顺序。
 *
 * 「哪一步在下一步之前」的唯一真源——UI 的进度指示器靠它计算「当前步的
 * 下标」和「总步数」。增删、重排、移除某一步只需改这个数组；所有读它的
 * 调用点都会自动跟着变。
 *
 * 刻意不包含 "welcome"：welcome 是首次进入的产品介绍，不是持久化的步骤。
 * 进度指示器也不为它显示，理由相同——不该让用户把「读介绍」也算成「在完成
 * 设置上往前走」。
 *
 * 有两个问题刻意不再作为独立步骤：
 *
 *   - "source"（你是怎么知道 Multica 的？）是纯归因数据，对用户零回报，
 *     所以不再占用关键路径。它改在引导后由工作区的 source-backfill 弹窗
 *     收集，而且只在 agent 为用户完成过工作之后——见 `needs-backfill.ts`。
 *   - "role" / "use_case" 合并进单一的 "about_you" 步骤，让相关的个性化
 *     输入待在一起。
 *
 * Runtime 是最后一个表单步骤。连接了 runtime 的路径会在 runtime 步骤的提交
 * 动作里一并「创建 Mika 并打开交互式引导聊天」——那个聊天本身就是产品体验，
 * 而不是又一个进度屏上的步骤。
 */
export const ONBOARDING_STEP_ORDER: readonly OnboardingStep[] = [
  "about_you",
  "workspace",
  "runtime",
] as const;
