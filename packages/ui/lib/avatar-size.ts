// 本文件定义「语义化」的头像尺寸档位：使用者按角色场景选档（行内小图/列表行/
// 区块标题/大图），而不是直接写像素数，保证同一「角色」在任何地方渲染成一致尺寸。

/**
 * 档位与 4px 网格对齐（对应 Tailwind 的 size-4/5/6/8/10/14）。像素直径是
 * 任何仍需具体尺寸的地方（字号缩放、头像重叠间距、在线点阈值等）的唯一事实来源。
 */
export type AvatarSize = "xs" | "sm" | "md" | "lg" | "xl" | "2xl";

/** 每个语义档位对应的像素直径 */
export const AVATAR_SIZE_PX: Record<AvatarSize, number> = {
  xs: 16,
  sm: 20,
  md: 24,
  lg: 32,
  xl: 40,
  "2xl": 56,
};

/**
 * 未显式指定尺寸时的默认档位。与基础头像的历史 20px 默认一致，
 * 让没传 size 的旧调用点行为不变。
 */
export const DEFAULT_AVATAR_SIZE: AvatarSize = "sm";
