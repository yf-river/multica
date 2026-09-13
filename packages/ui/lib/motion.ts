// 本文件集中定义 UI 动效参数：缓动曲线与时长档位，供动画/过渡统一取用。

/** 统一的「先快后慢」出缓动曲线（配合 framer-motion 的 cubic-bezier 使用） */
export const UI_EASE_OUT = [0.23, 1, 0.32, 1] as const;

/** 统一动画时长档位（秒）：micro 极短 / fast 快速 / standard 常规 */
export const UI_MOTION_DURATION = {
  micro: 0.1,
  fast: 0.15,
  standard: 0.2,
} as const;
