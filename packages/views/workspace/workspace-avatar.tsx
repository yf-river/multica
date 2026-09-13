/**
 * workspace 模块：工作区头像。
 * 有自定义头像时渲染图片，否则回退成带名称首字母的圆形占位。
 */

import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { cn } from "@multica/ui/lib/utils";

/** 不同尺寸对应的样式映射。 */
const sizeMap = {
  sm: "h-5 w-5 text-caption rounded-full",
  md: "h-7 w-7 text-caption rounded-full",
  lg: "h-9 w-9 text-body rounded-full",
} as const;

/** 工作区头像组件的属性。 */
interface WorkspaceAvatarProps {
  name: string;
  avatarUrl?: string | null;
  size?: keyof typeof sizeMap;
  className?: string;
}

/**
 * 工作区头像：优先展示图片；没有设置头像时用首字母占位。
 */
function WorkspaceAvatar({ name, avatarUrl, size = "sm", className }: WorkspaceAvatarProps) {
  const resolvedUrl = resolvePublicFileUrl(avatarUrl);
  if (resolvedUrl) {
    return (
      <img
        src={resolvedUrl}
        alt={name}
        className={cn("inline-block shrink-0 border object-cover", sizeMap[size], className)}
      />
    );
  }
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center justify-center border bg-muted font-semibold text-muted-foreground",
        sizeMap[size],
        className
      )}
    >
      {name.charAt(0).toUpperCase()}
    </span>
  );
}

export { WorkspaceAvatar, type WorkspaceAvatarProps };
