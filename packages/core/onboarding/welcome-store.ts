// 本文件实现「welcome 信号」store：用户跳过 runtime 连接后，把这份瞬时意图
// 从引导页带给随后登场的工作区外壳。

import { create } from "zustand";

/**
 * Welcome 信号——仅在用户跳过连接 runtime 时由 runtime 步骤写入，然后被工作区
 * 外壳里的 `<WelcomeAfterOnboarding />` 组件恰好消费一次。
 *
 * 这个信号是「引导屏」和「接替它的工作区外壳」之间的桥梁。我们进入工作区时
 * `onboarded_at` 已经为 true——它承载的是那份瞬时的「无 runtime」意图，而不
 * 把这份意图持久化到用户行上（v2 设计把它持久化成字段，结果复杂度漏得到处
 * 都是）。
 *
 * 信号只在用户显式跳过 runtime 设置时需要。工作区 welcome hook 会种一条
 * 「安装 runtime」的引导 issue 并在弹窗里展示。一旦出现 runtime，Runtimes 页
 * 就提供与「已连接引导路径」相同的 Mika bootstrap。连了 runtime 的引导在到达
 * 工作区之前就已创建 Mika，不走这个信号。
 */
export interface WelcomeSignal {
  workspaceId: string;
  choice: "skip";
}

/** welcome store 的状态结构。 */
interface WelcomeStoreState {
  signal: WelcomeSignal | null;
  /**
   * 用户显式与 Welcome 界面交互过之后为 true——打开了 runtime 引导或关掉了
   * Skip 弹窗。组件 MUST 以 `signal && !dismissed` 作为渲染条件；`dismiss()`
   * 才是把弹窗从视图上拿掉的动作。
   */
  dismissed: boolean;
  /** 写入一份 welcome 信号（同时重置 dismissed）。 */
  set: (signal: WelcomeSignal) => void;
  /**
   * 把 Welcome 界面标记为「已交互」。幂等。信号仍留在 store 里作记录，但组件
   * 不再渲染它。dismiss 时不清空整个 store，这样调试窥视（或未来某个分析
   * hook）仍能看到用户走的是哪条路。
   */
  dismiss: () => void;
  /**
   * 完全重置——同时清掉 signal 和 dismissed。供测试以及用户登出流程使用
   * （避免另一个用户在同一浏览器登录时继承一份过期的信号）。
   */
  reset: () => void;
}

/**
 * Zustand store，刻意不持久化。刷新页面即丢信号——这是正确行为：用户已经
 * 完成引导（onboarded_at 已设置），Welcome 是一次性 UI。绝不想让一次刷新
 * 重新触发 setup-guide 的创建。
 *
 * 为什么用订阅模型而不是「读一次即消费」：React 18+ 的 StrictMode 开发模式
 * 会把组件挂载两次（mount → unmount → mount）。若组件在 `useState` 初始化器
 * 里消费 store，第二次挂载看到的是已被清空的 store，会渲染 null——Welcome
 * 在开发模式下永远不出现。订阅一个稳定的 store 并把 dismissed 记在 store 里，
 * 能让两次挂载看到同一份状态。
 */
export const useWelcomeStore = create<WelcomeStoreState>((set) => ({
  signal: null,
  dismissed: false,
  set: (signal) => set({ signal, dismissed: false }),
  dismiss: () => set({ dismissed: true }),
  reset: () => set({ signal: null, dismissed: false }),
}));
