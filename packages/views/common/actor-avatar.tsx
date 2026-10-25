// 通用演员头像组件：按 actorType（成员/智能体/小队）渲染对应头像，支持悬浮卡片预览、在线状态圆点，以及可点击跳转到演员详情页。
"use client";

import { useEffect, useRef, useState } from "react";
import { ActorAvatar as ActorAvatarBase } from "@multica/ui/components/common/actor-avatar";
import { AVATAR_SIZE_PX, type AvatarSize } from "@multica/ui/lib/avatar-size";
import {
  HoverCard,
  HoverCardTrigger,
  HoverCardContent,
} from "@multica/ui/components/ui/hover-card";
import { useActorName } from "@multica/core/workspace/hooks";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { useAgentPresenceDetail } from "@multica/core/agents";
import { useCurrentWorkspace, useWorkspacePaths } from "@multica/core/paths";
import { AgentProfileCard } from "../agents/components/agent-profile-card";
import { AgentLivePeekCard } from "../agents/components/agent-live-peek-card";
import { MemberProfileCard } from "../members/member-profile-card";
import { SquadProfileCard } from "../squads/components/squad-profile-card";
import { availabilityConfig } from "../agents/presence";
import { useT } from "../i18n";
import {
  resolveClickIntent,
  useIntentNavigate,
  type LinkClickIntent,
} from "../navigation";

/**
 * 当 `enableHoverCard` 打开时，决定智能体悬浮卡片展示哪种内容。
 * - `"profile"`（默认）：静态身份信息（简介、运行时、技能、负责人），
 *   用在评论作者、选择器、列表行等"这是谁"的展示场景。
 * - `"live"`：实时活动概览（工作量、当前任务、最近动态），
 *   用在小队成员页等用户已认识身份、只想看实时状态的场景。
 *
 * 对非智能体演员无效（成员始终渲染成员卡片）。
 */
export type AgentHoverCardVariant = "profile" | "live";

interface ActorAvatarProps {
  /** 演员类型，如 "member" / "agent" / "squad" / "system"。 */
  actorType: string;
  /** 演员的唯一标识（成员用户 ID、智能体 ID 或小队 ID）。 */
  actorId: string;
  /** Timeline-provided identity for actors no longer in the live directory. */
  name?: string;
  avatarUrl?: string | null;
  /**
   * Disable profile interactions after the live directory confirms that this
   * actor no longer has a profile. Timeline surfaces opt in because their
   * hydrated identity remains displayable after an actor leaves.
   */
  profileRequiresDirectoryEntry?: boolean;
  size?: AvatarSize;
  /** 附加到头像上的自定义 className。 */
  className?: string;
  /**
   * 悬停时用悬浮卡片预览演员信息。用于评论作者、列表行、订阅者标签等
   * "这是谁"的展示场景。与 `showStatusDot` 相互独立，两者可同时、也可
   * 单独或都不启用。
   */
  enableHoverCard?: boolean;
  /**
   * 在头像右下角叠加智能体在线状态圆点。用于选择器行、当前负责人展示等
   * 需要判断状态的场景。对非智能体演员无效。与 `enableHoverCard` 相互
   * 独立，这样选择器行可以显示圆点而不用在下拉里嵌套弹层。
   */
  showStatusDot?: boolean;
  /**
   * 当智能体启用悬浮卡片时，选择渲染哪种内容。见 {@link AgentHoverCardVariant}。
   * 默认 `"profile"`，保持现有调用方的身份卡片行为不变。
   */
  hoverCardVariant?: AgentHoverCardVariant;
  /**
   * 让头像可点击跳转到演员页面。成员和智能体默认开启，而选择器/菜单
   * 控件保留自己的点击行为。
   */
  profileLink?: boolean;
}

const FOCUSABLE_ANCESTOR_SELECTOR =
  'a[href], button:not([disabled]), [role="button"]:not([aria-disabled="true"]), [tabindex]:not([tabindex="-1"])';
const PROFILE_LINK_CONTROL_SELECTOR =
  'button, [role^="menuitem"], [role="option"], [data-slot="dropdown-menu-item"], [data-slot="dropdown-menu-checkbox-item"], [data-slot="popover-trigger"]';

/**
 * 演员头像组件。根据演员类型解析名称、缩写和头像地址，再按需包裹：
 * 在线状态圆点、个人资料链接、以及各类悬浮预览卡片。
 * @param actorType 演员类型（member / agent / squad / system 等）。
 * @param actorId 演员 ID。
 * @param size 头像尺寸档位。
 * @param className 附加样式。
 * @param enableHoverCard 是否在悬停时展示预览卡片。
 * @param showStatusDot 是否为智能体叠加在线状态圆点。
 * @param hoverCardVariant 智能体悬浮卡片的内容变体。
 * @param profileLink 是否可点击跳转到演员页面。
 */
