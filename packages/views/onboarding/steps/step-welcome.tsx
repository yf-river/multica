"use client";

// 引导第 0 步（欢迎页）：一次性的产品介绍屏，左文案右示意图，让新用户理解
// 「人与 Agent 协作」的画面，然后开始探索或跳过回已有工作区。
import { statusCategoryOfKey } from "@multica/core/issues";
import { useState } from "react";
import { ArrowRight, Loader2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { cn } from "@multica/ui/lib/utils";
import { DragStrip } from "@multica/views/platform";
import { STATUS_CONFIG } from "@multica/core/issues/config";
import type { IssueStatus } from "@multica/core/types";
import { StatusIcon } from "../../issues/components/status-icon";
import { ProviderLogo } from "../../runtimes/components/provider-logo";
import { useT } from "../../i18n";

/**
 * 第 0 步——每次进入引导都会看到的一次性产品介绍页（走到哪一步不会被持久化）。
 * 已经完成引导的老用户到不了这屏：更早的 `!hasOnboarded` 判断已把他们拦下。
 *
 * 版式：lg+ 是左右两栏的编辑风主视觉，以下收成单栏。左栏 = 品牌字 + 衬线大字
 * 标题 + 引导语 + CTA；右栏 = 一叠「示例任务卡片」，展示人与 Agent 在面板上
 * 协作长什么样——也就是用户即将创建的东西。右栏只是插图、不是内容：lg 以下
 * 隐藏，保证标题和 CTA 在窄屏上是焦点。
 *
 * `onSkip` 提供时渲染一个次要的幽灵 CTA：在服务端把引导标记为完成，并把用户
 * 直接送进他已有的工作区。OnboardingFlow 只在用户已有 ≥1 个工作区时才传它——
 * 没有工作区就跳过，用户会落到没有着落的地方。
 *
 * @param onNext 「开始探索」的回调（进入正式引导第一步）
 * @param onSkip 「我以前弄过」的回调（直接跳到已有工作区，可选）
 */
export function StepWelcome({
  onNext,
  onSkip,
}: {
  onNext: () => void | Promise<void>;
  onSkip?: () => void | Promise<void>;
}) {
  const { t } = useT("onboarding");
  // 记录哪个按钮正在请求中：用来显示各自的小菊花，并在这期间同时禁用两个按钮。
  const [pending, setPending] = useState<"next" | "skip" | null>(null);

  const handleNext = async () => {
    if (pending) return;
    setPending("next");
    try {
      await onNext();
    } finally {
      setPending(null);
    }
  };

  const handleSkip = async () => {
    if (pending || !onSkip) return;
    setPending("skip");
    try {
      await onSkip();
    } finally {
      setPending(null);
    }
  };

  return (
    <div className="animate-onboarding-enter flex h-full min-h-[640px] flex-col lg:flex-row">
      {/* 左栏——文案 + 主按钮区（CTA） */}
      <div className="flex flex-col lg:flex-1">
        <DragStrip />
        <div className="flex flex-1 flex-col justify-center px-6 pb-12 sm:px-10 md:px-20 lg:px-20 xl:px-24">
          <div className="flex w-full max-w-[540px] flex-col gap-8">
            <div className="flex items-center gap-2.5">
              <MulticaIcon className="size-5 text-foreground" noSpin />
              <span className="font-serif text-title-lg font-medium tracking-tight">
                {t(($) => $.welcome.wordmark)}
              </span>
            </div>

            <h1 className="text-balance font-serif text-5xl font-medium leading-[1.04] tracking-tight sm:text-6xl">
              {t(($) => $.welcome.headline_line1)}
              <br />
              {t(($) => $.welcome.headline_line2)}{" "}
              <em className="italic text-brand">{t(($) => $.welcome.headline_emphasis)}</em>
            </h1>

            <div className="flex flex-col gap-4">
              <p className="text-title leading-relaxed text-foreground">
                {t(($) => $.welcome.lede)}
              </p>
              <p className="text-body leading-relaxed text-muted-foreground">
                {t(($) => $.welcome.lede_web)}
              </p>
            </div>

            <div className="flex flex-wrap items-center gap-3">
              <Button
                size="lg"
                onClick={handleNext}
                disabled={pending !== null}
              >
                {pending === "next" && (
                  <Loader2 className="h-4 w-4 animate-spin" />
                )}
                {t(($) => $.welcome.start_exploring)}
                <ArrowRight className="h-4 w-4" />
              </Button>
              {onSkip && (
                <Button
                  size="lg"
                  variant="ghost"
                  onClick={handleSkip}
                  disabled={pending !== null}
                >
                  {pending === "skip" && (
                    <Loader2 className="h-4 w-4 animate-spin" />
                  )}
                  {t(($) => $.welcome.skip_existing)}
                </Button>
              )}
            </div>
          </div>
        </div>
      </div>

      {/* 右栏——示例任务卡片插图。lg 以下隐藏。lg+ 用 flex 行 + 默认的
          `items-stretch` 让两栏都撑满容器高度，灰色背景才能铺满整个视口边缘；
          内部 `justify-center` 让示例卡片垂直居中，与左栏文案的居中构图呼应。 */}
      <div className="hidden border-l bg-muted/40 lg:flex lg:flex-1 lg:flex-col lg:overflow-hidden">
        <DragStrip />
        <div className="flex flex-1 flex-col items-center justify-center gap-7 px-8 py-8">
          <p className="max-w-[440px] text-balance text-center font-serif text-body-lg italic leading-snug text-muted-foreground">
            {t(($) => $.welcome.illustration_caption)}
          </p>
          <WelcomeIllustration />
        </div>
      </div>
    </div>
  );
}


/**
 * 「单人用户多 Agent 工作区里的一天」插画：五张活动卡片交织在三张共享任务上
 * （MCA-42 出现 3 次），让读者能直观看到 Agent 们互相引用彼此的成果——
 * 把产品「一个工作区、共享上下文」的主张落到实处。
 *
 * 卡片用了轻微旋转 + 缩进，看起来像随手叠起来的一摞而不是整齐的信息流，
 * 呼应左栏编辑风主视觉。
 */
function WelcomeIllustration() {
  const { t } = useT("onboarding");
  return (
    <div className="flex w-full max-w-[460px] flex-col gap-3">
      <MockActivityCard
        actor={{
          kind: "user",
          name: t(($) => $.welcome.illustration.card1_actor_name),
          initial: t(($) => $.welcome.illustration.card1_actor_initial),
        }}
        issueId="MCA-42"
        content={
          <>
            <Mention>{t(($) => $.welcome.illustration.card1_mention_content)}</Mention>
            {t(($) => $.welcome.illustration.card1_body_prefix)}
            <Mention>{t(($) => $.welcome.illustration.card1_mention_research)}</Mention>
            {t(($) => $.welcome.illustration.card1_body_suffix)}
          </>
        }
      />
      <MockActivityCard
        className="-translate-x-5 -rotate-[1.2deg]"
        actor={{
          kind: "agent",
          name: t(($) => $.welcome.illustration.card2_actor_name),
          provider: "codex",
        }}
        issueId="MCA-42"
        content={t(($) => $.welcome.illustration.card2_body)}
        status="in_progress"
      />
      <MockActivityCard
        className="translate-x-8 rotate-[1.6deg]"
        actor={{
          kind: "agent",
          name: t(($) => $.welcome.illustration.card3_actor_name),
          provider: "hermes",
        }}
        issueId="MCA-38"
        content={t(($) => $.welcome.illustration.card3_body)}
        status="done"
        timestamp={t(($) => $.welcome.illustration.card3_timestamp)}
      />
      <MockActivityCard
        className="-translate-x-6 -rotate-[0.8deg]"
        actor={{
          kind: "agent",
          name: t(($) => $.welcome.illustration.card4_actor_name),
          provider: "hermes",
        }}
        issueId="MCA-42"
        content={t(($) => $.welcome.illustration.card4_body)}
        status="in_review"
      />
      <MockActivityCard
        className="translate-x-6 rotate-[1deg]"
        actor={{
          kind: "agent",
          name: t(($) => $.welcome.illustration.card5_actor_name),
          provider: "claude",
        }}
        issueId="MCA-35"
        content={
          <>
            {t(($) => $.welcome.illustration.card5_body_prefix)}
            <Mention>{t(($) => $.welcome.illustration.card5_mention_you)}</Mention>
            {t(($) => $.welcome.illustration.card5_body_suffix)}
          </>
        }
        status="done"
        timestamp={t(($) => $.welcome.illustration.card5_timestamp)}
      />
    </div>
  );
}

type ProviderName =
  | "claude"
  | "codex"
  | "opencode"
  | "hermes"
  | "kimi"
  | "kiro"
  | "qoder"
  | "pi"
  | "copilot"
  | "cursor";

type ActivityActor =
  | { kind: "user"; name: string; initial: string }
  | { kind: "agent"; name: string; provider: ProviderName };

function MockActivityCard({
  actor,
  issueId,
  content,
  status,
  timestamp,
  className,
}: {
  actor: ActivityActor;
  issueId: string;
  content: React.ReactNode;
  status?: Extract<IssueStatus, "in_progress" | "done" | "in_review">;
  timestamp?: string;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "rounded-lg border bg-card px-4 py-3.5 shadow-sm",
        // 装饰性 hover：上浮、转正、阴影加深。卡片不可点——这些动效只是氛围感，
        // 让插图像真实 App 界面而不是一张平面的截图。
        "transition-all duration-200 ease-out will-change-transform",
        "hover:-translate-y-0.5 hover:rotate-0 hover:shadow-md",
        className,
      )}
    >
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2">
          <MockAvatar actor={actor} />
          <span className="truncate text-body font-medium text-foreground">
            {actor.name}
          </span>
        </div>
        <span className="shrink-0 font-mono text-micro text-muted-foreground">
          {issueId}
        </span>
      </div>

      <p className="mt-2.5 text-body leading-snug text-foreground">
        {content}
      </p>

      {status && <StatusFooter status={status} timestamp={timestamp} />}
    </div>
  );
}

