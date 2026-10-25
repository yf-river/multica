"use client";

// 本文件：CommentTriggerChips —— 评论输入框下方"将触发哪些 agent"的预览 chips。
// 单 agent 时渲染完整 chip（头像 + 状态点 + 句子，点击切换跳过）；多个 agent
// 折叠成重叠头像堆 + 计数句，逐 agent 的切换收进点击打开的 Popover。
// 悬停层一律只读（Tooltip）。被跳过的目标以"亮度变暗"传达：亮 = 将触发，
// 暗 = 已跳过。另处理"显式 @ 但不会触发"的目标（MUL-4525 §2）——渲染成
// 带原因的名字 warning chip，而不是静默的 no-op。

import { useMemo, useState } from "react";
import { TriangleAlert, Users } from "lucide-react";
import type { CommentReviewReply, CommentTriggerPreviewAgent, CommentTriggerOutcome } from "@multica/core/types";
import { useAgentPresenceDetail } from "@multica/core/agents";
import { mentionLabelsByTarget } from "@multica/core/issues/comment-trigger-outcomes";
import { useCurrentWorkspace } from "@multica/core/paths";
import { ActorAvatar as ActorAvatarBase } from "@multica/ui/components/common/actor-avatar";
import { AVATAR_SIZE_PX } from "@multica/ui/lib/avatar-size";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { Tooltip, TooltipContent, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { cn } from "@multica/ui/lib/utils";
import { AgentStatusDot } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { blockedReasonLabel, blockedShortReasonLabel } from "../blocked-trigger-copy";
import { reviewReplyRecipientLabel } from "../review-reply-copy";

// 单个 agent 完整渲染（"Walt 即将开始工作"，头像 + 状态点，点击切换）。
// 多个 agent 折叠成重叠头像堆 + 计数句，与 issues 头部那个
// WorkspaceAgentWorkingChip 相呼应。悬停层只读（Tooltip）；逐 agent 切换
// 放在点击打开的 Popover 里，好让层级扛得住连续点击。
// 抑制只用亮度传达：亮 = 将触发，暗 = 已跳过。
// 单 agent 头像取 `xs` 档；`+N` 溢出 chip 与堆叠重叠也复用该档的像素直径，
// 让折叠后的堆栈与头像精确对齐。
const AVATAR_SIZE = AVATAR_SIZE_PX.xs;
const MAX_STACK_HEADS = 4;

interface CommentTriggerChipsProps {
  agents: CommentTriggerPreviewAgent[];
  // 显式 @agent/@squad、但按现状发出*不会*触发的提及（MUL-4525 §2）。
  // 每个渲染成具名的 warning chip，让用户看清"哪个目标不会跑、为什么"，
  // 而不是发送后得到一次静默的 no-op。
  blocked?: CommentTriggerOutcome[];
  /** Whether the draft contains the structured @all member broadcast. */
  hasAllMembersMention?: boolean;
  // The draft markdown, used only to label each blocked target with the name the
  // user typed in its mention markup. The server omits blocked target names
  // (enumeration-safety); this is the user's own text, so it discloses nothing new.
  draftContent?: string;
  // 回复审查员时这条草稿的实际接收人（GONG-63）。说明「回复不会交给审查员」，
  // 人类负责人与未设置负责人也要有明确提示，不能读作静默无操作。
  reviewReply?: CommentReviewReply | null;
  suppressedAgentIds: Set<string>;
  onToggle: (agentId: string) => void;
}

type IssuesT = ReturnType<typeof useT<"issues">>["t"];

function sourceLabel(source: string, t: IssuesT): string {
  switch (source) {
    case "issue_assignee":
      return t(($) => $.comment.trigger_source_issue_assignee);
    case "mention_agent":
      return t(($) => $.comment.trigger_source_mention_agent);
    case "mention_squad_leader":
      return t(($) => $.comment.trigger_source_mention_squad_leader);
    case "review_followup_assignee":
      // 回复审查员的转交目标：来源写「任务负责人」，不暗示审查员会回应。
      return t(($) => $.comment.trigger_source_review_followup_assignee);
    default:
      return t(($) => $.comment.trigger_source_unknown);
  }
}

// 负责人 / @提及的触发理由被有意省略：头部（名字 · 来源）已经说明了它们
// 为什么触发，再加一行理由只是复述。只有小队队长链接（不明显）与未知
// 兜底携带头部没有的信息。
function sourceReason(agent: CommentTriggerPreviewAgent, t: IssuesT): string | null {
  switch (agent.source) {
    case "issue_assignee":
    case "mention_agent":
      return null;
    case "mention_squad_leader":
      return t(($) => $.comment.trigger_reason_mention_squad_leader);
    case "review_followup_assignee":
      return t(($) => $.comment.trigger_reason_review_followup_assignee);
    default:
      return agent.reason || t(($) => $.comment.trigger_reason_unknown);
  }
}

// 在线状态只是展示元数据——触发列表本身永远是后端预览。接近在线的 agent
// 立即开工；离线的排队。
function useTriggerPresenceLine(agentId: string, t: IssuesT): string | null {
  const ws = useCurrentWorkspace();
  const detail = useAgentPresenceDetail(ws?.id, agentId);
  if (detail === "loading") return null;
  return detail.availability === "online" || detail.availability === "unstable"
    ? t(($) => $.comment.trigger_starts_now)
    : t(($) => $.comment.trigger_starts_when_online);
}

// 所有触发表面（单 chip、Popover 行）共用同一个 tooltip 正文：
// 谁 · 为什么触发（+ 在线状态）· 点击会怎样。
function TriggerAgentTooltipBody({
  agent,
  suppressed,
  t,
}: {
  agent: CommentTriggerPreviewAgent;
  suppressed: boolean;
  t: IssuesT;
}) {
  const presenceLine = useTriggerPresenceLine(agent.id, t);
  return (
    <div className="space-y-0.5">
      <div className="flex items-baseline gap-1.5">
        <span className="font-medium">{agent.name}</span>
        <span className="text-micro text-muted-foreground">{sourceLabel(agent.source, t)}</span>
      </div>
      {suppressed ? (
        <div>{t(($) => $.comment.trigger_click_to_restore)}</div>
      ) : (
        <>
          {(() => {
            // 理由（如有）与在线状态共用一行；二者都可能缺席，所以只拼接
            // 存在的部分，避免多出多余空格。
            const line = [sourceReason(agent, t), presenceLine].filter(Boolean).join(" ");
            return line ? <div>{line}</div> : null;
          })()}
          <div className="text-muted-foreground">{t(($) => $.comment.trigger_click_to_skip)}</div>
        </>
      )}
    </div>
  );
}

/**
 * "将触发哪些 agent"的预览 chips（职责见文件头）。
 *
 * `agents` 是要触发的 agent 列表；`blocked` 是显式 @ 但不会触发的目标；
 * `draftContent` 仅用于反查被拦目标的名字；`suppressedAgentIds`/`onToggle`
 * 共同实现"跳过某 agent"的开关。
 */
export function CommentTriggerChips({
  agents,
  blocked = [],
  hasAllMembersMention = false,
  draftContent = "",
  reviewReply = null,
  suppressedAgentIds,
  onToggle,
}: CommentTriggerChipsProps) {
  const { t } = useT("issues");
  // 被拦结果不带名字（枚举安全）；从用户自己的草稿里找回他们打的标签，
  // 让每个 chip 能说出拦的是哪个目标。
  const blockedLabels = useMemo(() => mentionLabelsByTarget(draftContent), [draftContent]);

  // 加载与错误都不渲染任何东西：预览只是增强，这里的任何临时 UI 都会
  // 读成输入框噪音。
  if (agents.length === 0 && blocked.length === 0 && !reviewReply && !hasAllMembersMention) return null;

  const reviewNotice = reviewReply && reviewReply.status !== "will_notify" ? (
    <ReviewReplyNotice reviewReply={reviewReply} t={t} />
  ) : null;

  const allowed =
    agents.length === 1 ? (
      <SingleTriggerChip
        agent={agents[0]!}
        suppressed={suppressedAgentIds.has(agents[0]!.id)}
        onToggle={onToggle}
        t={t}
      />
    ) : agents.length > 1 ? (
      <MultiTriggerChip
        agents={agents}
        suppressedAgentIds={suppressedAgentIds}
        onToggle={onToggle}
        t={t}
      />
    ) : null;

  if (blocked.length === 0 && !reviewNotice && !hasAllMembersMention) return allowed;

  return (
    <div className="flex flex-col gap-1">
      {reviewNotice}
      {hasAllMembersMention && <span className="inline-flex items-center gap-1.5 text-micro text-muted-foreground"><Users className="size-3 shrink-0" />{t(($) => $.comment.all_members_notice)}</span>}
      <div className="flex flex-wrap items-center gap-1.5">
        {allowed}
        {blocked.map((outcome) => (
          <BlockedTriggerChip
            key={`${outcome.target_type}:${outcome.target_id}`}
            outcome={outcome}
            label={blockedLabels.get(`${outcome.target_type}:${outcome.target_id}`)}
            t={t}
          />
        ))}
      </div>
    </div>
  );
}

// 回复审查员的接收人提示（GONG-63）：一句话说清这条回复会交给谁，避免用户以为
// 审查员会回应。负责人是人类或未设置时也必须显示——那正是「没有 Agent 启动」的场合。
function ReviewReplyNotice({ reviewReply, t }: { reviewReply: CommentReviewReply; t: IssuesT }) {
  const blocked = reviewReply.status === "blocked";
  const text = reviewReplyRecipientLabel(reviewReply, t);
  if (!text) return null;
  return (
    <div
      className={cn(
        "flex min-w-0 items-center gap-1 text-micro",
        blocked ? "text-destructive" : "text-muted-foreground",
      )}
    >
      {blocked && <TriangleAlert className="size-3 shrink-0" />}
      <span className="truncate" title={text}>
        {text}
      </span>
    </div>
  );
}

// 单条被拦提及：长得像允许的 chip（"Go"），但带错误指示器与短理由
//（"未找到或无权限"），而不是"将开始"——被拒的 @ 因而读作清晰具体的
// 错误，而不是含糊的计数。
function BlockedTriggerChip({
  outcome,
  label,
  t,
}: {
  outcome: CommentTriggerOutcome;
  label?: string;
  t: IssuesT;
}) {
  const shortReason = blockedShortReasonLabel(outcome.reason_code, t);
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span
            className="inline-flex h-6 min-w-0 max-w-full animate-in fade-in items-center gap-1.5 rounded-md px-1.5 text-micro font-medium text-destructive"
            aria-label={
              label
                ? t(($) => $.comment.trigger_blocked_chip_aria, { name: label, reason: shortReason })
                : shortReason
            }
          >
            <TriangleAlert className="size-3 shrink-0" />
            {label ? (
              <span className="inline-flex min-w-0 items-center gap-1">
                <span className="truncate">{label}</span>
                <span className="shrink-0">·</span>
                <span className="shrink-0">{shortReason}</span>
              </span>
            ) : (
              <span className="truncate">{shortReason}</span>
            )}
          </span>
        }
      />
      <TooltipContent side="top" className="max-w-72 text-caption">
        {blockedReasonLabel(outcome.reason_code, t)}
      </TooltipContent>
    </Tooltip>
  );
}

