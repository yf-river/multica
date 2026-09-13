"use client";

// 本文件是 Agent 的“身份信息卡”（通常作为悬停卡内容出现）：用紧凑布局展示
// 头像、名字、可见性、可用状态，以及运行环境 / 模型 / 技能 / 拥有者等元信息。
// 顶部右侧提供一个仅悬停时出现的“详情 →”链接。

import { useQuery } from "@tanstack/react-query";
import type { Agent, AgentRuntime } from "@multica/core/types";
import { useAgentPresenceDetail } from "@multica/core/agents";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  deriveRuntimeHealth,
  runtimeDisplayLabel,
  type RuntimeHealth,
} from "@multica/core/runtimes";
import { agentListOptions, memberListOptions } from "@multica/core/workspace/queries";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { useWorkspacePaths } from "@multica/core/paths";
import { ActorAvatar as ActorAvatarBase } from "@multica/ui/components/common/actor-avatar";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { AppLink } from "../../navigation";
import { HealthIcon } from "../../runtimes/components/shared";
import { availabilityConfig } from "../presence";
import { VisibilityBadge } from "./visibility-badge";
import { useT } from "../../i18n";

interface AgentProfileCardProps {
  /** 要展示的 Agent id。卡片会自己去拉 Agent / 成员 / 运行环境数据。 */
  agentId: string;
}

/**
 * Agent 身份卡（悬停卡主体）。按 id 取数据并渲染成一张只读迷你卡片；
 * 数据还没加载出来时先显示骨架屏，Agent 不存在时显示“不可用”占位。
 */
export function AgentProfileCard({ agentId }: AgentProfileCardProps) {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const p = useWorkspacePaths();
  const { data: agents = [], isLoading: agentsLoading } = useQuery(agentListOptions(wsId));
  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const { data: runtimes = [] } = useQuery(runtimeListOptions(wsId));

  const agent = agents.find((a) => a.id === agentId);

  if (agentsLoading && !agent) {
    return (
      <div className="flex items-center gap-3">
        <Skeleton className="h-10 w-10 rounded-full" />
        <div className="flex-1 space-y-1.5">
          <Skeleton className="h-4 w-28" />
          <Skeleton className="h-3 w-20" />
        </div>
      </div>
    );
  }

  if (!agent) {
    return (
      <div className="text-caption text-muted-foreground">{t(($) => $.profile_card.unavailable)}</div>
    );
  }

  const owner = agent.owner_id
    ? members.find((m) => m.user_id === agent.owner_id) ?? null
    : null;
  const runtime = runtimes.find((r) => r.id === agent.runtime_id) ?? null;
  const isArchived = !!agent.archived_at;
  const initials = agent.name
    .split(" ")
    .map((w) => w[0])
    .join("")
    .toUpperCase()
    .slice(0, 2);

  return (
    // 卡根节点挂 `group`：右上角的“详情”链接只在鼠标悬停整张卡时淡入，
    // 平时扫一眼时不会碍事。
    <div className="group flex flex-col gap-3 text-left">
      {/* 头部区：左侧头像 + 名字 + 可用状态，右侧“详情 →”链接（仅悬停显示）。
          卡片刻意保持精简：这里只放三态可用圆点，最近任务的状态交给列表页和详情页。 */}
      <div className="flex items-start gap-3">
        {/* 直接用 Base 头像而非 ActorAvatar 包装：这张卡本身就是悬停卡的内容，
            不能再在内部嵌套另一张悬停卡或个人页链接。 */}
        <ActorAvatarBase
          name={agent.name}
          initials={initials}
          avatarUrl={resolvePublicFileUrl(agent.avatar_url)}
          isAgent
          size="xl"
        />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <p className="truncate text-body font-semibold">{agent.name}</p>
            {!isArchived && <VisibilityBadge value={agent.visibility} compact />}
            {isArchived && (
              <span className="rounded-md bg-muted px-1.5 py-0.5 text-micro font-medium text-muted-foreground">
                {t(($) => $.row.archived)}
              </span>
            )}
          </div>
          {!isArchived && (
            <AgentAvailabilityLine wsId={wsId} agentId={agent.id} />
          )}
        </div>
        {!isArchived && (
          <AppLink
            href={p.agentDetail(agent.id)}
            className="mr-1 mt-0.5 shrink-0 text-caption font-normal text-brand opacity-0 transition-opacity group-hover:opacity-100"
          >
            {t(($) => $.profile_card.detail_link)}
          </AppLink>
        )}
      </div>

      {/* Description */}
      {agent.description && (
        <p className="line-clamp-2 text-caption text-muted-foreground">
          {agent.description}
        </p>
      )}

      {/* 元信息行 —— 运行环境（跑在哪）、模型（用什么驱动）、技能（会什么）、
          拥有者（谁管）。这里把模型和推理档位也露出来，让用户悬停一眼就能回答
          “这 Agent 跑的是哪个模型？”，不用点开详情页。 */}
      <div className="flex flex-col gap-1.5 text-caption">
        <RuntimeRow agent={agent} runtime={runtime} />
        <ModelRow model={agent.model} thinkingLevel={agent.thinking_level} />
        {agent.skills.length > 0 && (
          <SkillsRow skills={agent.skills.map((s) => s.name)} />
        )}
        {owner && <MetaRow label={t(($) => $.profile_card.owner_label)} value={owner.name} />}
      </div>
    </div>
  );
}