function MockAvatar({ actor }: { actor: ActivityActor }) {
  if (actor.kind === "user") {
    return (
      <div
        aria-hidden
        className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-foreground text-micro font-semibold text-background"
      >
        {actor.initial}
      </div>
    );
  }
  return (
    <div
      aria-hidden
      className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full border bg-muted/40 text-foreground"
    >
      <ProviderLogo provider={actor.provider} className="h-3.5 w-3.5" />
    </div>
  );
}

function StatusFooter({
  status,
  timestamp,
}: {
  status: IssueStatus;
  timestamp?: string;
}) {
  // 插画里的状态都是硬编码的内置 key，所以文案要像真实任务的 `useStatusLabel`
  // 那样从 i18n 取（MUL-6243）——`STATUS_CONFIG` 仍负责图标颜色，但它的 `.label`
  // 是服务端的英文种子文案，绝不能直接上屏。
  const { t } = useT("issues");
  const category = statusCategoryOfKey(status);
  const cfg = STATUS_CONFIG[category];
  return (
    <div className="mt-3 flex items-center gap-2 text-caption">
      <span
        className={cn("flex items-center gap-1.5 font-medium", cfg.iconColor)}
      >
        <StatusIcon
          status={status}
          className={cn(
            "h-3.5 w-3.5",
            status === "in_progress" && "animate-pulse",
          )}
        />
        {t(($) => $.status[category])}
      </span>
      {timestamp && (
        <>
          <span className="text-muted-foreground">·</span>
          <span className="text-muted-foreground">{timestamp}</span>
        </>
      )}
    </div>
  );
}

function Mention({ children }: { children: React.ReactNode }) {
  return <span className="font-medium text-brand">{children}</span>;
}