function SingleTriggerChip({
  agent,
  suppressed,
  onToggle,
  t,
}: {
  agent: CommentTriggerPreviewAgent;
  suppressed: boolean;
  onToggle: (agentId: string) => void;
  t: IssuesT;
}) {
  const state = suppressed
    ? t(($) => $.comment.trigger_skipped_label)
    : sourceLabel(agent.source, t);
  // 头像承载"谁"；句子只承载条件与结果，因此保持固定宽度、不因长 agent 名
  // 而截断。
  const sentence = suppressed
    ? t(($) => $.comment.trigger_wont_trigger)
    : t(($) => $.comment.trigger_will_start);

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <button
            type="button"
            aria-pressed={suppressed}
            aria-label={t(($) => $.comment.trigger_chip_aria, { name: agent.name, state })}
            onClick={() => onToggle(agent.id)}
            className={cn(
              // 侧栏式静息态：弱化直到悬停，让这串 chip 读作元数据而非内容
              //（见 app-sidebar 导航项）。
              "inline-flex h-6 min-w-0 max-w-full animate-in fade-in cursor-pointer items-center gap-1.5 rounded-md px-1.5 text-micro font-medium text-muted-foreground transition-colors duration-200 hover:bg-muted hover:text-foreground",
            )}
          >
            <TriggerAgentAvatar agent={agent} suppressed={suppressed} />
            <span className="truncate">{sentence}</span>
          </button>
        }
      />
      <TooltipContent side="top" className="max-w-72 text-caption">
        <TriggerAgentTooltipBody agent={agent} suppressed={suppressed} t={t} />
      </TooltipContent>
    </Tooltip>
  );
}