export function ActorAvatar({
  actorType,
  actorId,
  name,
  avatarUrl,
  profileRequiresDirectoryEntry = false,
  size,
  className,
  enableHoverCard,
  showStatusDot,
  hoverCardVariant = "profile",
  profileLink,
}: ActorAvatarProps) {
  const {
    getActorName,
    getActorInitials,
    getActorAvatarUrl,
    hasActor,
  } = useActorName();
  const paths = useWorkspacePaths();
  const resolvedName = name ?? getActorName(actorType, actorId);
  const resolvedAvatarUrl =
    avatarUrl === undefined
      ? getActorAvatarUrl(actorType, actorId)
      : avatarUrl?.startsWith("/")
        ? resolvePublicFileUrl(avatarUrl)
        : avatarUrl;
  const avatar = (
    <ActorAvatarBase
      name={resolvedName}
      initials={getActorInitials(actorType, actorId, resolvedName)}
      avatarUrl={resolvedAvatarUrl}
      isAgent={actorType === "agent"}
      isSystem={actorType === "system"}
      isSquad={actorType === "squad"}
      size={size}
      className={className}
    />
  );

  // 可选的在线状态叠加层。只对智能体有意义——成员没有在线状态的后端数据。
  // 无条件用 relative inline-flex 包裹会给每个头像多一层 DOM，所以只在
  // 确实需要圆点时才包裹。
  const wrapDot = showStatusDot && actorType === "agent";
  const dotted = wrapDot ? (
    <span className="relative inline-flex">
      {avatar}
      <AgentStatusDot agentId={actorId} size={size} />
    </span>
  ) : (
    avatar
  );
  const profileAvailable =
    !profileRequiresDirectoryEntry || hasActor(actorType, actorId) !== false;
  const shouldLinkToProfile =
    profileAvailable &&
    (profileLink ??
      (actorType === "member" ||
        actorType === "agent" ||
        actorType === "squad"));
  const profileHref = shouldLinkToProfile
    ? actorType === "member"
      ? paths.memberDetail(actorId)
      : actorType === "agent"
        ? paths.agentDetail(actorId)
        : actorType === "squad"
          ? paths.squadDetail(actorId)
          : null
    : null;
  const content = profileHref ? (
    <ActorAvatarProfileLink href={profileHref}>{dotted}</ActorAvatarProfileLink>
  ) : (
    dotted
  );

  if (!enableHoverCard || !profileAvailable) {
    return content;
  }
  if (actorType === "agent") {
    return (
      <AgentAvatarHoverCard agentId={actorId} variant={hoverCardVariant}>
        {content}
      </AgentAvatarHoverCard>
    );
  }
  if (actorType === "member") {
    return <MemberAvatarHoverCard userId={actorId}>{content}</MemberAvatarHoverCard>;
  }
  if (actorType === "squad") {
    return <SquadAvatarHoverCard squadId={actorId}>{content}</SquadAvatarHoverCard>;
  }
  return content;
}

/**
 * 头像的个人资料链接。故意不用 `<a>`：头像常被嵌在菜单项、选项和按钮里，
 * 此时它应把点击让给外层控件。代价是没有原生右键菜单，所以这里手动实现了
 * 修饰键点击和中键点击，语义与 AppLink 保持一致。
 */
function ActorAvatarProfileLink({
  href,
  children,
}: {
  href: string;
  children: React.ReactNode;
}) {
  // Web 端说明：触发元素是 `<span role="link">` 而非 `<a>`，没有原生的修饰键
  // 点击行为可用，所以 useIntentNavigate 直接自己开浏览器标签页，而不是
  // 让点击在当前位置导航。
  const intentNavigate = useIntentNavigate();

  const insideControl = (event: React.SyntheticEvent) =>
    !!event.currentTarget.parentElement?.closest(PROFILE_LINK_CONTROL_SELECTOR);

  const open = (intent: LinkClickIntent) => intentNavigate(href, intent);

  const navigate = (event: React.MouseEvent | React.KeyboardEvent) => {
    if (insideControl(event)) return;
    event.preventDefault();
    event.stopPropagation();
    open(
      "metaKey" in event && "button" in event
        ? resolveClickIntent(event as React.MouseEvent)
        : "push",
    );
  };

  return (
    <span
      role="link"
      tabIndex={-1}
      className="inline-flex cursor-pointer rounded-full"
      onClick={navigate}
      onAuxClick={(event) => {
        if (event.defaultPrevented || event.button !== 1) return;
        if (insideControl(event)) return;
        event.preventDefault();
        event.stopPropagation();
        open("background-tab");
      }}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          navigate(event);
        }
      }}
    >
      {children}
    </span>
  );
}

