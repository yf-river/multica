"use client";

// 本文件提供一个重叠排列的 Agent 头像组，用于在紧凑空间里一次展示多个 Agent。

import { ActorAvatar as ActorAvatarBase } from "@multica/ui/components/common/actor-avatar";
import { AVATAR_SIZE_PX, type AvatarSize } from "@multica/ui/lib/avatar-size";
import { useActorName } from "@multica/core/workspace/hooks";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

interface AgentAvatarStackProps {
  // 要渲染的 Agent id，按展示顺序传入。组件本身不去重——调用方需要传
  // 一份去重后的列表（在上游用 `new Set(...)` 处理过）。
  agentIds: readonly string[];
  // 语义化的头像档位。头像之间重叠约 30%，因此可见间距会随尺寸自然缩放。
  // 默认值对应紧凑工具栏 / 卡片角落的密度（sm / 20px）。
  size?: AvatarSize;
  // 最多显示多少个头像，超出部分折叠成 `+N` 的角标。计划默认是 3 个，
  // 再多视觉上就会拥挤。
  max?: number;
  // `half` 会把整体不透明度降到 50%。由 IssueAgentActivityIndicator 用来
  // 表示“只有排队、没有正在运行”的状态——同一批头像，弱化的视觉。
  opacity?: "full" | "half";
  className?: string;
}

/**
 * 重叠排列的 Agent 头像组。纯展示组件——不做数据请求、不处理悬浮。
 * 需要展示每个 Agent 详情时，在上游用 HoverCardTrigger 包一层
 * （如 IssueAgentActivityIndicator / WorkspaceAgentWorkingChip）。
 *
 * `agentIds` 是完整输入列表。我们最多渲染 `max` 个头像；如果输入更长，
 * 就丢弃尾部、追加一个 `+N` 溢出角标，样式对齐头像尺寸。
 */
export function AgentAvatarStack({
  agentIds,
  size = "sm",
  max = 3,
  opacity = "full",
  className,
}: AgentAvatarStackProps) {
  const { getActorName, getActorInitials, getActorAvatarUrl } = useActorName();
  const { t } = useT("agents");
  if (agentIds.length === 0) return null;

  const visible = agentIds.slice(0, max);
  const overflow = agentIds.length - visible.length;
  const px = AVATAR_SIZE_PX[size];
  // 30% 的重叠能让头像读作“叠在一起”，又不至于挡住下一个头像的图标。
  const overlap = Math.round(px * 0.3);

  return (
    <span
      className={cn(
        "inline-flex items-center",
        opacity === "half" && "opacity-50",
        className,
      )}
      style={{ paddingLeft: 0 }}
    >
      {visible.map((id, i) => (
        <span
          key={id}
          // 后续每个头像用负 margin 叠在前一个之上，让整组头像在水平方向
          // 收缩堆叠，而不是线性铺开。
          style={{ marginLeft: i === 0 ? 0 : -overlap }}
          className="ring-2 ring-background rounded-full inline-flex"
        >
          <ActorAvatarBase
            name={getActorName("agent", id)}
            initials={getActorInitials("agent", id)}
            avatarUrl={getActorAvatarUrl("agent", id)}
            isAgent
            size={size}
          />
        </span>
      ))}
      {overflow > 0 && (
        <span
          style={{
            marginLeft: -overlap,
            width: px,
            height: px,
            fontSize: Math.max(9, Math.round(px * 0.45)),
          }}
          className="ring-2 ring-background rounded-full bg-muted text-muted-foreground inline-flex items-center justify-center font-medium tabular-nums"
          aria-label={t(($) => $.avatar_stack.overflow_aria, {
            count: overflow,
          })}
        >
          +{overflow}
        </span>
      )}
    </span>
  );
}
