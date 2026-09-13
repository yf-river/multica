"use client";

// 吸底评论输入框判断 hook：决定底部评论输入框是否要固定（sticky）在滚动视口上。
// 综合用户的偏好设置和当前屏幕宽度——窄屏下不固定，让输入框随时间线滚到末尾。
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { useCommentComposerStore } from "@multica/core/issues/stores";

/**
 * 底部评论输入框是否固定（sticky）在滚动视口上。
 *
 * 用户存的偏好（设置 → 偏好）是对「屏幕空间充裕」时的回答。窄视口不是那种屏幕：
 *
 *  - 聊天启动器占据每个工作区页面的右下角（见 `--chat-launcher-clearance`）。
 *    固定输入框会一直压在它下面，被它盖住的恰好是发送按钮。
 *  - 固定状态下，输入框会永久占用手机视口约 15% 的高度——而那是个人们
 *    多半在读、很少在写的界面。
 *
 * 所以小于 `md` 断点时输入框改为贴着时间线末尾，而手机读者本来就滑到底部去评论。
 * 偏好本身不会被改写——一旦视口足够宽，它立刻重新生效。
 *
 * 输入框和它的宿主都读这个判断，因此固定行为和「只在固定时有意义的 40vh 高度上限」
 * 永远不可能互相矛盾。
 */
export function useStickyComposer(): boolean {
  const preferred = useCommentComposerStore((s) => s.sticky);
  const isMobile = useIsMobile();
  return preferred && !isMobile;
}