// 叠在智能体头像右下角的小号在线状态指示圆点。
// 只在启用悬浮的界面上渲染，这样密集的装饰性 chip（例如列表行里 14px 的
// 负责人子头像）仍保持视觉干净。圆点随头像尺寸缩放——≥24px 用标准 8px 圆点，
// 更小的头像用 6px 圆点，避免指示器喧宾夺主。
// 导出给那些直接渲染基础头像（如评论触发 chip）但仍想要标准状态圆点的场景使用。
/**
 * 智能体在线状态圆点。基于智能体的可用性状态渲染对应颜色的小圆点。
 * @param agentId 智能体 ID。
 * @param size 头像尺寸档位，用于决定圆点大小。
 */
export function AgentStatusDot({ agentId, size }: { agentId: string; size?: AvatarSize }) {
  const ws = useCurrentWorkspace();
  const detail = useAgentPresenceDetail(ws?.id, agentId);
  const { t } = useT("agents");
  if (detail === "loading") return null;

  const { dotClass } = availabilityConfig[detail.availability];
  const px = size ? AVATAR_SIZE_PX[size] : 24;
  const dotSize = px >= 24 ? "h-1.5 w-1.5" : "h-1 w-1";

  return (
    <span
      // 圆点是这个头像上唯一的在线状态信号，所以 aria-label 就是全部可访问
      // 信息——必须整体翻译（含前缀）。它以前读作 `Status: ...label`，在
      // 所有语言环境下都是英文（#7411）。
      aria-label={t(($) => $.presence_status_aria, {
        status: t(($) => $.availability[detail.availability]),
      })}
      className={`absolute bottom-0 right-0 rounded-full ring-1 ring-background ${dotClass} ${dotSize}`}
    />
  );
}

/**
 * 用悬浮卡片包裹智能体头像。只有当外层没有可聚焦的祖先（链接/按钮）提供
 * 制表位时，触发元素才是键盘可聚焦的——这避免了嵌套的可制表后代，也避免
 * 头像位于行链接或点击目标内部时的键盘导航膨胀。
 */
function AgentAvatarHoverCard({
  agentId,
  variant,
  children,
}: {
  agentId: string;
  variant: AgentHoverCardVariant;
  children: React.ReactNode;
}) {
  const content =
    variant === "live" ? (
      <AgentLivePeekCard agentId={agentId} />
    ) : (
      <AgentProfileCard agentId={agentId} />
    );
  return (
    <ActorAvatarHoverCardShell content={content}>
      {children}
    </ActorAvatarHoverCardShell>
  );
}

function MemberAvatarHoverCard({
  userId,
  children,
}: {
  userId: string;
  children: React.ReactNode;
}) {
  return (
    <ActorAvatarHoverCardShell content={<MemberProfileCard userId={userId} />}>
      {children}
    </ActorAvatarHoverCardShell>
  );
}

function SquadAvatarHoverCard({
  squadId,
  children,
}: {
  squadId: string;
  children: React.ReactNode;
}) {
  return (
    <ActorAvatarHoverCardShell content={<SquadProfileCard squadId={squadId} />}>
      {children}
    </ActorAvatarHoverCardShell>
  );
}

// 智能体和成员悬浮卡片共用的外壳。保证焦点行为和宽度一致，让两种卡片在
// 结构上保持平行——内容不同，框架相同。
//
// 不要为了省每个头像的挂载开销而在 pointerenter 时延迟挂载 HoverCard
// （MUL-4827）。Base UI 通过触发元素上的原生 mouseenter/mouseleave 监听器来
// 驱动悬浮，且把关闭逻辑装在 mouseleave 处理器内部——所以一个从未真正收到
// mouseenter 的触发器，既无法取消待定的打开，也无法靠悬浮关闭。在 pointerenter
// 时预热会在手势中途替换节点，恰好丢失这些事件，导致被划过的头像约 600ms 后
// 弹出并卡住。这与 DeferredPopup 记录的不变式一致：延迟只对"结束一次手势"的
// 事件（click/Enter）安全，而 hover 会开启一次手势。立即挂载根节点每个头像
// 只多花约 0.15ms 的 JS，且关闭时零 DOM（弹层子树及其查询在打开前保持未挂载）。
function ActorAvatarHoverCardShell({
  content,
  children,
}: {
  content: React.ReactNode;
  children: React.ReactNode;
}) {
  const triggerRef = useRef<HTMLSpanElement>(null);
  const [standalone, setStandalone] = useState(false);

  useEffect(() => {
    const el = triggerRef.current;
    if (!el) return;
    const ancestor = el.parentElement?.closest(FOCUSABLE_ANCESTOR_SELECTOR);
    setStandalone(!ancestor);
  }, []);

  const tabIndex = standalone ? 0 : -1;
  const className = standalone
    ? "inline-flex cursor-pointer rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    : "inline-flex cursor-pointer";

  return (
    <HoverCard>
      <HoverCardTrigger
        render={<span ref={triggerRef} />}
        tabIndex={tabIndex}
        className={className}
      >
        {children}
      </HoverCardTrigger>
      <HoverCardContent align="start" className="w-72">
        {content}
      </HoverCardContent>
    </HoverCard>
  );
}