// 名字下方的紧凑可用状态行 —— 只有一个三态信号（在线 / 不稳定 / 离线）。
// 刻意不展示“最近任务”状态：那属于列表页和详情页，那里的空间够放
// 图标 + 文案 + 原因，放在弹层里反而拥挤。
function AgentAvailabilityLine({
  wsId,
  agentId,
}: {
  wsId: string | undefined;
  agentId: string;
}) {
  const { t } = useT("agents");
  const detail = useAgentPresenceDetail(wsId, agentId);
  if (detail === "loading") {
    return <Skeleton className="mt-0.5 h-3 w-16" />;
  }
  const av = availabilityConfig[detail.availability];
  return (
    <div className="mt-0.5 inline-flex items-center gap-1.5">
      <span className={`h-1.5 w-1.5 rounded-full ${av.dotClass}`} />
      <span className={`text-caption ${av.textClass}`}>{t(($) => $.availability[detail.availability])}</span>
    </div>
  );
}

// 运行环境行 —— wifi 式健康图标 + 运行环境名。图标形状（Wifi / WifiOff）与颜色
// 反映“运行环境 + 时钟”推导出的实时健康度；云端运行环境恒读作在线。
// 它和上面可用圆点是刻意重复的信号：圆点表示 Agent 的“有效可用性”（大多跟随
// 运行环境健康度），而圆点状态到底是哪台机器导致的，由运行环境名旁的
// wifi 图标来确认。
function RuntimeRow({
  agent,
  runtime,
}: {
  agent: Agent;
  runtime: AgentRuntime | null;
}) {
  const { t } = useT("agents");
  const isCloud = agent.runtime_mode === "cloud";
  const health: RuntimeHealth = isCloud
    ? "online"
    : runtime
      ? deriveRuntimeHealth(runtime, Date.now())
      : "offline";
  const label = runtime
    ? runtimeDisplayLabel(runtime)
    : isCloud
      ? t(($) => $.row.fallback_runtime_cloud)
      : t(($) => $.profile_card.unknown_runtime);
  return (
    <div className="flex items-center gap-1.5">
      <span className="w-12 shrink-0 text-muted-foreground">{t(($) => $.profile_card.runtime_label)}</span>
      <HealthIcon health={health} className="h-3 w-3 shrink-0" />
      <span className="min-w-0 truncate" title={label}>
        {label}
      </span>
    </div>
  );
}

// 模型行 —— 展示 Agent 实际跑的运行时模型 id（`agent.model`），并在其固定了
// 推理档位时以小徽章追加显示。model 为空表示“未覆盖”：运行时 CLI 自己的默认
// 在运行期决定，所以这里显示“运行时默认”占位而不是一个横杠。模型 id 用等宽字体，
// 让 `claude-opus-4-8` 这种带厂商前缀的 id 保持可读。推理徽章只看 `effort` 有值与否
// （不看 `hasModel`）：一个“运行时默认”模型的 Agent 仍可能持久化了 `thinking_level`
// 覆盖，运行期会作用到运行时的默认模型上——这一行本就是为了把这种运行时信息
// 一眼呈现出来，藏着反而会误报 Agent 的真实配置。
function ModelRow({
  model,
  thinkingLevel,
}: {
  model: string;
  thinkingLevel?: string;
}) {
  const { t } = useT("agents");
  const hasModel = model.trim().length > 0;
  const effort = thinkingLevel?.trim();
  return (
    <div className="flex items-center gap-1.5">
      <span className="w-12 shrink-0 text-muted-foreground">
        {t(($) => $.profile_card.model_label)}
      </span>
      {hasModel ? (
        <span className="min-w-0 truncate font-mono text-micro" title={model}>
          {model}
        </span>
      ) : (
        <span className="min-w-0 truncate text-muted-foreground">
          {t(($) => $.profile_card.model_unset)}
        </span>
      )}
      {effort && (
        <span className="shrink-0 rounded-md bg-muted px-1.5 py-0.5 text-micro font-medium text-muted-foreground">
          {effort}
        </span>
      )}
    </div>
  );
}

/** 一行键值对：固定宽标签 + 可截断的值（等宽模式用于 id 类文本）。 */
function MetaRow({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div className="flex items-center gap-1.5">
      <span className="w-12 shrink-0 text-muted-foreground">{label}</span>
      <span className={`truncate ${mono ? "font-mono text-micro" : ""}`} title={value}>
        {value}
      </span>
    </div>
  );
}

/** 技能行：最多显示 3 个技能 chip，超出折叠成 `+N`；长名字在 chip 内截断。 */
function SkillsRow({ skills }: { skills: string[] }) {
  const { t } = useT("agents");
  const visible = skills.slice(0, 3);
  const overflow = skills.length - visible.length;
  return (
    // 用 `items-start`（而非居中）：技能 chip 会换行成多排，垂直居中的标签会被
    // 拽到中间那一排旁边，让第一个 chip 看起来像属于上一行。把标签钉在第一排，
    // `pt-0.5` 让文字和 chip 文字（chip 自带 `py-0.5`）对齐。每个 chip 截断，
    // 防止单个超长技能名撑爆卡片宽度。
    <div className="flex items-start gap-1.5">
      <span className="w-12 shrink-0 pt-0.5 text-muted-foreground">{t(($) => $.profile_card.skills_label)}</span>
      <div className="flex min-w-0 flex-wrap gap-1">
        {visible.map((s) => (
          <span
            key={s}
            className="max-w-full truncate rounded-md bg-muted px-1.5 py-0.5 text-micro font-medium text-muted-foreground"
            title={s}
          >
            {s}
          </span>
        ))}
        {overflow > 0 && (
          <span className="shrink-0 rounded-md bg-muted px-1.5 py-0.5 text-micro font-medium text-muted-foreground">
            +{overflow}
          </span>
        )}
      </div>
    </div>
  );
}
