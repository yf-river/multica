// 新手引导开场卡片：在 Mika 的欢迎语下展示三个固定的入门方式卡片。
"use client";

import { useState } from "react";
import { ArrowUpRight, Check, Clock, LoaderCircle } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import {
  Tooltip,
  TooltipTrigger,
  TooltipContent,
} from "@multica/ui/components/ui/tooltip";
import type { ChatQuickAction } from "@multica/core/types";
import { useT } from "../../i18n";

/**
 * 产品固定的开场卡片，渲染在 Mika 的新手引导开场白（隐藏的 `onboarding_kickoff`
 * 之后的第一条助手回复）下面。它们用三种具体的开始方式取代那一条消息的
 * LLM 快捷操作 chips，让新成员永远不会面对一张白纸（MUL-5765）。
 *
 * 点击卡片走的是和快捷操作 chip 相同的路径：卡片的提示词作为一条可见的成员消息
 * 发送，然后 Mika 开始工作。卡片保留在会话历史里并保持可点击；当有任务在运行时，
 * 它们通过 `disabled` 遵循 chips 的禁用规则。
 * @param onPick - 点击卡片后的回调，接收一个快捷操作。
 * @param disabled - 是否禁用（有任务运行时应禁用）。
 */
export function OnboardingStarterCards({
  onPick,
  disabled,
}: {
  onPick: (action: ChatQuickAction) => void | Promise<unknown>;
  disabled: boolean;
}) {
  const { t } = useT("chat");
  const [submitting, setSubmitting] = useState(false);
  const blocked = disabled || submitting;

  const cards = [
    {
      key: "board",
      title: t(($) => $.onboarding_cards.board.title),
      desc: t(($) => $.onboarding_cards.board.desc),
      prompt: t(($) => $.onboarding_cards.board.prompt),
      vignette: <BoardVignette />,
    },
    {
      key: "delegate",
      title: t(($) => $.onboarding_cards.delegate.title),
      desc: t(($) => $.onboarding_cards.delegate.desc),
      prompt: t(($) => $.onboarding_cards.delegate.prompt),
      vignette: <DelegateVignette />,
    },
    {
      key: "digest",
      title: t(($) => $.onboarding_cards.digest.title),
      desc: t(($) => $.onboarding_cards.digest.desc),
      prompt: t(($) => $.onboarding_cards.digest.prompt),
      vignette: <DigestVignette />,
    },
  ];

  const handlePick = async (card: (typeof cards)[number]) => {
    if (blocked) return;
    setSubmitting(true);
    try {
      await onPick({ label: card.title, prompt: card.prompt });
    } catch {
      // 发送路径负责面向用户的错误反馈；这里重新启用，让临时失败可以被重试。
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div
      className="mt-3 grid grid-cols-1 gap-3 @2xl:grid-cols-3"
      aria-label={t(($) => $.onboarding_cards.aria_label)}
    >
      {cards.map((card) => (
        // 和快捷操作 chips 一样的“先看后提交”契约：整张卡片预览它将以成员名义
        // 发送的确切消息。
        <Tooltip key={card.key}>
          <TooltipTrigger
            render={
              <button
                type="button"
                disabled={blocked}
                aria-label={card.title}
                onClick={() => void handlePick(card)}
                className={cn(
                  "group flex flex-col overflow-clip rounded-xl border bg-card text-left transition-colors",
                  "hover:border-brand/40 hover:bg-accent/40",
                  "disabled:pointer-events-none disabled:opacity-55",
                )}
              />
            }
          >
            <div className="flex h-26 shrink-0 items-center justify-center bg-gradient-to-b from-muted/55 to-transparent">
              {card.vignette}
            </div>
            <div className="flex min-h-0 flex-1 flex-col items-start px-3.5 pt-3 pb-3.5">
              <div className="text-body-lg font-medium">{card.title}</div>
              <div className="mt-0.5 text-caption text-muted-foreground">
                {card.desc}
              </div>
              <div className="mt-auto flex items-center gap-1 pt-2.5 text-label font-medium text-brand">
                {t(($) => $.onboarding_cards.cta)}
                <ArrowUpRight aria-hidden="true" className="size-3.5" />
              </div>
            </div>
          </TooltipTrigger>
          <TooltipContent side="top" className="max-w-sm whitespace-pre-wrap break-words">
            {card.prompt}
          </TooltipContent>
        </Tooltip>
      ))}
    </div>
  );
}

// ─── 小插画（Vignettes）──────────────────────────────────────────────────
//
// 用 token 绘制的产品缩略图，而非位图：它们继承主题，所以同样的标记在
// 浅色和深色下都成立。整体都是装饰性的（aria-hidden）；
// 除了 digest 计划徽章之外没有任何文字，而那个徽章是真实的多语言文案。

/** 一条细长的横条，用作卡片内容占位。 */
function Bar({ className }: { className?: string }) {
  return (
    <div
      className={cn("h-1 rounded-full bg-muted-foreground/30", className)}
    />
  );
}

/** 迷你卡片容器，用于小插画内部的一块块内容。 */
function MiniCard({
  className,
  children,
}: {
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <div
      className={cn(
        "flex flex-col gap-1 rounded-[4px] border border-border bg-card px-1.5 py-1",
        className,
      )}
    >
      {children}
    </div>
  );
}

// 三列微缩看板；中间那列卡片带有 Mika 和品牌强调色。
function BoardVignette() {
  return (
    <div aria-hidden="true" className="flex w-49 gap-1.5">
      <div className="flex flex-1 flex-col gap-1 rounded-md bg-muted/70 p-1.5">
        <div className="h-1.5 w-6.5 rounded-full bg-muted-foreground/20" />
        <MiniCard>
          <Bar className="w-4/5" />
          <Bar className="w-1/2" />
        </MiniCard>
        <MiniCard>
          <Bar className="w-2/3" />
        </MiniCard>
      </div>
      <div className="flex flex-1 flex-col gap-1 rounded-md bg-muted/70 p-1.5">
        <div className="h-1.5 w-8 rounded-full bg-brand/30" />
        <MiniCard className="border-brand/45">
          <Bar className="w-3/4" />
          <div className="flex items-center gap-1">
            <span className="flex size-3.5 items-center justify-center rounded-full bg-muted text-micro leading-none">
              🦄
            </span>
            <Bar className="w-6 bg-brand/55" />
          </div>
        </MiniCard>
      </div>
      <div className="flex flex-1 flex-col gap-1 rounded-md bg-muted/70 p-1.5">
        <div className="h-1.5 w-6.5 rounded-full bg-muted-foreground/20" />
        <MiniCard>
          <Bar className="w-3/4" />
        </MiniCard>
        <MiniCard className="opacity-60">
          <Bar className="w-1/2" />
        </MiniCard>
      </div>
    </div>
  );
}

// 一个在 Mika 下运行的任务，以及它下面已交付的结果。
function DelegateVignette() {
  return (
    <div aria-hidden="true" className="flex w-45 flex-col gap-1.5">
      <MiniCard className="flex-row items-center gap-1.5 px-2 py-1.5">
        <span className="flex size-4.5 shrink-0 items-center justify-center rounded-full bg-muted text-micro leading-none">
          🦄
        </span>
        <div className="flex flex-1 flex-col gap-1">
          <Bar className="w-5/6" />
          <Bar className="w-1/2" />
        </div>
        <LoaderCircle aria-hidden="true" className="size-3.5 shrink-0 text-brand" />
      </MiniCard>
      <MiniCard className="flex-row items-center gap-1.5 px-2 py-1.5">
        <Check aria-hidden="true" className="size-3.5 shrink-0 text-success" />
        <div className="flex flex-1 flex-col gap-1">
          <Bar className="w-2/3" />
          <Bar className="w-5/6" />
        </div>
      </MiniCard>
    </div>
  );
}

// 一个收件箱摘要：计划徽章加三行摘要。
function DigestVignette() {
  const { t } = useT("chat");
  return (
    <div className="flex w-43 flex-col gap-1.5">
      <div className="flex items-center gap-1 self-start rounded-full bg-brand/12 px-2 py-0.5 text-micro font-semibold text-brand">
        <Clock aria-hidden="true" className="size-2.5" />
        {t(($) => $.onboarding_cards.digest_badge)}
      </div>
      <div aria-hidden="true" className="flex flex-col gap-1">
        <MiniCard className="flex-row items-center gap-1.5 px-1.5 py-1">
          <div className="size-1.5 rounded-full bg-brand" />
          <Bar className="w-4/5" />
        </MiniCard>
        <MiniCard className="flex-row items-center gap-1.5 px-1.5 py-1">
          <div className="size-1.5 rounded-full bg-muted-foreground/40" />
          <Bar className="w-3/5" />
        </MiniCard>
        <MiniCard className="flex-row items-center gap-1.5 px-1.5 py-1 opacity-60">
          <div className="size-1.5 rounded-full bg-muted-foreground/40" />
          <Bar className="w-2/3" />
        </MiniCard>
      </div>
    </div>
  );
}