function MultiTriggerChip({
  agents,
  suppressedAgentIds,
  onToggle,
  t,
}: {
  agents: CommentTriggerPreviewAgent[];
  suppressedAgentIds: Set<string>;
  onToggle: (agentId: string) => void;
  t: IssuesT;
}) {
  const [open, setOpen] = useState(false);
  const [tooltipHover, setTooltipHover] = useState(false);
  const activeCount = agents.filter((a) => !suppressedAgentIds.has(a.id)).length;
  const heads = agents.slice(0, MAX_STACK_HEADS);
  const overflow = agents.length - heads.length;
  // 对应 AgentAvatarStack：约 30% 重叠读作"堆叠"，又不遮住下一颗头像。
  const overlap = Math.round(AVATAR_SIZE * 0.3);
  // 头像堆承载"谁"；句子只承诺"会发生什么"，因此计数覆盖未抑制的
  // agent——被跳过的读作数字旁边那些变暗的头像。
  const sentence =
    activeCount === 0
      ? t(($) => $.comment.trigger_none_will_trigger)
      : t(($) => $.comment.trigger_will_start_count, { count: activeCount });

  const popoverTrigger = (
    <PopoverTrigger
      render={
        // 幽灵按钮式触达（悬停填充 + aria-expanded 钉住），让头像堆读作
        // 可点击，与全应用的选择器一致。
        <button
          type="button"
          className={cn(
            "inline-flex h-6 min-w-0 max-w-full animate-in fade-in cursor-pointer items-center gap-1.5 rounded-md px-1.5 text-micro font-medium text-muted-foreground transition-colors duration-200 hover:bg-muted hover:text-foreground aria-expanded:bg-muted aria-expanded:text-foreground",
          )}
        />
      }
    >
      <span className="inline-flex items-center">
        {heads.map((agent, i) => (
          <span
            key={agent.id}
            style={{ marginLeft: i === 0 ? 0 : -overlap }}
            className="inline-flex rounded-full ring-2 ring-background"
          >
            <TriggerAgentAvatar
              agent={agent}
              suppressed={suppressedAgentIds.has(agent.id)}
              showDot={false}
            />
          </span>
        ))}
        {overflow > 0 && (
          <span
            style={{
              marginLeft: -overlap,
              width: AVATAR_SIZE,
              height: AVATAR_SIZE,
              fontSize: Math.max(9, Math.round(AVATAR_SIZE * 0.45)),
            }}
            className="inline-flex items-center justify-center rounded-full bg-muted font-medium tabular-nums text-muted-foreground ring-2 ring-background"
          >
            +{overflow}
          </span>
        )}
      </span>
      <span className="truncate">{sentence}</span>
    </PopoverTrigger>
  );

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <Tooltip open={tooltipHover && !open} onOpenChange={setTooltipHover}>
        <TooltipTrigger render={popoverTrigger} />
        <TooltipContent side="top" className="text-caption">
          {t(($) => $.comment.trigger_click_to_manage)}
        </TooltipContent>
      </Tooltip>
      <PopoverContent align="start" className="w-64 p-2">
        <div className="px-1.5 pb-1 text-caption font-medium text-muted-foreground">
          {t(($) => $.comment.trigger_preview_title)}
        </div>
        <div className="flex flex-col">
          {agents.map((agent) => {
            const suppressed = suppressedAgentIds.has(agent.id);
            const state = suppressed
              ? t(($) => $.comment.trigger_skipped_label)
              : sourceLabel(agent.source, t);
            return (
              <Tooltip key={agent.id}>
                <TooltipTrigger
                  render={
                    <button
                      type="button"
                      aria-pressed={suppressed}
                      aria-label={t(($) => $.comment.trigger_chip_aria, { name: agent.name, state })}
                      onClick={() => onToggle(agent.id)}
                      className={cn(
                        "flex w-full cursor-pointer items-center gap-2 rounded-md px-1.5 py-1 text-left transition-colors hover:bg-muted",
                      )}
                    >
                      <TriggerAgentAvatar agent={agent} suppressed={suppressed} />
                      <span
                        className={cn(
                          "min-w-0 flex-1 truncate text-caption",
                          suppressed && "text-muted-foreground",
                        )}
                      >
                        {agent.name}
                      </span>
                      <span className="shrink-0 text-micro text-muted-foreground">{state}</span>
                    </button>
                  }
                />
                <TooltipContent side="right" className="max-w-72 text-caption">
                  <TriggerAgentTooltipBody agent={agent} suppressed={suppressed} t={t} />
                </TooltipContent>
              </Tooltip>
            );
          })}
        </div>
      </PopoverContent>
    </Popover>
  );
}

function TriggerAgentAvatar({
  agent,
  suppressed,
  showDot = true,
}: {
  agent: CommentTriggerPreviewAgent;
  suppressed: boolean;
  showDot?: boolean;
}) {
  return (
    <span
      className={cn(
        "relative inline-flex shrink-0",
        suppressed && "opacity-40 grayscale",
      )}
    >
      <ActorAvatarBase
        name={agent.name}
        initials=""
        avatarUrl={agent.avatar_url}
        isAgent
        size="xs"
      />
      {showDot && !suppressed && <AgentStatusDot agentId={agent.id} size="xs" />}
    </span>
  );
}
